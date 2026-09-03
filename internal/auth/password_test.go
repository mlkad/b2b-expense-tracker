package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidatePassword(t *testing.T) {
	t.Run("too short is refused", func(t *testing.T) {
		if err := ValidatePassword(strings.Repeat("a", MinPasswordLength-1)); err == nil {
			t.Fatal("accepted a password below the minimum")
		}
	})

	// bcrypt silently ignores everything past 72 bytes, so a 200-character
	// passphrase is really its first 72 characters. Refusing is better than
	// storing a hash of a password the user did not choose.
	t.Run("beyond bcrypt's 72-byte limit is refused, not truncated", func(t *testing.T) {
		if err := ValidatePassword(strings.Repeat("a", MaxPasswordBytes+1)); err == nil {
			t.Fatal("accepted a password bcrypt would truncate")
		}
		if err := ValidatePassword(strings.Repeat("a", MaxPasswordBytes)); err != nil {
			t.Fatalf("refused a password exactly at the limit: %v", err)
		}
	})

	// The limit is bytes, not runes: a multi-byte passphrase hits it sooner
	// than its character count suggests.
	t.Run("the limit counts bytes", func(t *testing.T) {
		multibyte := strings.Repeat("é", 40) // 80 bytes, 40 runes
		if err := ValidatePassword(multibyte); err == nil {
			t.Fatal("accepted 80 bytes because it was only 40 characters")
		}
	})
}

func TestHashAndCompare(t *testing.T) {
	const password = "correct-horse-battery-staple"

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if strings.Contains(hash, password) {
		t.Fatal("the plaintext appears in the hash")
	}

	if err := ComparePassword(&hash, password); err != nil {
		t.Fatalf("the correct password was refused: %v", err)
	}
	if err := ComparePassword(&hash, "wrong-"+password); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("got %v, want ErrPasswordMismatch", err)
	}

	t.Run("the same password hashes differently each time", func(t *testing.T) {
		second, err := HashPassword(password)
		if err != nil {
			t.Fatal(err)
		}
		if second == hash {
			t.Fatal("two hashes of the same password are identical; the salt is not random")
		}
	})
}

// An invited user who has not set a password has no credential. That is not
// the same as a wrong password, and it must never succeed.
func TestNilHashNeverAuthenticates(t *testing.T) {
	for _, attempt := range []string{"", "anything", "timing-equalisation-placeholder-value"} {
		if err := ComparePassword(nil, attempt); !errors.Is(err, ErrPasswordMismatch) {
			t.Fatalf("a nil hash accepted %q", attempt)
		}
	}
}

// Returning early for an unknown address makes the login endpoint a user
// enumeration oracle: the difference between a microsecond and a full bcrypt
// round is measurable over the network, and enumeration is the first step of a
// credential stuffing run against every other service those users use.
func TestUnknownAccountCostsTheSameAsAKnownOne(t *testing.T) {
	if testing.Short() {
		t.Skip("timing measurement is slow")
	}

	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}

	measure := func(h *string) time.Duration {
		const rounds = 3
		start := time.Now()
		for i := 0; i < rounds; i++ {
			_ = ComparePassword(h, "some-wrong-password")
		}
		return time.Since(start) / rounds
	}

	known := measure(&hash)
	unknown := measure(nil)

	// Both run a full bcrypt comparison, so the two should be within the same
	// order of magnitude. A missing placeholder shows up as unknown being
	// hundreds of times faster, not as a few percent of noise.
	ratio := float64(known) / float64(unknown)
	if ratio > 4 || ratio < 0.25 {
		t.Fatalf("known account took %s, unknown took %s (ratio %.1f): "+
			"the timing difference is an enumeration oracle", known, unknown, ratio)
	}
	t.Logf("known %s, unknown %s (ratio %.2f)", known, unknown, ratio)
}

func TestRefreshTokenDigest(t *testing.T) {
	token, digest, err := NewRefreshToken()
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	if len(digest) != sha256.Size {
		t.Fatalf("digest is %d bytes, want %d - the column has a CHECK on this", len(digest), sha256.Size)
	}

	// The stored digest must be derivable from what the client presents, or a
	// refresh could never be looked up.
	rederived, err := HashRefreshToken(token)
	if err != nil {
		t.Fatalf("HashRefreshToken: %v", err)
	}
	if string(rederived) != string(digest) {
		t.Fatal("the digest of a presented token does not match the one stored at issue")
	}

	t.Run("tokens are unique", func(t *testing.T) {
		seen := make(map[string]struct{}, 500)
		for i := 0; i < 500; i++ {
			tok, _, err := NewRefreshToken()
			if err != nil {
				t.Fatal(err)
			}
			if _, dup := seen[tok]; dup {
				t.Fatal("a refresh token repeated; the generator is not random")
			}
			seen[tok] = struct{}{}
		}
	})

	t.Run("malformed tokens are refused", func(t *testing.T) {
		for name, bad := range map[string]string{
			"empty":        "",
			"not base64":   "!!!!",
			"wrong length": base64.RawURLEncoding.EncodeToString([]byte("short")),
		} {
			if _, err := HashRefreshToken(bad); !errors.Is(err, ErrTokenInvalid) {
				t.Errorf("%s: got %v, want ErrTokenInvalid", name, err)
			}
		}
	})
}

func TestConstantTimeCompare(t *testing.T) {
	a := []byte("0123456789abcdef")
	if !ConstantTimeCompare(a, []byte("0123456789abcdef")) {
		t.Error("equal values compared unequal")
	}
	if ConstantTimeCompare(a, []byte("0123456789abcdee")) {
		t.Error("values differing in the last byte compared equal")
	}
	if ConstantTimeCompare(a, []byte("x123456789abcdef")) {
		t.Error("values differing in the first byte compared equal")
	}
	if ConstantTimeCompare(a, []byte("short")) {
		t.Error("values of different lengths compared equal")
	}
}
