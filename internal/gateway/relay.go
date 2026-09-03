package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
)

// Relay verifies events forwarded by the payment gateway.
//
// The gateway receives Stripe's webhooks, settles their ordering and
// idempotency, and forwards a normalised subscription event to this service.
// That indirection is what keeps Stripe's signing secret in exactly one
// service, and it means this receiver deals with one small event shape rather
// than with the whole Stripe API surface.
//
// The signature scheme mirrors Stripe's, for a reason that is not imitation:
// the timestamp is inside the signed payload. A scheme that signs only the
// body lets an attacker who captures one delivery replay it forever - and a
// replayed `subscription.updated` carrying an old `active` status is how a
// cancelled tenant keeps its plan.
type Relay struct {
	secret    []byte
	tolerance time.Duration
}

// SignatureHeader is where the signature travels.
const SignatureHeader = "X-Billing-Signature"

var (
	ErrSignatureMissing = errors.New("relay signature header is missing")
	ErrSignatureInvalid = errors.New("relay signature does not verify")
	ErrSignatureExpired = errors.New("relay signature is outside the tolerance window")
)

// DefaultTolerance is how far a delivery's timestamp may be from now.
//
// Five minutes is Stripe's own default and is a compromise between clock skew
// between two machines - which is real, and can be tens of seconds on a badly
// configured host - and the window in which a captured delivery can be
// replayed.
const DefaultTolerance = 5 * time.Minute

func NewRelay(secret string, tolerance time.Duration) (*Relay, error) {
	if len(secret) < 32 {
		return nil, errors.New("relay secret must be at least 32 bytes")
	}
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	return &Relay{secret: []byte(secret), tolerance: tolerance}, nil
}

// Event is the payload the gateway forwards.
type Event struct {
	// ID is the gateway's event id, used as the idempotency key. It is
	// untrusted until the signature verifies - which is why the receiver must
	// verify before it claims the id. Claiming first would let anyone POST a
	// guessed id, plant a settled row, and have the genuine delivery discarded
	// as a duplicate.
	ID string `json:"id"`

	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`

	// TenantRef is the tenant's gateway customer reference, which this service
	// stores as tenants.billing_customer_ref.
	TenantRef string `json:"tenant_ref"`

	Subscription *Subscription `json:"subscription,omitempty"`
}

// Known event types. An unrecognised type is acknowledged and recorded as
// skipped rather than rejected: the gateway would otherwise redeliver it
// forever, and a redelivery backlog is a worse outcome than an ignored event.
const (
	EventSubscriptionCreated = "subscription.created"
	EventSubscriptionUpdated = "subscription.updated"
	EventSubscriptionDeleted = "subscription.deleted"
	EventPaymentFailed       = "invoice.payment_failed"
	EventPaymentSucceeded    = "invoice.payment_succeeded"
)

// Verify checks a delivery and decodes it.
//
// The body is passed as raw bytes because the signature covers the exact bytes
// received. Re-encoding a decoded struct and signing that would fail on any
// difference in key order or whitespace, and worse, would pass on a body whose
// unparsed remainder differs from what was signed.
func (r *Relay) Verify(header string, body []byte, now time.Time) (*Event, error) {
	if header == "" {
		return nil, ErrSignatureMissing
	}

	timestamp, signatures, err := parseSignatureHeader(header)
	if err != nil {
		return nil, err
	}

	// Timestamp before signature: an expired delivery is rejected without
	// spending an HMAC on it, and there is nothing secret about the age of a
	// message the caller supplied.
	age := now.Sub(time.Unix(timestamp, 0))
	if age > r.tolerance || age < -r.tolerance {
		return nil, fmt.Errorf("%w: delivery is %s old", ErrSignatureExpired, age.Round(time.Second))
	}

	mac := hmac.New(sha256.New, r.secret)
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)
	expected := mac.Sum(nil)

	// The gateway may send several signatures during a secret rotation. Any
	// one matching is enough, and every candidate is compared in constant
	// time - a short-circuiting comparison leaks how many leading bytes of a
	// forged signature were right, which is enough to forge one byte at a time.
	matched := false
	for _, candidate := range signatures {
		if auth.ConstantTimeCompare(expected, candidate) {
			matched = true
		}
	}
	if !matched {
		return nil, ErrSignatureInvalid
	}

	var event Event
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, fmt.Errorf("%w: body is not valid json", ErrSignatureInvalid)
	}
	if event.ID == "" || event.Type == "" {
		return nil, fmt.Errorf("%w: event is missing id or type", ErrSignatureInvalid)
	}
	return &event, nil
}

// Sign produces the header for a body. It exists so the tests can generate
// genuine deliveries, and so the gateway side of the contract is written down
// in code rather than only in prose.
func (r *Relay) Sign(body []byte, at time.Time) string {
	mac := hmac.New(sha256.New, r.secret)
	fmt.Fprintf(mac, "%d.", at.Unix())
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", at.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

// parseSignatureHeader reads `t=<unix>,v1=<hex>[,v1=<hex>...]`.
func parseSignatureHeader(header string) (timestamp int64, signatures [][]byte, err error) {
	var seenTimestamp bool

	for _, part := range strings.Split(header, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		switch key {
		case "t":
			ts, convErr := strconv.ParseInt(value, 10, 64)
			if convErr != nil {
				return 0, nil, fmt.Errorf("%w: timestamp is not a unix time", ErrSignatureInvalid)
			}
			timestamp, seenTimestamp = ts, true
		case "v1":
			raw, decErr := hex.DecodeString(value)
			if decErr != nil {
				// A malformed candidate is skipped rather than fatal: during a
				// rotation one of several signatures being unreadable should
				// not reject a delivery the other one would have verified.
				continue
			}
			signatures = append(signatures, raw)
		}
	}

	if !seenTimestamp {
		return 0, nil, fmt.Errorf("%w: no timestamp in the signature header", ErrSignatureInvalid)
	}
	if len(signatures) == 0 {
		return 0, nil, fmt.Errorf("%w: no usable signature in the header", ErrSignatureInvalid)
	}
	return timestamp, signatures, nil
}
