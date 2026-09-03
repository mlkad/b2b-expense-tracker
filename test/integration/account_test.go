//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// The first call a dashboard makes.
func TestMeDescribesTheCaller(t *testing.T) {
	api, tokens := newAPI(t)
	o := seedOrg(t, "account-me")

	got := do(t, api, http.MethodGet, "/api/v1/me", tokenFor(t, tokens, o, o.Manager), nil)
	if got.Status != http.StatusOK {
		t.Fatalf("returned %d: %s", got.Status, got.Body)
	}
	body := got.json(t)

	for field, want := range map[string]any{
		"role":        "manager",
		"status":      "active",
		"tenant_slug": "account-me",
	} {
		if body[field] != want {
			t.Errorf("%s = %v, want %v", field, body[field], want)
		}
	}

	// The manager is seeded scoped to Engineering, and the ceiling is the role
	// default because the membership sets no override.
	if body["department_id"] == nil {
		t.Error("a department-scoped manager reports no department")
	}
	if limit, ok := body["approval_limit_minor"].(float64); !ok || limit <= 0 {
		t.Errorf("approval_limit_minor = %v, want the finite manager default", body["approval_limit_minor"])
	}

	perms, ok := body["permissions"].([]any)
	if !ok || len(perms) == 0 {
		t.Fatalf("no permissions returned: %v", body["permissions"])
	}
	var canApprove, canPay bool
	for _, p := range perms {
		switch p {
		case "expense:approve":
			canApprove = true
		case "expense:pay":
			canPay = true
		}
	}
	if !canApprove {
		t.Error("a manager is not told they can approve")
	}
	if canPay {
		t.Error("a manager is told they can settle payments; that is the separation of duties")
	}
}

func TestOrganisationSettings(t *testing.T) {
	api, tokens := newAPI(t)
	o := seedOrg(t, "account-org")

	owner := tokenFor(t, tokens, o, o.Manager)
	promote(t, o, o.Manager, "owner")
	member := tokenFor(t, tokens, o, o.Submitter)

	t.Run("a member cannot rename the organisation", func(t *testing.T) {
		got := do(t, api, http.MethodPatch, "/api/v1/tenant", member, map[string]any{"name": "Hijacked"})
		if got.Status != http.StatusForbidden {
			t.Fatalf("returned %d, want 403: %s", got.Status, got.Body)
		}
	})

	t.Run("an owner can", func(t *testing.T) {
		got := do(t, api, http.MethodPatch, "/api/v1/tenant", owner, map[string]any{"name": "Renamed Ltd"})
		if got.Status != http.StatusOK {
			t.Fatalf("returned %d: %s", got.Status, got.Body)
		}
		if got.json(t)["name"] != "Renamed Ltd" {
			t.Errorf("name = %v", got.json(t)["name"])
		}
	})

	// The slug appears in bookmarked links and in the sign-in form. It is not
	// a settings field, and a request naming it must not silently succeed
	// while ignoring it.
	t.Run("the slug is not changeable", func(t *testing.T) {
		got := do(t, api, http.MethodPatch, "/api/v1/tenant", owner,
			map[string]any{"name": "Renamed Ltd", "slug": "something-else"})
		if got.Status != http.StatusUnprocessableEntity {
			t.Fatalf("returned %d, want 422 for an unknown field: %s", got.Status, got.Body)
		}

		after := do(t, api, http.MethodGet, "/api/v1/tenant", owner, nil)
		if after.json(t)["slug"] != "account-org" {
			t.Fatalf("the slug changed to %v", after.json(t)["slug"])
		}
	})

	t.Run("an invalid name is refused", func(t *testing.T) {
		got := do(t, api, http.MethodPatch, "/api/v1/tenant", owner, map[string]any{"name": "   "})
		if got.Status != http.StatusUnprocessableEntity {
			t.Fatalf("returned %d, want 422: %s", got.Status, got.Body)
		}
	})
}

// Changing the currency after claims exist would leave totals summing mixed
// currencies - a number that looks authoritative and means nothing.
func TestCurrencyCannotChangeOnceClaimsExist(t *testing.T) {
	api, tokens := newAPI(t)
	o := seedOrg(t, "account-currency")
	owner := tokenFor(t, tokens, o, o.Manager)
	promote(t, o, o.Manager, "owner")

	t.Run("allowed while the organisation is empty", func(t *testing.T) {
		got := do(t, api, http.MethodPatch, "/api/v1/tenant", owner,
			map[string]any{"name": "Currency Ltd", "default_currency": "EUR"})
		if got.Status != http.StatusOK {
			t.Fatalf("returned %d: %s", got.Status, got.Body)
		}
		if got.json(t)["default_currency"] != "EUR" {
			t.Errorf("currency = %v", got.json(t)["default_currency"])
		}
	})

	seedClaim(t, o, "draft", 1000)

	t.Run("refused once a claim exists", func(t *testing.T) {
		got := do(t, api, http.MethodPatch, "/api/v1/tenant", owner,
			map[string]any{"name": "Currency Ltd", "default_currency": "GBP"})
		if got.Status != http.StatusUnprocessableEntity {
			t.Fatalf("returned %d, want 422: %s", got.Status, got.Body)
		}
		if !strings.Contains(string(got.Body), "mixed currencies") {
			t.Errorf("the refusal does not explain why: %s", got.Body)
		}
	})

	t.Run("renaming still works", func(t *testing.T) {
		got := do(t, api, http.MethodPatch, "/api/v1/tenant", owner, map[string]any{"name": "Still Renameable"})
		if got.Status != http.StatusOK {
			t.Fatalf("returned %d: %s", got.Status, got.Body)
		}
	})
}

