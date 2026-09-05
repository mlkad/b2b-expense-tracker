package middleware

import (
	"net/http"
	"strconv"
	"strings"
)

// CORSConfig lists the browser origins allowed to call the API.
type CORSConfig struct {
	// AllowedOrigins must be exact origins - scheme, host and port. There is
	// no wildcard and no pattern matching, because credentials are involved:
	// a response carrying Access-Control-Allow-Credentials: true with an
	// origin the browser did not vet lets any site read a logged-in user's
	// data. Reflecting the request's Origin unconditionally is the same bug
	// wearing a disguise.
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	ExposedHeaders []string
	MaxAgeSeconds  int
}

func (c CORSConfig) withDefaults() CORSConfig {
	if len(c.AllowedMethods) == 0 {
		c.AllowedMethods = []string{"GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"}
	}
	if len(c.AllowedHeaders) == 0 {
		c.AllowedHeaders = []string{"Authorization", "Content-Type", "X-Request-Id", "Idempotency-Key"}
	}
	if len(c.ExposedHeaders) == 0 {
		// Content-Disposition has to be exposed or a browser fetch() cannot
		// read the filename of a downloaded report.
		c.ExposedHeaders = []string{"X-Request-Id", "Content-Disposition"}
	}
	if c.MaxAgeSeconds <= 0 {
		c.MaxAgeSeconds = 600
	}
	return c
}

func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	cfg = cfg.withDefaults()

	allowed := make(map[string]struct{}, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		allowed[strings.TrimRight(strings.TrimSpace(o), "/")] = struct{}{}
	}

	methods := strings.Join(cfg.AllowedMethods, ", ")
	headers := strings.Join(cfg.AllowedHeaders, ", ")
	exposed := strings.Join(cfg.ExposedHeaders, ", ")
	maxAge := strconv.Itoa(cfg.MaxAgeSeconds)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			_, ok := allowed[strings.TrimRight(origin, "/")]

			if origin != "" && ok {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Expose-Headers", exposed)
				// The response varies by Origin, so a cache that ignored this
				// would serve one origin's CORS headers to another.
				h.Add("Vary", "Origin")
			}

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if ok {
					h := w.Header()
					h.Set("Access-Control-Allow-Methods", methods)
					h.Set("Access-Control-Allow-Headers", headers)
					h.Set("Access-Control-Max-Age", maxAge)
					h.Add("Vary", "Access-Control-Request-Method")
					h.Add("Vary", "Access-Control-Request-Headers")
				}
				// A disallowed preflight gets 204 with no CORS headers rather
				// than an error status. The browser blocks it either way, and
				// a 4xx here shows up in the developer console as a server
				// fault rather than as the policy decision it is.
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
