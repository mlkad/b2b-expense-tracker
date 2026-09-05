package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/billing"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/expense"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	exportpkg "github.com/mlkad/b2b-expense-tracker/internal/export"
	"github.com/mlkad/b2b-expense-tracker/internal/gateway"
	"github.com/mlkad/b2b-expense-tracker/internal/service"
	"github.com/mlkad/b2b-expense-tracker/internal/storage"
	"github.com/mlkad/b2b-expense-tracker/internal/transport/http/middleware"
)

// The status code is the only part of an error a client can act on
// automatically, so the mapping is asserted case by case rather than trusted.
func TestErrorClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not found", shared.ErrNotFound, http.StatusNotFound},
		{"wrapped not found", fmt.Errorf("loading claim: %w", shared.ErrNotFound), http.StatusNotFound},
		{"forbidden", shared.ErrForbidden, http.StatusForbidden},
		{"conflict", shared.ErrConflict, http.StatusConflict},

		// A lost compare-and-swap is 409, not 500: the request was fine and
		// the world moved, so the client should reload and decide again.
		{"stale write", shared.ErrStaleWrite, http.StatusConflict},

		{"validation", shared.ErrValidation, http.StatusUnprocessableEntity},
		{"export too large", service.ErrExportTooLarge, http.StatusRequestEntityTooLarge},

		// A plan ceiling is not a permission problem. A 403 would send the
		// caller to an administrator, who cannot help.
		{"plan limit", &service.ErrPlanLimit{
			Singular: "department", Plural: "departments",
			Limit: 1, Current: 1, Plan: billing.PlanFree,
		}, http.StatusPaymentRequired},

		// The deployment has no object store. The request is well formed and
		// the code is correct, so a 500 would send somebody hunting a bug.
		{"storage disabled", service.ErrStorageDisabled, http.StatusNotImplemented},
		{"storage unavailable", storage.ErrUnavailable, http.StatusServiceUnavailable},

		{"gateway unavailable", gateway.ErrUnavailable, http.StatusServiceUnavailable},
		{"gateway rejected", gateway.ErrRejected, http.StatusBadGateway},

		// Both of these are wiring bugs rather than client errors. Reporting
		// them as 403 would make a broken code path look like a permission
		// decision and it would be triaged as one.
		{"no tenant context", shared.ErrNoTenantContext, http.StatusInternalServerError},
		{"tenant mismatch", shared.ErrTenantMismatch, http.StatusInternalServerError},
		{"missing subject", middleware.ErrNoSubject, http.StatusInternalServerError},

		{"unknown", errors.New("something went wrong"), http.StatusInternalServerError},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, message := classify(c.err)
			if got != c.want {
				t.Fatalf("status = %d, want %d", got, c.want)
			}
			if message == "" {
				t.Error("no message")
			}
		})
	}
}

// A 5xx must never carry the underlying error text: it can name a table, a
// constraint, a host or a query.
func TestInternalErrorsDoNotLeakDetail(t *testing.T) {
	leaky := errors.New(`pq: relation "expenses" does not exist on host db-primary-3.internal`)

	rec := httptest.NewRecorder()
	writeError(rec, httptest.NewRequest(http.MethodGet, "/", nil), leaky)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, secret := range []string{"expenses", "db-primary-3", "pq:"} {
		if strings.Contains(body, secret) {
			t.Fatalf("the response leaked %q: %s", secret, body)
		}
	}

	// Client errors are the opposite: the message is the point.
	rec = httptest.NewRecorder()
	writeError(rec, httptest.NewRequest(http.MethodGet, "/", nil),
		fmt.Errorf("%w: role viewer does not carry expense:approve", shared.ErrForbidden))
	if !strings.Contains(rec.Body.String(), "expense:approve") {
		t.Fatalf("a 403 withheld the reason: %s", rec.Body.String())
	}
}

// Field errors are what the dashboard puts next to an input, so they must
// arrive as structured fields rather than as prose.
func TestFieldErrorsAreStructured(t *testing.T) {
	var v shared.Validator
	v.Add("amount_minor", "must be greater than zero")
	v.Add("currency", "must be a three-letter ISO 4217 code")

	rec := httptest.NewRecorder()
	writeError(rec, httptest.NewRequest(http.MethodPost, "/", nil), v.Err())

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", rec.Code)
	}

	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Fields) != 2 {
		t.Fatalf("got %d fields, want 2: %s", len(body.Fields), rec.Body.String())
	}
	if body.Fields[0].Field != "amount_minor" {
		t.Errorf("first field = %q", body.Fields[0].Field)
	}
}

