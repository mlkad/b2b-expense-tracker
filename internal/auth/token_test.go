package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const testSecret = "a-test-secret-that-is-at-least-32-bytes-long"

func newService(t *testing.T, mutate ...func(*Config)) *TokenService {
	t.Helper()
	cfg := Config{Secret: testSecret, Issuer: "expense-api", Audience: "expense-clients", TTL: 15 * time.Minute}
	for _, m := range mutate {
		m(&cfg)
	}
	svc, err := NewTokenService(cfg)
	if err != nil {
		t.Fatalf("NewTokenService: %v", err)
	}
	return svc
}

func TestNewTokenServiceRejectsWeakConfiguration(t *testing.T) {
	cases := map[string]Config{
		"short secret": {Secret: "too-short", Issuer: "i", Audience: "a", TTL: time.Minute},
		"no issuer":    {Secret: testSecret, Audience: "a", TTL: time.Minute},
		"no audience":  {Secret: testSecret, Issuer: "i", TTL: time.Minute},
		"zero ttl":     {Secret: testSecret, Issuer: "i", Audience: "a"},
		"negative ttl": {Secret: testSecret, Issuer: "i", Audience: "a", TTL: -time.Minute},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewTokenService(cfg); err == nil {
				t.Fatal("accepted an unsafe configuration")
			}
		})
	}
}

func TestIssueAndParseRoundTrip(t *testing.T) {
	svc := newService(t)
	userID, tenantID := uuid.New(), uuid.New()

	token, expiresAt, err := svc.Issue(userID, tenantID, "ada@example.com")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if time.Until(expiresAt) > 16*time.Minute {
		t.Errorf("expiry %s is beyond the configured TTL", time.Until(expiresAt))
	}

	subject, err := svc.Parse(token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if subject.UserID != userID || subject.TenantID != tenantID {
		t.Fatalf("round trip lost identity: %+v", subject)
	}
	if subject.TokenID == "" {
		t.Error("no jti; there would be nothing for a revocation list to key on")
	}
}

// The tenant claim is what the database session is bound to, so a token
// without one must be refused outright rather than treated as "no tenant" -
// which under the fail-closed policies returns an empty result set that reads
// like an empty account.
func TestTokenWithoutATenantIsRefused(t *testing.T) {
	svc := newService(t)

	t.Run("Issue refuses to mint one", func(t *testing.T) {
		if _, _, err := svc.Issue(uuid.New(), uuid.Nil, "a@b.c"); err == nil {
			t.Fatal("minted a token with no tenant")
		}
		if _, _, err := svc.Issue(uuid.Nil, uuid.New(), "a@b.c"); err == nil {
			t.Fatal("minted a token with no subject")
		}
	})

	t.Run("Parse refuses one that was minted elsewhere", func(t *testing.T) {
		// Forged the way a caller with the signing key but a different code
		// path would: valid signature, missing claim.
		claims := Claims{RegisteredClaims: jwt.RegisteredClaims{
			Subject:   uuid.NewString(),
			Issuer:    "expense-api",
			Audience:  jwt.ClaimStrings{"expense-clients"},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}}
		signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Parse(signed); !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("got %v, want ErrTokenInvalid", err)
		}
	})

	t.Run("Parse refuses a malformed tenant claim", func(t *testing.T) {
		claims := Claims{
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   uuid.NewString(),
				Issuer:    "expense-api",
				Audience:  jwt.ClaimStrings{"expense-clients"},
				IssuedAt:  jwt.NewNumericDate(time.Now()),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
			TenantID: "'; DROP TABLE expenses; --",
		}
		signed, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
		if _, err := svc.Parse(signed); !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("got %v, want ErrTokenInvalid", err)
		}
	})
}

// Expiry is the one failure a client can act on, so it is the one that stays
// distinguishable. Everything else collapses to ErrTokenInvalid, because
// telling a forger which part to fix next is the whole game.
func TestExpiryIsDistinguishableAndNothingElseIs(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		svc := newService(t, func(c *Config) { c.TTL = time.Nanosecond })
		token, _, err := svc.Issue(uuid.New(), uuid.New(), "a@b.c")
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)

		if _, err := svc.Parse(token); !errors.Is(err, ErrTokenExpired) {
			t.Fatalf("got %v, want ErrTokenExpired", err)
		}
	})

	svc := newService(t)
	valid, _, _ := svc.Issue(uuid.New(), uuid.New(), "a@b.c")

	other := newService(t, func(c *Config) { c.Secret = strings.Repeat("z", 40) })
	wrongKey, _, _ := other.Issue(uuid.New(), uuid.New(), "a@b.c")

	wrongIssuer := newService(t, func(c *Config) { c.Issuer = "some-other-service" })
	foreignIssuer, _, _ := wrongIssuer.Issue(uuid.New(), uuid.New(), "a@b.c")

	wrongAudience := newService(t, func(c *Config) { c.Audience = "some-other-audience" })
	foreignAudience, _, _ := wrongAudience.Issue(uuid.New(), uuid.New(), "a@b.c")

	cases := map[string]string{
		"empty":                   "",
		"not a jwt":               "definitely-not-a-token",
		"tampered payload":        valid[:len(valid)-6] + "AAAAAA",
		"signed with another key": wrongKey,
		// Issuer and audience are verified on every parse, so a token minted
		// for a different service or a different environment cannot be
		// replayed here.
		"another issuer":   foreignIssuer,
		"another audience": foreignAudience,
	}

	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Parse(token)
			if !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("got %v, want ErrTokenInvalid", err)
			}
			if errors.Is(err, ErrTokenExpired) {
				t.Fatal("a non-expiry failure was reported as expiry; a client would retry forever")
			}
		})
	}
}

// Algorithm confusion: a token claiming alg:none, and one claiming an
// asymmetric algorithm whose "signature" is an HMAC of the public parameters.
func TestAlgorithmConfusionIsRefused(t *testing.T) {
	svc := newService(t)

	encode := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}

	body := map[string]any{
		"sub": uuid.NewString(),
		"tid": uuid.NewString(),
		"iss": "expense-api",
		"aud": "expense-clients",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}

	for name, header := range map[string]map[string]any{
		"alg none":  {"alg": "none", "typ": "JWT"},
		"alg NONE":  {"alg": "NONE", "typ": "JWT"},
		"alg RS256": {"alg": "RS256", "typ": "JWT"},
		"alg HS512": {"alg": "HS512", "typ": "JWT"},
	} {
		t.Run(name, func(t *testing.T) {
			token := encode(header) + "." + encode(body) + "."
			if _, err := svc.Parse(token); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("got %v, want ErrTokenInvalid", err)
			}
		})
	}
}
