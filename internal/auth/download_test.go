package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func downloadTokens(t *testing.T) *DownloadTokens {
	t.Helper()
	d, err := NewDownloadTokens(testSecret, "expense-api")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func downloadSubject() Subject {
	return Subject{UserID: uuid.New(), TenantID: uuid.New()}
}

func TestNewDownloadTokensRejectsWeakConfiguration(t *testing.T) {
	if _, err := NewDownloadTokens("short", "expense-api"); err == nil {
		t.Error("accepted a short secret")
	}
	if _, err := NewDownloadTokens(testSecret, ""); err == nil {
		t.Error("accepted an empty issuer")
	}
}

func TestDownloadTokenRoundTrip(t *testing.T) {
	d := downloadTokens(t)
	subject := downloadSubject()
	const query = "format=csv&from=2026-01-01"

	token, expiresAt, err := d.Issue(subject, query)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// A query string is written down by every access log it passes through, so
	// the lifetime is the window in which a leaked URL still works.
	if until := time.Until(expiresAt); until > 2*time.Minute {
		t.Errorf("a download token lives for %s; it should expire in about a minute", until)
	}

	got, err := d.Parse(token, query)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.UserID != subject.UserID || got.TenantID != subject.TenantID {
		t.Fatalf("round trip lost identity: %+v", got)
	}
}

// The assertion the whole design turns on. The export reads its filters from
// the URL, so a token that did not bind them would let the holder widen the
// report to the whole organisation by editing the address bar.
func TestDownloadTokenIsBoundToItsQuery(t *testing.T) {
	d := downloadTokens(t)
	subject := downloadSubject()

	token, _, err := d.Issue(subject, "format=csv&department_id=mine")
	if err != nil {
		t.Fatal(err)
	}

	widened := []string{
		"format=csv", // department filter removed
		"format=csv&department_id=somebody-elses", // pointed elsewhere
		"format=xlsx&department_id=mine",          // a different format
		"format=csv&department_id=mine&status=paid",
		"",
	}

	for _, query := range widened {
		if _, err := d.Parse(token, query); !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("a token issued for one report was accepted for %q", query)
		}
	}

	if _, err := d.Parse(token, "format=csv&department_id=mine"); err != nil {
		t.Fatalf("the query it was issued for was refused: %v", err)
	}
}

func TestDownloadTokenExpires(t *testing.T) {
	d := downloadTokens(t)

	// Minted two minutes ago, so it is past the one-minute lifetime by the
	// time it is parsed - without the test having to wait for it.
	d.now = func() time.Time { return time.Now().Add(-2 * time.Minute) }
	stale, _, err := d.Issue(downloadSubject(), "format=csv")
	if err != nil {
		t.Fatal(err)
	}

	d.now = time.Now
	if _, err := d.Parse(stale, "format=csv"); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("got %v, want ErrTokenExpired", err)
	}

	fresh, _, err := d.Issue(downloadSubject(), "format=csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Parse(fresh, "format=csv"); err != nil {
		t.Fatalf("a fresh token was refused: %v", err)
	}
}

// The two token types must not be interchangeable. An access token in a URL
// would be a full-API credential in browser history and every access log it
// passes; a download token accepted as a bearer would bypass every other check.
func TestAccessAndDownloadTokensAreNotInterchangeable(t *testing.T) {
	access := newService(t)
	downloads := downloadTokens(t)
	subject := downloadSubject()

	accessToken, _, err := access.Issue(subject.UserID, subject.TenantID, "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	downloadToken, _, err := downloads.Issue(subject, "format=csv")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("an access token is not a download token", func(t *testing.T) {
		if _, err := downloads.Parse(accessToken, "format=csv"); !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("got %v, want ErrTokenInvalid", err)
		}
	})

	t.Run("a download token is not a bearer", func(t *testing.T) {
		if _, err := access.Parse(downloadToken); !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("got %v, want ErrTokenInvalid", err)
		}
	})
}

func TestDownloadTokenRejectsTampering(t *testing.T) {
	d := downloadTokens(t)
	token, _, err := d.Issue(downloadSubject(), "format=csv")
	if err != nil {
		t.Fatal(err)
	}

	other, err := NewDownloadTokens(strings.Repeat("z", 40), "expense-api")
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := other.Issue(downloadSubject(), "format=csv")
	if err != nil {
		t.Fatal(err)
	}

	for name, candidate := range map[string]string{
		"empty":                   "",
		"not a token":             "nonsense",
		"tampered signature":      token[:len(token)-6] + "AAAAAA",
		"signed with another key": foreign,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := d.Parse(candidate, "format=csv"); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("got %v, want ErrTokenInvalid", err)
			}
		})
	}
}

func TestIssueRefusesAnIncompleteSubject(t *testing.T) {
	d := downloadTokens(t)

	if _, _, err := d.Issue(Subject{TenantID: uuid.New()}, "format=csv"); err == nil {
		t.Error("minted a token with no user")
	}
	if _, _, err := d.Issue(Subject{UserID: uuid.New()}, "format=csv"); err == nil {
		t.Error("minted a token with no tenant")
	}
}
