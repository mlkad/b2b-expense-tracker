// Package auth mints and verifies the credentials the API accepts.
//
// The access token is the only thing that asserts a tenant, so the claim
// carrying it is the security boundary of the whole multi-tenant design: a
// token whose tenant claim can be altered is a token that reads another
// customer's data. Everything here exists to make that claim unforgeable and
// short-lived.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var (
	ErrTokenInvalid = errors.New("token is not valid")
	ErrTokenExpired = errors.New("token has expired")
)

// MinSecretBytes is the HMAC key floor. An HS256 key shorter than the 256-bit
// digest adds nothing over a 256-bit one, and a short one is usually a
// passphrase somebody typed - which is guessable.
const MinSecretBytes = 32

type Config struct {
	Secret   string
	Issuer   string
	Audience string
	TTL      time.Duration
}

// Claims is the access token payload.
//
// TenantID is the load-bearing one. It is not a convenience: it is what the
// middleware binds the database session to, and therefore what row-level
// security filters on. Email is carried for logs and the UI and is never used
// for a lookup, because a user can change it while a token is still live.
//
// The role is deliberately NOT here. A token issued before a demotion would
// keep its old authority for the rest of its lifetime, and fifteen minutes in
// which a removed employee can still approve payments is not an acceptable
// trade for one indexed lookup per request. The role comes from ResolveActor.
type Claims struct {
	jwt.RegisteredClaims
	TenantID string `json:"tid"`
	Email    string `json:"email,omitempty"`
}

type TokenService struct {
	secret   []byte
	issuer   string
	audience string
	ttl      time.Duration
	parser   *jwt.Parser
}

func NewTokenService(cfg Config) (*TokenService, error) {
	if len(cfg.Secret) < MinSecretBytes {
		return nil, fmt.Errorf("jwt secret must be at least %d bytes, got %d", MinSecretBytes, len(cfg.Secret))
	}
	if cfg.Issuer == "" || cfg.Audience == "" {
		return nil, errors.New("jwt issuer and audience are required")
	}
	if cfg.TTL <= 0 {
		return nil, errors.New("jwt ttl must be positive")
	}

	return &TokenService{
		secret:   []byte(cfg.Secret),
		issuer:   cfg.Issuer,
		audience: cfg.Audience,
		ttl:      cfg.TTL,
		parser: jwt.NewParser(
			// Pinning the algorithm is defence in depth rather than the only
			// guard: jwt/v5 refuses alg:none on its own, and the keyfunc below
			// always returns []byte, so an RS256 header fails on key type
			// before any signature is checked. The allowlist is what keeps
			// that true if the keyfunc ever returns more than one key type,
			// which is where algorithm confusion actually becomes reachable.
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
			jwt.WithIssuer(cfg.Issuer),
			jwt.WithAudience(cfg.Audience),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
		),
	}, nil
}

func (s *TokenService) TTL() time.Duration { return s.ttl }

// Issue mints an access token for one user acting in one tenant.
//
// A session that spans tenants gets one token per tenant rather than one token
// listing several. A multi-tenant token would mean the tenant is chosen by
// whatever the request says it is, which puts tenant selection back in the
// hands of the client.
func (s *TokenService) Issue(userID, tenantID uuid.UUID, email string) (string, time.Time, error) {
	if userID == uuid.Nil || tenantID == uuid.Nil {
		return "", time.Time{}, errors.New("cannot issue a token without both a user and a tenant")
	}

	now := time.Now().UTC()
	expiresAt := now.Add(s.ttl)

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			Issuer:    s.issuer,
			Audience:  jwt.ClaimStrings{s.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        uuid.NewString(),
		},
		TenantID: tenantID.String(),
		Email:    email,
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, expiresAt, nil
}

// Subject is a verified identity: which user, acting in which tenant.
type Subject struct {
	UserID   uuid.UUID
	TenantID uuid.UUID
	Email    string
	TokenID  string
}

// Parse verifies a token and returns its subject.
//
// Every failure other than expiry collapses to ErrTokenInvalid. The caller has
// no legitimate use for the distinction between a bad signature, a wrong
// audience and a malformed segment, and reporting it tells a forger which part
// to fix next. Expiry is separated because a client does have a legitimate
// response to it: refresh.
func (s *TokenService) Parse(token string) (Subject, error) {
	var claims Claims
	_, err := s.parser.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		return s.secret, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return Subject{}, ErrTokenExpired
		}
		return Subject{}, fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil || userID == uuid.Nil {
		return Subject{}, fmt.Errorf("%w: subject is not a uuid", ErrTokenInvalid)
	}

	// A token without a parseable tenant is rejected outright rather than
	// treated as "no tenant". The alternative would be a request that reaches
	// the database with no binding, and under the fail-closed policies that
	// returns an empty result set that reads like an empty account.
	tenantID, err := uuid.Parse(claims.TenantID)
	if err != nil || tenantID == uuid.Nil {
		return Subject{}, fmt.Errorf("%w: tenant claim is missing or malformed", ErrTokenInvalid)
	}

	return Subject{
		UserID:   userID,
		TenantID: tenantID,
		Email:    claims.Email,
		TokenID:  claims.ID,
	}, nil
}
