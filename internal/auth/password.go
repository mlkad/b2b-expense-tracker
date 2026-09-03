package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// BcryptCost is the work factor.
//
// 12 is roughly 250ms on current server hardware, which is the usual balance:
// slow enough that offline cracking of a leaked hash is expensive, fast enough
// that a login does not feel broken and that the endpoint cannot be turned
// into a denial of service by an attacker who simply submits many logins. The
// login route is rate limited for the same reason.
const BcryptCost = 12

// bcrypt silently truncates at 72 bytes, so a 200-character passphrase is
// really its first 72 characters. Refusing is better than accepting a password
// that is not the one the user chose.
const (
	MinPasswordLength = 12
	MaxPasswordBytes  = 72
)

var ErrPasswordMismatch = errors.New("password does not match")

func HashPassword(plain string) (string, error) {
	if err := ValidatePassword(plain); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), BcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

func ValidatePassword(plain string) error {
	if utf8.RuneCountInString(plain) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	if len(plain) > MaxPasswordBytes {
		return fmt.Errorf("password must be at most %d bytes; bcrypt ignores anything beyond that", MaxPasswordBytes)
	}
	return nil
}

// dummyHash is compared against when no user was found.
//
// Without it, a login for an unknown address returns in microseconds while one
// for a known address takes the full bcrypt cost, and the difference is
// measurable over the network. That turns the login endpoint into a user
// enumeration oracle, which is the first step of a credential stuffing run.
var dummyHash = mustHash("timing-equalisation-placeholder-value")

func mustHash(s string) []byte {
	h, err := bcrypt.GenerateFromPassword([]byte(s), BcryptCost)
	if err != nil {
		panic(err)
	}
	return h
}

// ComparePassword checks a password against a stored hash. A nil hash means
// the account has no password credential - an invited user who has not set one
// - and still costs a full comparison.
func ComparePassword(hash *string, plain string) error {
	stored := dummyHash
	if hash != nil {
		stored = []byte(*hash)
	}
	if err := bcrypt.CompareHashAndPassword(stored, []byte(plain)); err != nil {
		return ErrPasswordMismatch
	}
	if hash == nil {
		// The comparison succeeded against the placeholder, which can only
		// happen if someone guessed it. Refuse regardless.
		return ErrPasswordMismatch
	}
	return nil
}

// RefreshTokenBytes is the entropy in an opaque refresh token. 32 bytes is
// beyond brute force and matches the sha256 digest it is stored as.
const RefreshTokenBytes = 32

// NewRefreshToken returns the token to hand the client and the digest to
// store.
//
// Only the digest is persisted. Storing the token itself would make a database
// read equivalent to a stolen session for every live user, which turns a
// read-only SQL injection into a full account takeover.
func NewRefreshToken() (token string, digest []byte, err error) {
	raw := make([]byte, RefreshTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate refresh token: %w", err)
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), sum[:], nil
}

// HashRefreshToken digests a presented token for lookup.
func HashRefreshToken(token string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("%w: refresh token is malformed", ErrTokenInvalid)
	}
	if len(raw) != RefreshTokenBytes {
		return nil, fmt.Errorf("%w: refresh token has the wrong length", ErrTokenInvalid)
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

// ConstantTimeCompare is used where a secret is compared outside bcrypt - the
// billing relay's HMAC, for one. bytes.Equal short-circuits on the first
// differing byte, which leaks how much of a forged signature was correct.
func ConstantTimeCompare(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
