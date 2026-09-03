// Package gateway is this service's client for the Stripe Payment &
// Subscription Gateway (project #1).
//
// The division of responsibility between the two services is deliberate and
// worth stating, because it is the reason this package is small:
//
//   - The gateway owns Stripe. It holds the API key, receives Stripe's
//     webhooks, resolves their ordering and idempotency, and keeps the
//     authoritative subscription record. This service never sees a cus_ or a
//     sub_ id and never calls api.stripe.com.
//   - This service owns entitlement. It keeps a local projection of the
//     subscription (tenant_subscriptions) and answers "may this tenant do
//     this" from that projection alone.
//
// Which means the gateway is never called on the request path. Every
// entitlement check is a local indexed read, so the expense product stays up
// when billing is down - and a customer whose card is being retried can still
// open their own records. The client here is used for exactly three things:
// starting a checkout, opening the customer portal, and the nightly
// reconciliation sweep that repairs drift if a relayed event was ever lost.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var (
	// ErrUnavailable means the gateway could not be reached or returned 5xx
	// after the retries were exhausted. Callers on the request path must
	// degrade rather than fail: an entitlement answer is already available
	// locally, and only checkout genuinely needs the gateway.
	ErrUnavailable = errors.New("payment gateway is unavailable")

	// ErrNotFound means the gateway has no subscription for this tenant. It is
	// a normal answer for a tenant that never started a checkout.
	ErrNotFound = errors.New("no subscription at the payment gateway")

	ErrRejected = errors.New("payment gateway rejected the request")
)

type Config struct {
	// BaseURL is the gateway's origin, e.g. https://billing.internal.
	BaseURL string

	// ServiceSecret signs the service token this client presents.
	//
	// The gateway's own routes authenticate an end user's bearer token. A
	// server-to-server caller has no end user, so it presents a token minted
	// with a secret the two services share, whose subject is the tenant's
	// gateway customer reference. The gateway accepts it on a service audience
	// only - see docs/BILLING_INTEGRATION.md for the contract, which is the
	// one addition this integration asks of project #1.
	ServiceSecret string

	Issuer   string
	Audience string

	Timeout    time.Duration
	MaxRetries int
}

func (c Config) withDefaults() Config {
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = 2
	}
	if c.Issuer == "" {
		c.Issuer = "b2b-expense-tracker"
	}
	if c.Audience == "" {
		c.Audience = "stripe-payment-service"
	}
	return c
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()
	if cfg.BaseURL == "" {
		return nil, errors.New("gateway base url is required")
	}
	if len(cfg.ServiceSecret) < 32 {
		return nil, errors.New("gateway service secret must be at least 32 bytes")
	}

	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout: cfg.Timeout,
			Transport: &http.Transport{
				// A small pool of long-lived connections. Every call here is
				// to one host, so the default per-host limit of 2 idle
				// connections would mean a TLS handshake on most calls during
				// the reconciliation sweep.
				MaxIdleConns:        16,
				MaxIdleConnsPerHost: 16,
				IdleConnTimeout:     90 * time.Second,
				ForceAttemptHTTP2:   true,
			},
		},
	}, nil
}

// Subscription is the gateway's view, in the shape this service needs.
type Subscription struct {
	SubscriptionID     string     `json:"id"`
	CustomerRef        string     `json:"customer_ref"`
	PlanCode           string     `json:"plan_code"`
	Status             string     `json:"status"`
	Seats              int        `json:"quantity"`
	CurrentPeriodStart time.Time  `json:"current_period_start"`
	CurrentPeriodEnd   time.Time  `json:"current_period_end"`
	CancelAtPeriodEnd  bool       `json:"cancel_at_period_end"`
	TrialEnd           *time.Time `json:"trial_end,omitempty"`
}

