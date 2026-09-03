//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The specification is checked against the router rather than maintained
// alongside it.
//
// An API document that nobody verifies is a document that describes last
// quarter's API. This walks the real chi tree and compares it with
// api/openapi.json in both directions, so a route added without a spec entry
// fails the build, and so does a spec entry for a route that no longer exists.
func TestOpenAPIMatchesTheRouter(t *testing.T) {
	api, _ := newAPI(t)

	router, ok := api.(chi.Routes)
	if !ok {
		t.Fatal("the router does not expose its routes")
	}

	implemented := map[string]bool{}
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// chi reports a trailing slash on group mount points, and OPTIONS is
		// synthesised by the CORS middleware rather than being a route anyone
		// documents.
		route = strings.TrimSuffix(route, "/")
		if route == "" || method == http.MethodOptions {
			return nil
		}
		implemented[strings.ToLower(method)+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}

	raw, err := os.ReadFile("../../api/openapi.json")
	if err != nil {
		t.Fatalf("read specification: %v", err)
	}

	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string         `json:"operationId"`
			Summary     string         `json:"summary"`
			Tags        []string       `json:"tags"`
			Responses   map[string]any `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("specification is not valid json: %v", err)
	}

	documented := map[string]bool{}
	for path, operations := range spec.Paths {
		for method := range operations {
			documented[method+" "+path] = true
		}
	}

	var missing, extra []string
	for route := range implemented {
		if !documented[route] {
			missing = append(missing, route)
		}
	}
	for route := range documented {
		if !implemented[route] {
			extra = append(extra, route)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("these routes exist but are not in api/openapi.json:\n  %s", strings.Join(missing, "\n  "))
	}
	if len(extra) > 0 {
		t.Errorf("api/openapi.json documents routes that do not exist:\n  %s", strings.Join(extra, "\n  "))
	}
	if len(implemented) == 0 {
		t.Fatal("walked no routes; the comparison would pass vacuously")
	}
	if len(missing) == 0 && len(extra) == 0 {
		t.Logf("%d operations, all documented", len(implemented))
	}
}

// A document whose operations have no summary or no error responses is worse
// than none: it looks authoritative and answers nothing.
func TestOpenAPIOperationsAreUsable(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.json")
	if err != nil {
		t.Fatal(err)
	}

	var spec struct {
		Info struct {
			Title       string `json:"title"`
			Version     string `json:"version"`
			Description string `json:"description"`
		} `json:"info"`
		Paths map[string]map[string]struct {
			OperationID string         `json:"operationId"`
			Summary     string         `json:"summary"`
			Tags        []string       `json:"tags"`
			Security    *[]any         `json:"security"`
			Responses   map[string]any `json:"responses"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}

	if spec.Info.Title == "" || spec.Info.Version == "" || spec.Info.Description == "" {
		t.Error("the document has no title, version or description")
	}

	seen := map[string]string{}
	for path, operations := range spec.Paths {
		for method, op := range operations {
			where := fmt.Sprintf("%s %s", strings.ToUpper(method), path)

			if op.Summary == "" {
				t.Errorf("%s has no summary", where)
			}
			if len(op.Tags) == 0 {
				t.Errorf("%s has no tag, so it appears nowhere in a rendered document", where)
			}
			if op.OperationID == "" {
				t.Errorf("%s has no operationId, so a generated client cannot name it", where)
			} else if prior, dup := seen[op.OperationID]; dup {
				t.Errorf("%s and %s share the operationId %q; a generated client would have two methods of the same name",
					prior, where, op.OperationID)
			} else {
				seen[op.OperationID] = where
			}

			if len(op.Responses) == 0 {
				t.Errorf("%s documents no responses", where)
				continue
			}

			// A success and at least one failure. An operation documenting only
			// its happy path tells a client nothing about what to handle.
			var success, failure bool
			for code := range op.Responses {
				switch {
				case strings.HasPrefix(code, "2"), strings.HasPrefix(code, "3"):
					success = true
				case strings.HasPrefix(code, "4"), strings.HasPrefix(code, "5"):
					failure = true
				}
			}
			if !success {
				t.Errorf("%s documents no successful response", where)
			}
			if !failure {
				t.Errorf("%s documents no failure response", where)
			}

			// Everything but the probes and the HMAC-authenticated relay needs
			// a bearer token, and the document has to say so or a reader will
			// assume the API is open.
			isPublic := strings.HasPrefix(path, "/livez") || strings.HasPrefix(path, "/readyz") ||
				strings.HasPrefix(path, "/internal/") || strings.HasPrefix(path, "/api/v1/auth/")
			if !isPublic && op.Security != nil && len(*op.Security) == 0 {
				t.Errorf("%s opts out of authentication but is not a public route", where)
			}
		}
	}

	for _, required := range []string{"Money", "Expense", "Error", "FieldError", "Page", "ExpenseStatus"} {
		if _, ok := spec.Components.Schemas[required]; !ok {
			t.Errorf("the document has no %s schema", required)
		}
	}
}
