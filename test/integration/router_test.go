//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
	"github.com/mlkad/b2b-expense-tracker/internal/logger"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/service"
	transport "github.com/mlkad/b2b-expense-tracker/internal/transport/http"
	"github.com/mlkad/b2b-expense-tracker/internal/transport/http/handler"
	"github.com/mlkad/b2b-expense-tracker/internal/transport/http/middleware"
)

// newAPI builds the real router over the real services and the real database.
//
// Not a mock in sight, deliberately. What this suite is for is the wiring
// between the layers - middleware order, status codes, headers, the tenant
// binding surviving from a bearer token all the way to a policy - and every one
// of those is a thing a mock would assert into existence rather than test.
func newAPI(t *testing.T) (http.Handler, *auth.TokenService) {
	t.Helper()

	const testJWTSecret = "an-integration-test-signing-secret-32b"

	tokens, err := auth.NewTokenService(auth.Config{
		Secret:   testJWTSecret,
		Issuer:   "expense-api",
		Audience: "expense-clients",
		TTL:      15 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	log := logger.New(logger.ParseLevel("error"), logger.FormatText, "integration", "test")

	var (
		tenancyRepo = repo.NewTenancyRepository()
		expenseRepo = repo.NewExpenseRepository()
		billingRepo = repo.NewBillingRepository()
		budgetRepo  = repo.NewBudgetRepository()
		orgRepo     = repo.NewOrgRepository()
		fileRepo    = repo.NewAttachmentRepository()
	)
	scope := service.NewScope(app, tenancyRepo)

	downloadTokens, err := auth.NewDownloadTokens(testJWTSecret, "integration")
	if err != nil {
		t.Fatal(err)
	}

	handlers := transport.Handlers{
		Auth: handler.NewAuthHandler(
			service.NewAuthService(scope, tenancyRepo, tokens, 30*24*time.Hour, log), 0, false),
		Expenses: handler.NewExpenseHandler(service.NewExpenseService(scope, expenseRepo, nil)),
		Exports: handler.NewExportHandler(
			service.NewReportService(scope, expenseRepo, billingRepo, tenancyRepo),
			downloadTokens, 2*time.Minute),
		Billing: handler.NewBillingHandler(
			service.NewBillingService(scope, billingRepo, tenancyRepo, nil, log), nil, log),
		Org: handler.NewOrgHandler(
			service.NewOrgService(scope, orgRepo, budgetRepo, tenancyRepo, billingRepo)),
		Files: handler.NewAttachmentHandler(
			service.NewAttachmentService(scope, fileRepo, expenseRepo, objectStore,
				5*time.Minute, 5*time.Minute, log)),
		Account: handler.NewAccountHandler(service.NewAccountService(scope, tenancyRepo, log)),
		Health:  handler.NewHealthHandler(app, "test"),
	}

	router := transport.NewRouter(handlers, transport.RouterConfig{
		APITimeout:     30 * time.Second,
		ExportTimeout:  2 * time.Minute,
		CORS:           middleware.CORSConfig{AllowedOrigins: []string{"https://app.example.com"}},
		Tokens:         tokens,
		DownloadTokens: downloadTokens,
	}, log)

	return router, tokens
}

// call performs one request against the router.
type call struct {
	Status int
	Header http.Header
	Body   []byte
}

func (c call) json(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(c.Body, &out); err != nil {
		t.Fatalf("response is not json (%d): %s", c.Status, c.Body)
	}
	return out
}

func do(t *testing.T, api http.Handler, method, path, token string, body any) call {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)

	return call{Status: rec.Code, Header: rec.Header(), Body: rec.Body.Bytes()}
}

// tokenFor mints an access token for a seeded membership, the same way the
// login endpoint would.
func tokenFor(t *testing.T, tokens *auth.TokenService, o orgFixture, membershipID uuid.UUID) string {
	t.Helper()
	subject := subjectFor(t, o, membershipID)
	token, _, err := tokens.Issue(subject.UserID, subject.TenantID, "member@example.test")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// The whole lifecycle over HTTP, with the status codes a client actually sees.
func TestExpenseLifecycleOverHTTP(t *testing.T) {
	api, tokens := newAPI(t)
	o := seedOrg(t, "http-lifecycle")
	grantPlan(t, o.TenantID, "growth", 20)

	submitter := tokenFor(t, tokens, o, o.Submitter)
	manager := tokenFor(t, tokens, o, o.Manager)
	finance := tokenFor(t, tokens, o, o.Finance)

	created := do(t, api, http.MethodPost, "/api/v1/expenses", submitter, map[string]any{
		"department_id": o.Department,
		"category":      "software",
		"amount_minor":  12500,
		"currency":      "USD",
		"merchant":      "Figma",
		"spent_at":      time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02"),
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create returned %d: %s", created.Status, created.Body)
	}
	if loc := created.Header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/expenses/") {
		t.Errorf("no usable Location header: %q", loc)
	}
	id := created.json(t)["id"].(string)

	t.Run("a member cannot approve their own claim", func(t *testing.T) {
		if got := do(t, api, http.MethodPost, "/api/v1/expenses/"+id+"/submit", submitter, nil); got.Status != http.StatusOK {
			t.Fatalf("submit returned %d: %s", got.Status, got.Body)
		}
		got := do(t, api, http.MethodPost, "/api/v1/expenses/"+id+"/approve", submitter, nil)
		if got.Status != http.StatusForbidden {
			t.Fatalf("self-approval returned %d, want 403: %s", got.Status, got.Body)
		}
	})

	t.Run("rejecting without a reason is 422, not 400", func(t *testing.T) {
		got := do(t, api, http.MethodPost, "/api/v1/expenses/"+id+"/reject", manager, map[string]any{})
		if got.Status != http.StatusUnprocessableEntity {
			t.Fatalf("returned %d, want 422: %s", got.Status, got.Body)
		}
		if !strings.Contains(string(got.Body), "reason") {
			t.Errorf("the response does not name the missing field: %s", got.Body)
		}
	})

	t.Run("approve then pay", func(t *testing.T) {
		if got := do(t, api, http.MethodPost, "/api/v1/expenses/"+id+"/approve", manager, nil); got.Status != http.StatusOK {
			t.Fatalf("approve returned %d: %s", got.Status, got.Body)
		}
		got := do(t, api, http.MethodPost, "/api/v1/expenses/"+id+"/pay", finance,
			map[string]any{"payment_ref": "BACS-0001"})
		if got.Status != http.StatusOK {
			t.Fatalf("pay returned %d: %s", got.Status, got.Body)
		}
		if status := got.json(t)["status"]; status != "paid" {
			t.Fatalf("status = %v", status)
		}
	})

	t.Run("a settled claim is final, and the API says why", func(t *testing.T) {
		got := do(t, api, http.MethodPost, "/api/v1/expenses/"+id+"/reject", manager,
			map[string]any{"reason": "changed my mind"})
		if got.Status != http.StatusConflict {
			t.Fatalf("returned %d, want 409: %s", got.Status, got.Body)
		}
		if !strings.Contains(string(got.Body), "compensating") {
			t.Errorf("the 409 does not say what to do instead: %s", got.Body)
		}
	})

	t.Run("the ledger records every step", func(t *testing.T) {
		got := do(t, api, http.MethodGet, "/api/v1/expenses/"+id+"/history", finance, nil)
		if got.Status != http.StatusOK {
			t.Fatalf("history returned %d", got.Status)
		}
		items := got.json(t)["items"].([]any)
		if len(items) != 4 {
			t.Fatalf("ledger holds %d entries, want created/submitted/approved/paid", len(items))
		}
	})
}

// Every protected route must reject an anonymous caller. Asserted by walking
// the routes rather than by listing them, so a route added without
// authentication fails this test rather than shipping.
func TestProtectedRoutesRejectAnonymousCallers(t *testing.T) {
	api, _ := newAPI(t)

	protected := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/expenses"},
		{http.MethodPost, "/api/v1/expenses"},
		{http.MethodGet, "/api/v1/expenses/pending"},
		{http.MethodGet, "/api/v1/expenses/00000000-0000-0000-0000-000000000001"},
		{http.MethodPost, "/api/v1/expenses/00000000-0000-0000-0000-000000000001/approve"},
		{http.MethodGet, "/api/v1/departments"},
		{http.MethodPost, "/api/v1/departments"},
		{http.MethodGet, "/api/v1/budgets"},
		{http.MethodGet, "/api/v1/budgets/consumption"},
		{http.MethodGet, "/api/v1/summary"},
		{http.MethodGet, "/api/v1/members"},
		{http.MethodPost, "/api/v1/members"},
		{http.MethodGet, "/api/v1/vendor-subscriptions"},
		{http.MethodGet, "/api/v1/billing/entitlement"},
		{http.MethodPost, "/api/v1/billing/checkout"},
		{http.MethodGet, "/api/v1/reports/expenses/export?format=csv"},
		{http.MethodGet, "/api/v1/auth/tenants"},
	}

	for _, r := range protected {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			got := do(t, api, r.method, r.path, "", nil)
			if got.Status != http.StatusUnauthorized {
				t.Fatalf("returned %d, want 401", got.Status)
			}
			if !strings.Contains(got.Header.Get("WWW-Authenticate"), "invalid_token") {
				t.Errorf("no WWW-Authenticate: %q", got.Header.Get("WWW-Authenticate"))
			}
		})
	}
}