// -----------------------------------------------------------------------------
// Request decoding
// -----------------------------------------------------------------------------

func TestDecodeJSON(t *testing.T) {
	type payload struct {
		Merchant string `json:"merchant"`
		Amount   int64  `json:"amount_minor"`
	}

	decode := func(body string, contentType string) (payload, error) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		var p payload
		return p, decodeJSON(httptest.NewRecorder(), req, &p)
	}

	t.Run("a well-formed body decodes", func(t *testing.T) {
		p, err := decode(`{"merchant":"Figma","amount_minor":1250}`, "application/json")
		if err != nil {
			t.Fatal(err)
		}
		if p.Merchant != "Figma" || p.Amount != 1250 {
			t.Fatalf("%+v", p)
		}
	})

	t.Run("a charset parameter is accepted", func(t *testing.T) {
		if _, err := decode(`{"merchant":"x"}`, "application/json; charset=utf-8"); err != nil {
			t.Fatalf("a perfectly ordinary content type was refused: %v", err)
		}
	})

	// A typo in a field name would otherwise be silently ignored, and the
	// caller would spend an afternoon wondering why the value did not apply.
	t.Run("unknown fields are refused", func(t *testing.T) {
		_, err := decode(`{"merchant":"Figma","aproval_limit":5}`, "application/json")
		if !errors.Is(err, shared.ErrValidation) {
			t.Fatalf("got %v, want a validation error naming the unknown field", err)
		}
	})

	t.Run("a second document is refused", func(t *testing.T) {
		// Quietly ignoring the second means the request says something
		// different to the client than to the server.
		_, err := decode(`{"merchant":"a"}{"merchant":"b"}`, "application/json")
		if !errors.Is(err, shared.ErrValidation) {
			t.Fatalf("got %v, want ErrValidation", err)
		}
	})

	t.Run("an empty body is refused", func(t *testing.T) {
		if _, err := decode("", "application/json"); !errors.Is(err, shared.ErrValidation) {
			t.Fatalf("got %v, want ErrValidation", err)
		}
	})

	t.Run("a non-JSON content type is refused", func(t *testing.T) {
		if _, err := decode(`{"merchant":"x"}`, "text/xml"); !errors.Is(err, shared.ErrValidation) {
			t.Fatalf("got %v, want ErrValidation", err)
		}
	})

	// Without a cap, a request with a multi-gigabyte body is an out-of-memory
	// condition that costs the sender nothing but bandwidth.
	t.Run("an oversized body is refused", func(t *testing.T) {
		huge := `{"merchant":"` + strings.Repeat("a", MaxRequestBytes+1024) + `"}`
		_, err := decode(huge, "application/json")
		if !errors.Is(err, shared.ErrValidation) {
			t.Fatalf("got %v, want ErrValidation", err)
		}
		if !strings.Contains(err.Error(), "bytes") {
			t.Errorf("the error does not say the body was too large: %v", err)
		}
	})
}

// -----------------------------------------------------------------------------
// Query parameters
// -----------------------------------------------------------------------------

