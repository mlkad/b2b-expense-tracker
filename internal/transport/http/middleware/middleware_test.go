package middleware

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// -----------------------------------------------------------------------------
// RequestID
// -----------------------------------------------------------------------------

func TestRequestID(t *testing.T) {
	t.Run("one is minted when the client sends none", func(t *testing.T) {
		rec := httptest.NewRecorder()
		var seen string
		RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = RequestIDFromContext(r.Context())
		})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		if seen == "" {
			t.Fatal("no request id in context")
		}
		if rec.Header().Get("X-Request-Id") != seen {
			t.Fatal("the response header and the context disagree")
		}
	})

	// The id goes into log lines and into a response header, so an unfiltered
	// value is a log injection: a newline turns one record into two, and the
	// second says whatever the caller wanted.
	t.Run("hostile inbound ids are stripped", func(t *testing.T) {
		cases := map[string]string{
			"newline injection": "abc\ninjected=\"admin\"",
			"header injection":  "abc\r\nX-Admin: true",
			"spaces and quotes": `abc "quoted" value`,
			"very long":         strings.Repeat("a", 500),
		}
		for name, hostile := range cases {
			t.Run(name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.Header.Set("X-Request-Id", hostile)
				rec := httptest.NewRecorder()

				var seen string
				RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					seen = RequestIDFromContext(r.Context())
				})).ServeHTTP(rec, req)

				if strings.ContainsAny(seen, "\r\n \"") {
					t.Fatalf("dangerous characters survived: %q", seen)
				}
				if len(seen) > 64 {
					t.Fatalf("id is %d characters; unbounded ids bloat every log line", len(seen))
				}
			})
		}
	})
}

// -----------------------------------------------------------------------------
// RequireAuth
// -----------------------------------------------------------------------------

type stubParser struct {
	subject auth.Subject
	err     error
}

func (s stubParser) Parse(string) (auth.Subject, error) { return s.subject, s.err }

// reject runs one unauthenticated request and returns what the client saw. The
// inner handler fails the test if it is ever reached.
func reject(t *testing.T, parser stubParser, header string) *httptest.ResponseRecorder {
	t.Helper()

	h := RequireAuth(parser, discardLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("the handler ran for an unauthenticated request")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRequireAuth(t *testing.T) {
	subject := auth.Subject{UserID: uuid.New(), TenantID: uuid.New(), Email: "a@b.c"}

	t.Run("a valid token reaches the handler with the subject", func(t *testing.T) {
		var seen auth.Subject
		h := RequireAuth(stubParser{subject: subject}, discardLogger())(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = MustSubject(r)
				w.WriteHeader(http.StatusOK)
			}))

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer some-token")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		if seen.TenantID != subject.TenantID {
			t.Fatal("the tenant did not reach the handler")
		}
	})

	t.Run("the scheme is matched case-insensitively", func(t *testing.T) {
		h := RequireAuth(stubParser{subject: subject}, discardLogger())(http.HandlerFunc(ok))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "bearer some-token")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status %d; RFC 7235 makes the scheme case-insensitive and clients send lowercase", rec.Code)
		}
	})

	// Two groups, and only one of them has to be uniform.
	//
	// "You sent no credential" is not an oracle - the caller already knows what
	// they sent, and saying so saves an afternoon of debugging. What must not
	// vary is the answer among *presented* tokens: distinguishing expired from
	// malformed from wrongly-signed tells a forger which part to fix next.
	t.Run("no credential is reported plainly", func(t *testing.T) {
		for name, header := range map[string]string{
			"no header":    "",
			"wrong scheme": "Basic dXNlcjpwYXNz",
			"empty token":  "Bearer ",
		} {
			t.Run(name, func(t *testing.T) {
				rec := reject(t, stubParser{subject: subject}, header)
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status %d, want 401", rec.Code)
				}
			})
		}
	})

	t.Run("every presented token is rejected identically", func(t *testing.T) {
		parsers := map[string]stubParser{
			"invalid":        {err: auth.ErrTokenInvalid},
			"expired":        {err: auth.ErrTokenExpired},
			"bad signature":  {err: errors.New("signature is invalid")},
			"wrong audience": {err: auth.ErrTokenInvalid},
		}

		var bodies []string
		for name, parser := range parsers {
			t.Run(name, func(t *testing.T) {
				rec := reject(t, parser, "Bearer some-token")
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status %d, want 401", rec.Code)
				}
				if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "invalid_token") {
					t.Errorf("WWW-Authenticate = %q; clients use it to decide whether to refresh",
						rec.Header().Get("WWW-Authenticate"))
				}
				bodies = append(bodies, rec.Body.String())
			})
		}

		for i := 1; i < len(bodies); i++ {
			if bodies[i] != bodies[0] {
				t.Fatalf("two rejected tokens produced different answers:\n  %s  %s", bodies[0], bodies[i])
			}
		}
	})
}

// A handler asking for the caller on a route that was never wrapped in
// RequireAuth is a wiring mistake, and must not be reported as 401 - that would
// make an unprotected route look protected.
func TestMissingSubjectIsAWiringErrorNotA401(t *testing.T) {
	_, err := SubjectFromContext(httptest.NewRequest(http.MethodGet, "/", nil).Context())
	if !errors.Is(err, ErrNoSubject) {
		t.Fatalf("got %v, want ErrNoSubject", err)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("MustSubject returned on an unprotected route instead of panicking")
		}
	}()
	MustSubject(httptest.NewRequest(http.MethodGet, "/", nil))
}

