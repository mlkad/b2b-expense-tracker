package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
)

// TokenParser is the slice of auth.TokenService this middleware needs.
type TokenParser interface {
	Parse(token string) (auth.Subject, error)
}

// RequireAuth rejects any request without a valid bearer token and puts the
// verified subject - user and tenant - into the request context.
//
// The tenant comes from the signed token and from nowhere else. There is no
// X-Tenant-ID header, no tenant path segment, and no query parameter, because
// any of those would be a value the client chooses. Every tenant-scoped query
// in the service binds the database session to this claim, so a header-based
// tenant would be a one-line cross-tenant read.
//
// The response body says only that authentication failed. Distinguishing
// "expired" from "malformed" from "wrong signature" tells a forger which part
// to fix next; the distinction goes to the log instead. The WWW-Authenticate
// header is the exception: `error="invalid_token"` is what RFC 6750 defines
// and what clients use to decide whether to refresh.
func RequireAuth(tokens TokenParser, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				unauthorized(w, "missing bearer token")
				return
			}

			subject, err := tokens.Parse(raw)
			if err != nil {
				// Debug, not warn: an expired token is the normal state of any
				// long-lived browser tab, and logging it at warn trains
				// operators to ignore the level that matters.
				log.DebugContext(r.Context(), "token rejected",
					slog.String("reason", err.Error()),
					slog.Bool("expired", errors.Is(err, auth.ErrTokenExpired)),
					slog.String("remote_addr", ClientIP(r)))
				unauthorized(w, "invalid or expired token")
				return
			}

			ctx := WithSubject(r.Context(), subject)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bearerToken extracts the credential from an Authorization header.
//
// The scheme comparison is case-insensitive because RFC 7235 says it is, and
// clients in the wild send "bearer".
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

func unauthorized(w http.ResponseWriter, detail string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="api", error="invalid_token"`)
	writeProblem(w, http.StatusUnauthorized, detail)
}
