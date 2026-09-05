//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// A browser navigating to a download sends no Authorization header. Before the
// signed link existed, the export buttons in the dashboard produced nothing but
// a 401 page - so this is the regression that matters.
func TestExportLinkWorksWithoutABearerToken(t *testing.T) {
	api, tokens := newAPI(t)
	o := seedOrg(t, "export-link")
	seedClaim(t, o, "approved", 12_500)

	token := tokenFor(t, tokens, o, o.Manager)
	promote(t, o, o.Manager, "finance")

	t.Run("the export refuses an unauthenticated navigation", func(t *testing.T) {
		got := do(t, api, http.MethodGet, "/api/v1/reports/expenses/export?format=csv", "", nil)
		if got.Status != http.StatusUnauthorized {
			t.Fatalf("returned %d, want 401", got.Status)
		}
	})

	ticket := do(t, api, http.MethodGet, "/api/v1/reports/expenses/export/token?format=csv", token, nil)
	if ticket.Status != http.StatusOK {
		t.Fatalf("minting a link returned %d: %s", ticket.Status, ticket.Body)
	}
	signed, _ := ticket.json(t)["url"].(string)
	if signed == "" {
		t.Fatal("no url in the ticket")
	}

	t.Run("the signed link downloads the report", func(t *testing.T) {
		// No bearer token: exactly what a browser navigation sends.
		got := do(t, api, http.MethodGet, signed, "", nil)
		if got.Status != http.StatusOK {
			t.Fatalf("returned %d: %s", got.Status, got.Body)
		}
		if ct := got.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
			t.Errorf("content type = %q", ct)
		}
		if cd := got.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
			t.Errorf("content disposition = %q", cd)
		}
		if !strings.Contains(string(got.Body), "Merchant") {
			t.Errorf("the body does not look like the report:\n%s", got.Body[:min(200, len(got.Body))])
		}
	})

	// The export reads its filters from the URL, so a token that did not bind
	// them would let the holder widen the report by editing the address bar.
	t.Run("the link cannot be edited into a different report", func(t *testing.T) {
		for _, tampered := range []string{
			signed + "&status=paid",
			strings.Replace(signed, "format=csv", "format=xlsx", 1),
			strings.Replace(signed, "format=csv", "format=csv&from=2020-01-01", 1),
		} {
			got := do(t, api, http.MethodGet, tampered, "", nil)
			if got.Status != http.StatusUnauthorized {
				t.Errorf("a widened link returned %d, want 401: %s", got.Status, tampered)
			}
		}
	})

	// An access token in a URL would be a full-API credential in browser
	// history and in every access log the request passes through.
	t.Run("an access token is not accepted as a download token", func(t *testing.T) {
		got := do(t, api, http.MethodGet,
			"/api/v1/reports/expenses/export?format=csv&token="+token, "", nil)
		if got.Status != http.StatusUnauthorized {
			t.Fatalf("returned %d, want 401", got.Status)
		}
	})

	// A bearer still works, because that is what an API client would use.
	t.Run("a bearer token still works", func(t *testing.T) {
		got := do(t, api, http.MethodGet, "/api/v1/reports/expenses/export?format=csv", token, nil)
		if got.Status != http.StatusOK {
			t.Fatalf("returned %d: %s", got.Status, got.Body)
		}
	})
}

// Minting a link needs the export permission, so a member cannot obtain one for
// a report they could not run.
func TestMintingALinkNeedsTheExportPermission(t *testing.T) {
	api, tokens := newAPI(t)
	o := seedOrg(t, "export-link-perm")

	member := tokenFor(t, tokens, o, o.Submitter)
	got := do(t, api, http.MethodGet, "/api/v1/reports/expenses/export/token?format=csv", member, nil)

	// The mint endpoint validates the format and the filters, then signs; the
	// export itself is where the role is checked, so the refusal arrives when
	// the link is used rather than when it is issued.
	if got.Status == http.StatusOK {
		signed, _ := got.json(t)["url"].(string)
		used := do(t, api, http.MethodGet, signed, "", nil)
		if used.Status != http.StatusForbidden {
			t.Fatalf("a member downloaded a report: %d %s", used.Status, used.Body)
		}
		return
	}
	if got.Status != http.StatusForbidden {
		t.Fatalf("returned %d, want 403 either at mint or at use", got.Status)
	}
}
