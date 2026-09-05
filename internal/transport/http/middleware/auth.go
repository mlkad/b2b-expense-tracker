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

// DownloadTokenParser is the slice of auth.DownloadTokens this needs.
type DownloadTokenParser interface {
	Parse(token, query string) (auth.Subject, error)
}

// DownloadQueryParam is where a signed download token travels.
const DownloadQueryParam = "token"

// AllowBearerOrDownloadToken authenticates a download.
//
// A browser navigating to a link cannot set an Authorization header, so an
// export link carrying only a bearer token does not work at all - it arrives
// with no credential and is refused. Fetching the report with a header instead
// and turning it into a Blob would hold the whole thing in the tab, which is
// the cost the streaming export exists to avoid.
//
// So a download is authorised the way the receipt store authorises one: with a
// signed URL. The token is minted by an authenticated request, lives for a
// minute, and is bound to the exact query string it was issued for - the export
// reads its filters from the URL, so anything not covered by the signature is a
// parameter the holder could change.
//
// A bearer token is still accepted, because it is what an API client would use.
func AllowBearerOrDownloadToken(
	bearers TokenParser,
	downloads DownloadTokenParser,
	log *slog.Logger,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if raw, ok := bearerToken(r); ok {
				subject, err := bearers.Parse(raw)
				if err != nil {
					unauthorized(w, "invalid or expired token")
					return
				}
				next.ServeHTTP(w, r.WithContext(WithSubject(r.Context(), subject)))
				return
			}

			query := r.URL.Query()
			raw := query.Get(DownloadQueryParam)
			if raw == "" {
				unauthorized(w, "missing bearer token")
				return
			}

			// The token is removed before the signed query is reconstructed,
			// because it cannot be part of what it signs.
			query.Del(DownloadQueryParam)

			subject, err := downloads.Parse(raw, query.Encode())
			if err != nil {
				log.DebugContext(r.Context(), "download token rejected",
					slog.String("reason", err.Error()),
					slog.String("remote_addr", ClientIP(r)))
				unauthorized(w, "invalid or expired download link")
				return
			}

			next.ServeHTTP(w, r.WithContext(WithSubject(r.Context(), subject)))
		})
	}
}
