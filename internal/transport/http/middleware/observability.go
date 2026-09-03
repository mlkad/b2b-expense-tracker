package middleware

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/logger"
)

// RequestID assigns a correlation id, or adopts the one an upstream proxy
// supplied.
//
// An inbound id is length-capped and stripped of anything but the characters a
// uuid uses. It goes into log lines and into a response header, so an
// unfiltered value is a log injection - a newline in the header turns one
// record into two, and the second one says whatever the caller wanted it to.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitizeRequestID(r.Header.Get("X-Request-Id"))
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func sanitizeRequestID(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// statusRecorder captures what was actually written, so the access log reports
// the response rather than what the handler intended.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(p)
	s.bytes += int64(n)
	return n, err
}

// Unwrap lets http.NewResponseController reach the underlying writer, which is
// what the export handler uses to flush and to extend its deadline mid-stream.
// Without it, wrapping the writer would silently disable both.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog records one line per request and attaches a request-scoped logger.
//
// It must sit outside Recoverer. It reads the status after the inner handler
// returns, so a panic unwinding through it would be logged as 200, or not at
// all. With Recoverer inside, the recovered 500 is already written by the time
// this looks.
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}

			reqLog := log.With(slog.String("request_id", RequestIDFromContext(r.Context())))
			ctx := logger.WithContext(r.Context(), reqLog)

			next.ServeHTTP(rec, r.WithContext(ctx))

			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}

			level := slog.LevelInfo
			switch {
			case status >= 500:
				level = slog.LevelError
			case status >= 400:
				level = slog.LevelWarn
			}

			// The path is logged, the query string is not: filters carry
			// merchant names and amounts, and an access log is the most widely
			// readable artefact a service produces.
			reqLog.LogAttrs(ctx, level, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", status),
				slog.Int64("bytes", rec.bytes),
				slog.Duration("elapsed", time.Since(start)),
				slog.String("remote_addr", ClientIP(r)))
		})
	}
}

// Recoverer turns a panic into a 500 without taking the process down.
//
// It sits inside AccessLog and inside routing, so the recovered request is
// still logged and still carries its route pattern. The stack goes to the log
// and never to the response: a stack trace names internal paths, package
// versions and sometimes argument values.
func Recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				// http.ErrAbortHandler is the documented way to drop a
				// connection deliberately - the export handler uses it when a
				// report fails after the first byte. Re-panicking lets the
				// server handle it as intended instead of logging a false
				// alarm.
				if rec == http.ErrAbortHandler {
					panic(rec)
				}

				log.LogAttrs(r.Context(), slog.LevelError, "panic recovered",
					slog.Any("panic", rec),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("stack", string(debug.Stack())))

				writeProblem(w, http.StatusInternalServerError, "internal server error")
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout bounds a request. It is applied per route group rather than
// globally, because the health probes carry their own short deadline and must
// stay answerable when everything else is saturated, and the export endpoints
// need minutes rather than seconds.
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClientIP resolves the caller's address.
//
// RemoteAddr is the only value that cannot be forged, so it is what this
// returns. X-Forwarded-For is attacker-controlled unless every hop in front is
// trusted and counted, and a rate limiter keyed on an unvalidated header is a
// rate limiter with an off switch. TrustedProxyIP handles the deployed case
// where the count is known.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// TrustedProxyIP returns the client address as seen behind exactly `hops`
// trusted reverse proxies.
//
// Counting from the right is what makes it safe: the rightmost entry in
// X-Forwarded-For was appended by the nearest proxy and cannot be forged by
// the client, while the leftmost can be anything the client typed. Skipping
// `hops` entries from the right lands on the address the outermost trusted
// proxy observed. A hops of zero ignores the header entirely.
func TrustedProxyIP(r *http.Request, hops int) string {
	if hops <= 0 {
		return ClientIP(r)
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	idx := len(parts) - hops
	if idx < 0 || idx >= len(parts) {
		return ClientIP(r)
	}
	candidate := strings.TrimSpace(parts[idx])
	if net.ParseIP(candidate) == nil {
		return ClientIP(r)
	}
	return candidate
}

// problem is the error envelope every failing endpoint returns.
//
// One shape for every error means the dashboard has one error path. The
// fields are deliberately few: a machine-readable status, a human-readable
// message, and optionally the fields that failed validation.
type problem struct {
	Status  int              `json:"status"`
	Message string           `json:"message"`
	Fields  []map[string]any `json:"fields,omitempty"`
	TraceID string           `json:"trace_id,omitempty"`
}

func writeProblem(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Status: status, Message: message})
}

// SecurityHeaders sets the headers that are cheap and unconditionally correct
// for a JSON API.
//
// There is no Content-Security-Policy here: this service serves no HTML, and a
// CSP on a JSON response protects nothing while giving the impression the
// question has been handled. The dashboard sets its own.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// Only over TLS: sending HSTS over plain HTTP is ignored by browsers,
		// and setting it in local development pins localhost to HTTPS in the
		// developer's browser for a year.
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