// -----------------------------------------------------------------------------
// Client address
// -----------------------------------------------------------------------------

// X-Forwarded-For is attacker-controlled unless every hop in front is trusted
// and counted. A rate limiter keyed on an unvalidated header has an off switch.
//
// The list grows left to right: each proxy appends the address of whoever
// connected to it. So behind N trusted proxies the genuine client sits at
// len-N, and everything to the left of that was supplied by the client itself
// and may be anything at all. Counting from the right is what makes the forged
// entries unreachable.
func TestTrustedProxyIP(t *testing.T) {
	const remote = "10.0.0.1"
	const client = "203.0.113.7"

	req := func(xff string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote + ":34567"
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}

	cases := []struct {
		name string
		xff  string
		hops int
		want string
	}{
		// The default. The header is ignored entirely, which is the only safe
		// behaviour when the number of hops is unknown.
		{"no proxies trusted", client, 0, remote},
		{"header absent", "", 1, remote},

		// One proxy: it appended the address it saw, and that is the only
		// entry present when the client sent nothing.
		{"one hop, honest client", client, 1, client},
		// The client prepended a forgery before the proxy appended the truth.
		{"one hop, client forged an entry", "evil-forged-value, " + client, 1, client},

		// Two proxies: the rightmost entry is the first proxy's own address,
		// and the client is one further left.
		{"two hops", client + ", 10.1.1.1", 2, client},
		{"two hops, client forged an entry", "evil-forged-value, " + client + ", 10.1.1.1", 2, client},

		// Misconfiguration must fall back to the address that cannot be
		// forged rather than pick something arbitrary.
		{"more hops claimed than present", client, 5, remote},
		{"the selected entry is not an address", "not-an-ip", 1, remote},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TrustedProxyIP(req(c.xff), c.hops); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}

	// The property that matters: whatever the client prepends, it is never
	// what comes back when the hop count is right.
	t.Run("a forged prefix is never selected", func(t *testing.T) {
		for _, forged := range []string{"1.1.1.1", "evil", "127.0.0.1", strings.Repeat("9.9.9.9, ", 20)} {
			got := TrustedProxyIP(req(forged+", "+client), 1)
			if got != client {
				t.Errorf("forged prefix %q produced %q, want %q", forged, got, client)
			}
		}
	})

	t.Run("ClientIP never reads the header", func(t *testing.T) {
		if got := ClientIP(req(client)); got != remote {
			t.Fatalf("got %q; RemoteAddr is the only value that cannot be forged", got)
		}
	})
}

// -----------------------------------------------------------------------------
// CORS
// -----------------------------------------------------------------------------

func TestCORS(t *testing.T) {
	cfg := CORSConfig{AllowedOrigins: []string{"https://app.example.com"}}
	handler := CORS(cfg)(http.HandlerFunc(ok))

	t.Run("an allowed origin is echoed with credentials and Vary", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Origin", "https://app.example.com")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
			t.Fatalf("allow-origin = %q", got)
		}
		if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
			t.Error("credentials not allowed; the dashboard sends a cookie on refresh")
		}
		if !strings.Contains(rec.Header().Get("Vary"), "Origin") {
			t.Error("no Vary: Origin; a cache would serve one origin's headers to another")
		}
	})

	// Reflecting an arbitrary Origin while allowing credentials lets any site
	// read a logged-in user's data.
	t.Run("an unknown origin gets no CORS headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Origin", "https://evil.example.com")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("an unlisted origin was allowed")
		}
	})

	t.Run("preflight from an allowed origin advertises the methods", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/", nil)
		req.Header.Set("Origin", "https://app.example.com")
		req.Header.Set("Access-Control-Request-Method", "POST")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("status %d", rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "POST") {
			t.Error("POST not advertised")
		}
		if !strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), "Content-Disposition") {
			t.Error("Content-Disposition not exposed; a fetch() download cannot read the filename")
		}
	})

	t.Run("a trailing slash on a configured origin still matches", func(t *testing.T) {
		h := CORS(CORSConfig{AllowedOrigins: []string{"https://app.example.com/"}})(http.HandlerFunc(ok))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Origin", "https://app.example.com")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Header().Get("Access-Control-Allow-Origin") == "" {
			t.Fatal("a configuration typo that is trivially normalisable broke CORS entirely")
		}
	})
}

// -----------------------------------------------------------------------------
// Recoverer
// -----------------------------------------------------------------------------

func TestRecoverer(t *testing.T) {
	t.Run("a panic becomes a 500 with no stack in the body", func(t *testing.T) {
		h := Recoverer(discardLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("database credentials are hunter2")
		}))

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status %d", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "goroutine") {
			t.Fatalf("the panic value or a stack trace reached the client: %s", rec.Body.String())
		}
	})

	// http.ErrAbortHandler is the documented way to drop a connection
	// deliberately, which the export handler uses when a report fails after
	// the first byte. Swallowing it would turn an aborted download into a
	// spurious 500 in the log.
	t.Run("ErrAbortHandler is re-panicked untouched", func(t *testing.T) {
		h := Recoverer(discardLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		}))

		defer func() {
			if rec := recover(); rec != http.ErrAbortHandler {
				t.Fatalf("recovered %v, want it to propagate", rec)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	})
}

func TestSecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	SecurityHeaders(http.HandlerFunc(ok)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	// Sending HSTS over plain HTTP is ignored by browsers, and setting it in
	// local development pins localhost to HTTPS for a year.
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS was set on a plaintext request")
	}
}