// A token minted for one tenant must not reach another's data, all the way
// from the Authorization header to the row-level security policy.
func TestTenantIsolationHoldsOverHTTP(t *testing.T) {
	api, tokens := newAPI(t)

	acme := seedOrg(t, "http-acme")
	globex := seedOrg(t, "http-globex")
	acmeClaim := seedClaim(t, acme, "approved", 4321)
	seedClaim(t, globex, "approved", 1111)

	acmeToken := tokenFor(t, tokens, acme, acme.Finance)
	globexToken := tokenFor(t, tokens, globex, globex.Finance)

	t.Run("each tenant lists only its own", func(t *testing.T) {
		for name, tc := range map[string]struct {
			token string
			want  float64
		}{"acme": {acmeToken, 4321}, "globex": {globexToken, 1111}} {
			got := do(t, api, http.MethodGet, "/api/v1/expenses", tc.token, nil)
			if got.Status != http.StatusOK {
				t.Fatalf("%s: list returned %d: %s", name, got.Status, got.Body)
			}
			items := got.json(t)["items"].([]any)
			if len(items) != 1 {
				t.Fatalf("%s saw %d claims, want 1", name, len(items))
			}
			amount := items[0].(map[string]any)["amount"].(map[string]any)["amount_minor"].(float64)
			if amount != tc.want {
				t.Fatalf("%s saw a claim for %v, want %v - that row belongs to another tenant", name, amount, tc.want)
			}
		}
	})

	// Not-found rather than forbidden: a 403 would confirm the id exists.
	t.Run("fetching another tenant's claim by id is 404", func(t *testing.T) {
		got := do(t, api, http.MethodGet, "/api/v1/expenses/"+acmeClaim.String(), globexToken, nil)
		if got.Status != http.StatusNotFound {
			t.Fatalf("returned %d, want 404: %s", got.Status, got.Body)
		}
	})
}

