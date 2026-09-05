package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// DownloadTokenAudience is deliberately different from the API's audience.
//
// The two token types must not be interchangeable. An access token presented
// as a download token would be a credential in a URL - and therefore in browser
// history, in a proxy log and in a referrer header - with fifteen minutes of
// life and the full API behind it. A download token presented as a bearer would
// be a way to bypass every other permission check. Verifying the audience on
// both parsers is what keeps them apart.
const DownloadTokenAudience = "b2b-expense-tracker-download"

// DownloadTokenTTL is how long a signed download URL is usable.
//
// One minute. The token travels in a query string, which is the one place a
// credential is guaranteed to be written down by something other than the
// browser: access logs record the full path, and a link pasted into a chat
// carries it. The lifetime is therefore the window in which a leaked URL works,
// and a download starts within a second of the click.
const DownloadTokenTTL = time.Minute

// DownloadClaims is what a download token asserts.
//
// It carries the query it was minted for. Without that, a token issued for
// "March, my own department" would also fetch the whole organisation's history
// - the export handler reads its filters from the URL, so anything not bound
// into the signature is a parameter the holder can change.
type DownloadClaims struct {
	jwt.RegisteredClaims
	TenantID string `json:"tid"`
	Query    string `json:"qry"`
}

// DownloadTokens mints and verifies them.
type DownloadTokens struct {
	secret []byte
	issuer string
	parser *jwt.Parser

	// now is injectable so a test can mint a token in the past. A test that
	// proved expiry by sleeping for a minute is one nobody would keep.
	now func() time.Time
}

func NewDownloadTokens(secret, issuer string) (*DownloadTokens, error) {
	if len(secret) < MinSecretBytes {
		return nil, fmt.Errorf("download token secret must be at least %d bytes", MinSecretBytes)
	}
	if issuer == "" {
		return nil, errors.New("download token issuer is required")
	}

	return &DownloadTokens{
		secret: []byte(secret),
		issuer: issuer,
		now:    time.Now,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
			jwt.WithIssuer(issuer),
			jwt.WithAudience(DownloadTokenAudience),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
		),
	}, nil
}

// Issue mints a token for one user, one tenant and one exact query.
func (d *DownloadTokens) Issue(subject Subject, query string) (string, time.Time, error) {
	if subject.UserID == uuid.Nil || subject.TenantID == uuid.Nil {
		return "", time.Time{}, errors.New("cannot mint a download token without a user and a tenant")
	}

	now := d.now().UTC()
	expiresAt := now.Add(DownloadTokenTTL)

	claims := DownloadClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject.UserID.String(),
			Issuer:    d.issuer,
			Audience:  jwt.ClaimStrings{DownloadTokenAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        uuid.NewString(),
		},
		TenantID: subject.TenantID.String(),
		Query:    query,
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(d.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign download token: %w", err)
	}
	return signed, expiresAt, nil
}

// Parse verifies a token and checks it was minted for this exact query.
//
// The query comparison is the part that matters. Everything else only proves
// the token is genuine; this proves it is being used for the report it was
// issued for, rather than for one the holder rewrote in the address bar.
func (d *DownloadTokens) Parse(token, query string) (Subject, error) {
	var claims DownloadClaims
	if _, err := d.parser.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) {
		return d.secret, nil
	}); err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return Subject{}, ErrTokenExpired
		}
		return Subject{}, fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil || userID == uuid.Nil {
		return Subject{}, fmt.Errorf("%w: subject is not a uuid", ErrTokenInvalid)
	}
	tenantID, err := uuid.Parse(claims.TenantID)
	if err != nil || tenantID == uuid.Nil {
		return Subject{}, fmt.Errorf("%w: tenant claim is missing or malformed", ErrTokenInvalid)
	}

	if claims.Query != query {
		return Subject{}, fmt.Errorf("%w: this token was issued for a different report", ErrTokenInvalid)
	}

	return Subject{UserID: userID, TenantID: tenantID}, nil
}