func TestChangePassword(t *testing.T) {
	api, _ := newAPI(t)

	const (
		email    = "pwchange@example.test"
		original = "correct-horse-battery"
		next     = "a-different-long-passphrase"
	)

	registered := do(t, api, http.MethodPost, "/api/v1/auth/register", "", map[string]any{
		"email": email, "password": original, "full_name": "Ada",
		"organisation_name": "Password Ltd", "organisation_slug": "password-ltd",
		"currency": "USD",
	})
	if registered.Status != http.StatusCreated {
		t.Fatalf("register returned %d: %s", registered.Status, registered.Body)
	}
	token := registered.json(t)["access_token"].(string)

	// Cleanup: the fixture helper only knows about seeded organisations.
	t.Cleanup(func() {
		db := ownerDB(t)
		db.Exec(`DELETE FROM tenants WHERE slug = 'password-ltd'`)
		db.Exec(`DELETE FROM users WHERE email = $1`, email)
	})

	// Without the current password, a stolen access token - fifteen minutes of
	// life - becomes a permanent takeover in one request.
	t.Run("the current password is required", func(t *testing.T) {
		got := do(t, api, http.MethodPost, "/api/v1/auth/password", token,
			map[string]any{"current_password": "not-the-password", "new_password": next})
		if got.Status != http.StatusUnauthorized {
			t.Fatalf("returned %d, want 401: %s", got.Status, got.Body)
		}
		// The same answer as a failed login. A distinctive one would let the
		// holder of a stolen token confirm guesses at the real password.
		if !strings.Contains(string(got.Body), "email or password is incorrect") {
			t.Errorf("the response is distinguishable from a failed login: %s", got.Body)
		}
	})

	t.Run("a weak or unchanged password is refused", func(t *testing.T) {
		for name, candidate := range map[string]string{
			"too short": "short",
			"unchanged": original,
		} {
			got := do(t, api, http.MethodPost, "/api/v1/auth/password", token,
				map[string]any{"current_password": original, "new_password": candidate})
			if got.Status != http.StatusUnprocessableEntity {
				t.Errorf("%s: returned %d, want 422: %s", name, got.Status, got.Body)
			}
		}
	})

	t.Run("changing it works and ends every session", func(t *testing.T) {
		got := do(t, api, http.MethodPost, "/api/v1/auth/password", token,
			map[string]any{"current_password": original, "new_password": next})
		if got.Status != http.StatusNoContent {
			t.Fatalf("returned %d: %s", got.Status, got.Body)
		}
		// The cookie is cleared, or the client keeps sending one that no
		// longer works and the next request fails in a way that looks like a bug.
		if cookie := got.Header.Get("Set-Cookie"); !strings.Contains(cookie, "Max-Age=0") {
			t.Errorf("the refresh cookie was not cleared: %q", cookie)
		}

		// Every refresh token is revoked, including the one from registration.
		var live int
		ownerDB(t).QueryRow(`
			SELECT count(*) FROM refresh_tokens rt
			  JOIN users u ON u.id = rt.user_id
			 WHERE u.email = $1 AND rt.revoked_at IS NULL`, email).Scan(&live)
		if live != 0 {
			t.Fatalf("%d sessions survived the password change; somebody who changed their "+
				"password because it was compromised has done nothing about the attacker", live)
		}
	})

	t.Run("the old password no longer works and the new one does", func(t *testing.T) {
		old := do(t, api, http.MethodPost, "/api/v1/auth/login", "",
			map[string]any{"email": email, "password": original, "organisation_slug": "password-ltd"})
		if old.Status != http.StatusUnauthorized {
			t.Fatalf("the old password still signs in: %d", old.Status)
		}

		fresh := do(t, api, http.MethodPost, "/api/v1/auth/login", "",
			map[string]any{"email": email, "password": next, "organisation_slug": "password-ltd"})
		if fresh.Status != http.StatusOK {
			t.Fatalf("the new password does not sign in: %d %s", fresh.Status, fresh.Body)
		}
	})
}