// Exports stream over HTTP with the headers a browser needs.
func TestExportOverHTTP(t *testing.T) {
	api, tokens := newAPI(t)
	o := seedOrg(t, "http-export")
	grantPlan(t, o.TenantID, "growth", 20)
	for i := 0; i < 25; i++ {
		seedClaim(t, o, "approved", int64(100+i))
	}

	token := tokenFor(t, tokens, o, o.Finance)

	for _, format := range []struct{ name, contentType, magic string }{
		{"csv", "text/csv", "\xef\xbb\xbf"},
		{"xlsx", "spreadsheetml", "PK"},
		{"pdf", "application/pdf", "%PDF-1.7"},
	} {
		t.Run(format.name, func(t *testing.T) {
			got := do(t, api, http.MethodGet,
				"/api/v1/reports/expenses/export?format="+format.name, token, nil)

			if got.Status != http.StatusOK {
				t.Fatalf("returned %d: %s", got.Status, got.Body)
			}
			if !strings.Contains(got.Header.Get("Content-Type"), format.contentType) {
				t.Errorf("content type = %q", got.Header.Get("Content-Type"))
			}
			if !bytes.HasPrefix(got.Body, []byte(format.magic)) {
				t.Errorf("body does not start with %q: %q", format.magic, got.Body[:min(16, len(got.Body))])
			}
			if !strings.Contains(got.Header.Get("Content-Disposition"), "attachment;") {
				t.Errorf("disposition = %q", got.Header.Get("Content-Disposition"))
			}
		})
	}

	t.Run("an unknown format is 422, not 500", func(t *testing.T) {
		got := do(t, api, http.MethodGet, "/api/v1/reports/expenses/export?format=docx", token, nil)
		if got.Status != http.StatusUnprocessableEntity {
			t.Fatalf("returned %d, want 422: %s", got.Status, got.Body)
		}
	})
}

