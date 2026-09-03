package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const relaySecret = "a-relay-secret-that-is-at-least-32-bytes"

func testRelay(t *testing.T) *Relay {
	t.Helper()
	r, err := NewRelay(relaySecret, DefaultTolerance)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func testBody(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(Event{
		ID:        "evt_test",
		Type:      EventSubscriptionUpdated,
		CreatedAt: time.Now().UTC(),
		TenantRef: "0b8f2c1e-0000-0000-0000-000000000001",
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestNewRelayRejectsAShortSecret(t *testing.T) {
	if _, err := NewRelay("too-short", DefaultTolerance); err == nil {
		t.Fatal("accepted a short relay secret")
	}
}

func TestVerifyAcceptsAGenuineDelivery(t *testing.T) {
	relay, body := testRelay(t), testBody(t)
	now := time.Now()

	event, err := relay.Verify(relay.Sign(body, now), body, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if event.ID != "evt_test" || event.Type != EventSubscriptionUpdated {
		t.Fatalf("decoded %+v", event)
	}
}

func TestVerifyRejects(t *testing.T) {
	relay, body := testRelay(t), testBody(t)
	now := time.Now()
	valid := relay.Sign(body, now)

	other, err := NewRelay("a-completely-different-secret-of-length!!", DefaultTolerance)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		header string
		body   []byte
		want   error
	}{
		"missing header":         {"", body, ErrSignatureMissing},
		"no timestamp":           {"v1=deadbeef", body, ErrSignatureInvalid},
		"no signature":           {fmt.Sprintf("t=%d", now.Unix()), body, ErrSignatureInvalid},
		"timestamp not a number": {"t=yesterday,v1=deadbeef", body, ErrSignatureInvalid},
		"garbage":                {"nonsense", body, ErrSignatureInvalid},
		"another secret":         {other.Sign(body, now), body, ErrSignatureInvalid},
		"wrong signature":        {fmt.Sprintf("t=%d,v1=%s", now.Unix(), strings.Repeat("00", 32)), body, ErrSignatureInvalid},
		"tampered body":          {valid, append([]byte(nil), append(body[:len(body)-1], '!')...), ErrSignatureInvalid},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := relay.Verify(c.header, c.body, now); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

// The timestamp is inside the signed payload, so a captured delivery cannot be
// re-dated. Without that, one intercepted `subscription.updated` carrying an
// old `active` could be replayed forever to keep a cancelled tenant's plan.
func TestReplayOutsideToleranceIsRejected(t *testing.T) {
	relay, body := testRelay(t), testBody(t)
	signedAt := time.Now()
	header := relay.Sign(body, signedAt)

	for name, at := range map[string]time.Time{
		"too old":       signedAt.Add(DefaultTolerance + time.Minute),
		"too far ahead": signedAt.Add(-DefaultTolerance - time.Minute),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := relay.Verify(header, body, at); !errors.Is(err, ErrSignatureExpired) {
				t.Fatalf("got %v, want ErrSignatureExpired", err)
			}
		})
	}

	t.Run("modest clock skew is tolerated", func(t *testing.T) {
		if _, err := relay.Verify(header, body, signedAt.Add(DefaultTolerance-time.Second)); err != nil {
			t.Fatalf("a delivery inside the window was rejected: %v", err)
		}
		if _, err := relay.Verify(header, body, signedAt.Add(-DefaultTolerance+time.Second)); err != nil {
			t.Fatalf("a delivery from a slightly fast sender was rejected: %v", err)
		}
	})
}

// During a secret rotation the gateway sends several signatures. Any one
// matching must be enough, or every delivery fails for the duration of the
// rotation.
func TestSecretRotationAcceptsAnyMatchingSignature(t *testing.T) {
	relay, body := testRelay(t), testBody(t)
	now := time.Now()

	old, err := NewRelay("the-previous-secret-also-long-enough-ok!!", DefaultTolerance)
	if err != nil {
		t.Fatal(err)
	}

	// Old signature first, current second - and the reverse - so the test does
	// not pass merely because the right one happened to be checked first.
	current := strings.TrimPrefix(relay.Sign(body, now), fmt.Sprintf("t=%d,", now.Unix()))
	previous := strings.TrimPrefix(old.Sign(body, now), fmt.Sprintf("t=%d,", now.Unix()))

	for name, header := range map[string]string{
		"current first":  fmt.Sprintf("t=%d,%s,%s", now.Unix(), current, previous),
		"previous first": fmt.Sprintf("t=%d,%s,%s", now.Unix(), previous, current),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := relay.Verify(header, body, now); err != nil {
				t.Fatalf("a header carrying a valid signature was rejected: %v", err)
			}
		})
	}

	t.Run("a malformed candidate does not sink a valid one", func(t *testing.T) {
		header := fmt.Sprintf("t=%d,v1=not-hex,%s", now.Unix(), current)
		if _, err := relay.Verify(header, body, now); err != nil {
			t.Fatalf("an unreadable candidate rejected the whole delivery: %v", err)
		}
	})

	t.Run("no valid signature is still refused", func(t *testing.T) {
		header := fmt.Sprintf("t=%d,%s", now.Unix(), previous)
		if _, err := relay.Verify(header, body, now); !errors.Is(err, ErrSignatureInvalid) {
			t.Fatalf("got %v, want ErrSignatureInvalid", err)
		}
	})
}

// A body that verifies but is not an event must not reach the ingest path.
func TestVerifyRejectsAWellSignedNonEvent(t *testing.T) {
	relay := testRelay(t)
	now := time.Now()

	for name, body := range map[string][]byte{
		"not json":  []byte(`{not json`),
		"no id":     []byte(`{"type":"subscription.updated"}`),
		"no type":   []byte(`{"id":"evt_1"}`),
		"empty obj": []byte(`{}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := relay.Verify(relay.Sign(body, now), body, now); !errors.Is(err, ErrSignatureInvalid) {
				t.Fatalf("got %v, want ErrSignatureInvalid", err)
			}
		})
	}
}