// GetSubscription reads the authoritative record for one tenant.
//
// Used by the nightly reconciliation job, not by request handling. If a
// relayed event was dropped - the receiver was down during a redelivery
// window, say - this is what notices and repairs it.
func (c *Client) GetSubscription(ctx context.Context, tenantID uuid.UUID, customerRef string) (*Subscription, error) {
	var sub Subscription
	err := c.do(ctx, http.MethodGet, "/api/v1/subscription", tenantID, customerRef, nil, &sub)
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

type CheckoutRequest struct {
	PriceID    string `json:"price_id"`
	Quantity   int    `json:"quantity,omitempty"`
	SuccessURL string `json:"success_url"`
	CancelURL  string `json:"cancel_url"`
}

type CheckoutSession struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// StartCheckout opens a Stripe Checkout session through the gateway.
//
// This is the one call that genuinely needs the gateway to be up, and it is
// the one place a 503 is the honest answer: there is no way to take a payment
// while the payment service is down, and pretending otherwise would leave a
// customer believing they had subscribed.
func (c *Client) StartCheckout(ctx context.Context, tenantID uuid.UUID, customerRef string, req CheckoutRequest) (*CheckoutSession, error) {
	var session CheckoutSession
	if err := c.do(ctx, http.MethodPost, "/api/v1/checkout", tenantID, customerRef, req, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

type PortalSession struct {
	URL string `json:"url"`
}

func (c *Client) OpenPortal(ctx context.Context, tenantID uuid.UUID, customerRef string, returnURL string) (*PortalSession, error) {
	var session PortalSession
	body := map[string]string{"return_url": returnURL}
	if err := c.do(ctx, http.MethodPost, "/api/v1/portal", tenantID, customerRef, body, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// do performs one call with retries.
//
// Only idempotent methods and only transport-level or 5xx failures are
// retried. A POST /checkout that returned 500 might have created a session, so
// retrying it could produce two - which is why StartCheckout passes an
// idempotency key derived from the tenant and the minute, and why a 4xx is
// never retried at all.
func (c *Client) do(ctx context.Context, method, path string, tenantID uuid.UUID, customerRef string, body, out any) error {
	token, err := c.serviceToken(customerRef)
	if err != nil {
		return err
	}

	var payload []byte
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}

	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff with full jitter. Without the jitter, every
			// replica retrying a gateway that just came back up hits it in
			// the same millisecond and knocks it over again.
			delay := time.Duration(1<<attempt) * 100 * time.Millisecond
			jittered := time.Duration(rand.Int63n(int64(delay)))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(jittered):
			}
		}

		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}

		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.cfg.BaseURL, "/")+path, reader)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
			// Scoped to the tenant and stable across the retries of one
			// logical call, so a redelivered POST resolves to the session the
			// first attempt created rather than a second one.
			req.Header.Set("Idempotency-Key", idempotencyKey(tenantID, path))
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%w: %v", ErrUnavailable, err)
			continue
		}

		err = decode(resp, out)
		resp.Body.Close()

		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrUnavailable) {
			return err // 4xx and decoding failures are final
		}
		lastErr = err
	}
	return lastErr
}

func decode(resp *http.Response, out any) error {
	// A hostile or broken gateway must not be able to exhaust this process's
	// memory with an unbounded response body.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: read response: %v", ErrUnavailable, err)
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode >= 500:
		return fmt.Errorf("%w: gateway returned %d", ErrUnavailable, resp.StatusCode)
	case resp.StatusCode >= 400:
		var problem struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &problem)
		if problem.Message == "" {
			problem.Message = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("%w: %s", ErrRejected, problem.Message)
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode gateway response: %w", err)
	}
	return nil
}

// serviceToken mints the short-lived credential this client presents.
//
// One minute of life. It is minted per call rather than cached because minting
// costs an HMAC and caching would mean holding a bearer credential in memory
// for its whole lifetime; a token that exists for the duration of one request
// cannot be replayed later from a heap dump.
func (c *Client) serviceToken(customerRef string) (string, error) {
	now := time.Now().UTC()
	claims := jwt.RegisteredClaims{
		Subject:   customerRef,
		Issuer:    c.cfg.Issuer,
		Audience:  jwt.ClaimStrings{c.cfg.Audience},
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
		ID:        uuid.NewString(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(c.cfg.ServiceSecret))
	if err != nil {
		return "", fmt.Errorf("sign service token: %w", err)
	}
	return signed, nil
}

func idempotencyKey(tenantID uuid.UUID, path string) string {
	// The minute bucket makes a user's repeated clicks within a minute resolve
	// to one session, while a genuine second attempt a minute later gets a
	// fresh one.
	return fmt.Sprintf("%s:%s:%d", tenantID, strings.TrimPrefix(path, "/api/v1/"), time.Now().Unix()/60)
}