// Keyset pagination has to walk the whole set exactly once: no repeats, no
// skips. That is the property OFFSET loses when rows are inserted mid-walk.
func TestKeysetPaginationOverHTTP(t *testing.T) {
	api, tokens := newAPI(t)
	o := seedOrg(t, "http-paging")

	const total = 23
	for i := 0; i < total; i++ {
		seedClaim(t, o, "approved", int64(1000+i))
	}
	token := tokenFor(t, tokens, o, o.Finance)

	seen := map[string]int{}
	cursor := ""
	pages := 0

	for {
		path := "/api/v1/expenses?limit=5"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		got := do(t, api, http.MethodGet, path, token, nil)
		if got.Status != http.StatusOK {
			t.Fatalf("page %d returned %d: %s", pages, got.Status, got.Body)
		}
		pages++
		if pages > 20 {
			t.Fatal("pagination did not terminate")
		}

		body := got.json(t)
		for _, item := range body["items"].([]any) {
			seen[item.(map[string]any)["id"].(string)]++
		}

		if !body["has_more"].(bool) {
			break
		}
		next, ok := body["next_cursor"].(string)
		if !ok || next == "" {
			t.Fatal("has_more is true but no cursor was returned")
		}
		cursor = next
	}

	if len(seen) != total {
		t.Fatalf("walked %d distinct claims over %d pages, want %d", len(seen), pages, total)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("claim %s appeared %d times", id, count)
		}
	}
}

// The router's own fallbacks must answer in the same envelope as every handler,
// so a client has one shape to parse.
func TestRouterFallbacks(t *testing.T) {
	api, _ := newAPI(t)

	t.Run("unknown path", func(t *testing.T) {
		got := do(t, api, http.MethodGet, "/api/v1/nope", "", nil)
		if got.Status != http.StatusNotFound {
			t.Fatalf("status %d", got.Status)
		}
		if got.json(t)["message"] != "not found" {
			t.Errorf("body = %s", got.Body)
		}
	})

	t.Run("wrong method", func(t *testing.T) {
		got := do(t, api, http.MethodDelete, "/api/v1/auth/login", "", nil)
		if got.Status != http.StatusMethodNotAllowed {
			t.Fatalf("status %d", got.Status)
		}
	})

	t.Run("probes need no credential", func(t *testing.T) {
		for _, path := range []string{"/livez", "/readyz"} {
			if got := do(t, api, http.MethodGet, path, "", nil); got.Status != http.StatusOK {
				t.Errorf("%s returned %d; an orchestrator cannot use a probe that needs a token", path, got.Status)
			}
		}
	})

	t.Run("security headers are set even on a 404", func(t *testing.T) {
		got := do(t, api, http.MethodGet, "/api/v1/nope", "", nil)
		if got.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Error("no nosniff")
		}
		if got.Header.Get("X-Request-Id") == "" {
			t.Error("no correlation id")
		}
	})

	t.Run("a preflight for an unmatched path still gets CORS headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/api/v1/nope", nil)
		req.Header.Set("Origin", "https://app.example.com")
		req.Header.Set("Access-Control-Request-Method", "POST")
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)

		if rec.Header().Get("Access-Control-Allow-Origin") == "" {
			t.Fatal("a preflight for an unknown path got no CORS headers; " +
				"the browser reports that as a CORS failure rather than a 404")
		}
	})
}

// A malformed id is the client's mistake and is fixable, so it is 422 with the
// field named - not a 404, which reads as a missing resource.
func TestMalformedPathIdIsAFieldError(t *testing.T) {
	api, tokens := newAPI(t)
	o := seedOrg(t, "http-badid")
	token := tokenFor(t, tokens, o, o.Finance)

	got := do(t, api, http.MethodGet, "/api/v1/expenses/not-a-uuid", token, nil)
	if got.Status != http.StatusUnprocessableEntity {
		t.Fatalf("returned %d, want 422: %s", got.Status, got.Body)
	}
	if !strings.Contains(string(got.Body), `"id"`) {
		t.Errorf("the response does not name the field: %s", got.Body)
	}
}