func TestQueryReader(t *testing.T) {
	t.Run("a full filter parses", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/?status=approved&category=software&from=2026-01-01&to=2026-03-31"+
				"&min_amount_minor=100&max_amount_minor=5000&q=figma&limit=50", nil)
		q := newQueryReader(req)
		f := q.filter()
		limit := q.intDefault("limit", 25)

		if err := q.err(); err != nil {
			t.Fatalf("valid parameters rejected: %v", err)
		}
		if f.Status == nil || *f.Status != expense.StatusApproved {
			t.Errorf("status = %v", f.Status)
		}
		if f.SpentFrom == nil || f.SpentFrom.Format("2006-01-02") != "2026-01-01" {
			t.Errorf("from = %v", f.SpentFrom)
		}
		if f.MinMinor == nil || *f.MinMinor != 100 {
			t.Errorf("min = %v", f.MinMinor)
		}
		if limit != 50 {
			t.Errorf("limit = %d", limit)
		}
	})

	t.Run("an absent filter is nil, not a zero value", func(t *testing.T) {
		q := newQueryReader(httptest.NewRequest(http.MethodGet, "/", nil))
		f := q.filter()
		if err := q.err(); err != nil {
			t.Fatal(err)
		}
		// nil is what the sqlc.narg parameters read as "no constraint". A zero
		// value would filter on the zero value instead.
		if f.Status != nil || f.SpentFrom != nil || f.MinMinor != nil || f.Search != nil {
			t.Fatalf("absent parameters produced constraints: %+v", f)
		}
	})

	t.Run("every bad parameter is reported together", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/?status=invented&from=yesterday&min_amount_minor=lots&department_id=nope", nil)
		q := newQueryReader(req)
		_ = q.filter()

		err := q.err()
		if !errors.Is(err, shared.ErrValidation) {
			t.Fatalf("got %v, want ErrValidation", err)
		}
		var fields shared.FieldErrors
		if !errors.As(err, &fields) || len(fields) != 4 {
			t.Fatalf("got %v, want four field errors so a client fixes them in one pass", err)
		}
	})

	// An unbounded search term reaches a trigram index as a pattern, and a very
	// long one is expensive to match against every row.
	t.Run("an overlong search term is refused", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/?q="+strings.Repeat("a", 200), nil)
		q := newQueryReader(req)
		_ = q.filter()
		if err := q.err(); !errors.Is(err, shared.ErrValidation) {
			t.Fatalf("got %v, want ErrValidation", err)
		}
	})
}

// -----------------------------------------------------------------------------
// Download headers
// -----------------------------------------------------------------------------

// The report name derives from tenant-controlled data, and an unescaped quote
// or semicolon would let it inject header parameters.
func TestContentDispositionIsInjectionSafe(t *testing.T) {
	for _, filename := range []string{
		`normal.csv`,
		`with "quotes".csv`,
		`semi;colon.csv`,
		`back\slash.csv`,
		"newline\r\nX-Admin: true.csv",
		"expenses-Ünïcödé.xlsx",
	} {
		t.Run(filename, func(t *testing.T) {
			got := contentDisposition(filename)

			if strings.ContainsAny(got, "\r\n") {
				t.Fatalf("a header value contains a line break: %q", got)
			}
			// Exactly two quotes: the pair around the ASCII filename, and none
			// smuggled in from the input to close the parameter early.
			if n := strings.Count(got, `"`); n != 2 {
				t.Fatalf("got %d quotes, want 2: %q", n, got)
			}
			// One semicolon separating each parameter, and none from the
			// filename adding one of its own.
			if n := strings.Count(got, ";"); n != 2 {
				t.Fatalf("got %d semicolons, want 2: %q", n, got)
			}
			if !strings.Contains(got, "filename*=UTF-8''") {
				t.Errorf("no RFC 5987 form, so a non-ASCII name would be mangled: %q", got)
			}
		})
	}
}

var fixedGeneratedAt = time.Date(2026, 3, 31, 17, 0, 0, 0, time.UTC)

func exportReportFixture() exportpkg.Report {
	return exportpkg.Report{
		Title:      "Expense claims",
		TenantName: "Acme",
		Generated:  fixedGeneratedAt,
	}
}

func TestExportHeadersTellCachesAndProxiesWhatToDo(t *testing.T) {
	rec := httptest.NewRecorder()
	setDownloadHeaders(rec, "xlsx", exportReportFixture())

	h := rec.Header()
	if !strings.Contains(h.Get("Content-Type"), "spreadsheetml") {
		t.Errorf("content type = %q", h.Get("Content-Type"))
	}
	if !strings.HasPrefix(h.Get("Content-Disposition"), "attachment;") {
		t.Errorf("disposition = %q", h.Get("Content-Disposition"))
	}
	// A report is a point-in-time snapshot of private financial data.
	if !strings.Contains(h.Get("Cache-Control"), "no-store") {
		t.Errorf("cache-control = %q; a shared cache must not keep this", h.Get("Cache-Control"))
	}
	// Without this, a buffering proxy would read the whole response to compute
	// a length, reintroducing exactly the memory cost the streaming design
	// avoids - one hop away.
	if h.Get("X-Accel-Buffering") != "no" {
		t.Error("nginx is not told to stop buffering")
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("content type sniffing is not disabled")
	}
}
