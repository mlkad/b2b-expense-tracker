package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const serviceSecret = "a-service-secret-that-is-32-bytes-or-more"

func testClient(t *testing.T, handler http.Handler, mutate ...func(*Config)) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := Config{BaseURL: srv.URL, ServiceSecret: serviceSecret, Timeout: 2 * time.Second, MaxRetries: 2}
	for _, m := range mutate {
		m(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv
}

func TestNewRejectsIncompleteConfiguration(t *testing.T) {
	if _, err := New(Config{ServiceSecret: serviceSecret}); err == nil {
		t.Error("accepted a client with no base url")
	}
	if _, err := New(Config{BaseURL: "http://x", ServiceSecret: "short"}); err == nil {
		t.Error("accepted a short service secret")
	}
}

// The gateway authenticates an end user's bearer token; a server-to-server
// caller has none, so it presents a signed service token whose subject is the
// tenant's customer reference.
func TestServiceTokenIsPresentedAndScopedToTheTenant(t *testing.T) {
	customerRef := uuid.NewString()
	var seen string

	client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(Subscription{SubscriptionID: "sub_1", Status: "active"})
	}))

	if _, err := client.GetSubscription(context.Background(), uuid.New(), customerRef); err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}

	raw, ok := strings.CutPrefix(seen, "Bearer ")
	if !ok {
		t.Fatalf("Authorization header was %q", seen)
	}

	var claims jwt.RegisteredClaims
	parsed, err := jwt.NewParser(jwt.WithValidMethods([]string{"HS256"})).
		ParseWithClaims(raw, &claims, func(*jwt.Token) (any, error) { return []byte(serviceSecret), nil })
	if err != nil || !parsed.Valid {
		t.Fatalf("the presented token does not verify: %v", err)
	}
	if claims.Subject != customerRef {
		t.Errorf("subject = %q, want the customer reference %q", claims.Subject, customerRef)
	}
	if len(claims.Audience) == 0 || claims.Audience[0] != "stripe-payment-service" {
		t.Errorf("audience = %v; the gateway must be able to reject a token minted for something else", claims.Audience)
	}
	// A credential that outlives the call it was minted for can be replayed
	// later out of a heap dump.
	if until := time.Until(claims.ExpiresAt.Time); until > 2*time.Minute {
		t.Errorf("service token lives for %s; it should expire in about a minute", until)
	}
}

func TestRetriesOnlyWhatIsWorthRetrying(t *testing.T) {
	t.Run("5xx is retried and can succeed", func(t *testing.T) {
		var calls int32
		client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if atomic.AddInt32(&calls, 1) < 3 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			json.NewEncoder(w).Encode(Subscription{SubscriptionID: "sub_1", Status: "active"})
		}))

		sub, err := client.GetSubscription(context.Background(), uuid.New(), "cus_1")
		if err != nil {
			t.Fatalf("GetSubscription: %v", err)
		}
		if sub.SubscriptionID != "sub_1" {
			t.Fatalf("decoded %+v", sub)
		}
		if got := atomic.LoadInt32(&calls); got != 3 {
			t.Fatalf("made %d attempts, want 3", got)
		}
	})

	t.Run("5xx that never recovers reports unavailable", func(t *testing.T) {
		var calls int32
		client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusInternalServerError)
		}))

		_, err := client.GetSubscription(context.Background(), uuid.New(), "cus_1")
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("got %v, want ErrUnavailable", err)
		}
		if got := atomic.LoadInt32(&calls); got != 3 {
			t.Fatalf("made %d attempts, want MaxRetries+1 = 3", got)
		}
	})

	t.Run("4xx is final", func(t *testing.T) {
		var calls int32
		client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"message": "price_id is required"})
		}))

		_, err := client.StartCheckout(context.Background(), uuid.New(), "cus_1", CheckoutRequest{})
		if !errors.Is(err, ErrRejected) {
			t.Fatalf("got %v, want ErrRejected", err)
		}
		if !strings.Contains(err.Error(), "price_id is required") {
			t.Errorf("the gateway's own message was lost: %v", err)
		}
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Fatalf("a 4xx was retried %d times; it will fail identically every time", got)
		}
	})

	t.Run("404 is a normal answer, not a failure", func(t *testing.T) {
		client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		if _, err := client.GetSubscription(context.Background(), uuid.New(), "cus_1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("got %v, want ErrNotFound", err)
		}
	})
}

// A POST that 500s might have created a session, so retrying it could produce
// two. The idempotency key is what lets the gateway resolve the retry to the
// session the first attempt created.
func TestPostsCarryAStableIdempotencyKey(t *testing.T) {
	var keys []string
	client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(keys) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(CheckoutSession{URL: "https://checkout.example/s/1"})
	}))

	tenantID := uuid.New()
	if _, err := client.StartCheckout(context.Background(), tenantID, "cus_1", CheckoutRequest{PriceID: "price_1"}); err != nil {
		t.Fatalf("StartCheckout: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("made %d attempts, want 2", len(keys))
	}
	if keys[0] == "" {
		t.Fatal("no idempotency key on a POST")
	}
	if keys[0] != keys[1] {
		t.Fatalf("the retry used a different key (%q vs %q); the gateway would create a second session",
			keys[0], keys[1])
	}
	if !strings.Contains(keys[0], tenantID.String()) {
		t.Errorf("the key is not scoped to the tenant: %q", keys[0])
	}
}

// A hostile or broken gateway must not be able to exhaust this process's
// memory with an unbounded response body.
func TestResponseBodyIsBounded(t *testing.T) {
	client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"`))
		// Written in chunks rather than a byte at a time: two million single
		// writes take long enough under the race detector to turn this into a
		// timeout test by accident, which would pass for the wrong reason.
		chunk := bytes.Repeat([]byte("A"), 64<<10)
		for written := 0; written < 2<<20; written += len(chunk) {
			w.Write(chunk)
		}
		w.Write([]byte(`"}`))
	}))

	// Truncated at the limit, so it can no longer be valid JSON - which is the
	// point: the read stops rather than growing without bound.
	if _, err := client.GetSubscription(context.Background(), uuid.New(), "cus_1"); err == nil {
		t.Fatal("an oversized response was accepted")
	}
}

func TestContextCancellationStopsTheRetryLoop(t *testing.T) {
	var attempts int32
	client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.GetSubscription(ctx, uuid.New(), "cus_1")
	if err == nil {
		t.Fatal("a cancelled request succeeded")
	}
	// Asserted on the error rather than on elapsed wall-clock time. A
	// stopwatch here would be a test that fails on a loaded CI runner for
	// reasons that have nothing to do with the retry loop, and the thing
	// actually worth checking is that the loop reports the cancellation rather
	// than swallowing it and continuing.
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want it to report the cancellation", err)
	}
	if atomic.LoadInt32(&attempts) > 1 {
		t.Fatalf("made %d attempts after the context was cancelled, want at most 1",
			atomic.LoadInt32(&attempts))
	}
}
