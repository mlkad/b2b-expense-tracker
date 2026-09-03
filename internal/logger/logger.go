// Package logger builds the process-wide structured logger.
package logger

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// Format selects the handler. JSON in every deployed environment because log
// aggregators parse it; text locally because humans read it.
type Format string

const (
	FormatJSON Format = "json"
	FormatText Format = "text"
)

// sensitiveKeys never appear in a log line whatever their value.
//
// The list is enforced by a ReplaceAttr hook rather than by remembering not to
// log these. A hook is checkable in one place; a convention is checkable only
// by reading every call site that will ever exist.
var sensitiveKeys = map[string]struct{}{
	"password":      {},
	"password_hash": {},
	"token":         {},
	"access_token":  {},
	"refresh_token": {},
	"authorization": {},
	"secret":        {},
	"signature":     {},
	"api_key":       {},
	"cookie":        {},
	"set-cookie":    {},
}

func New(level slog.Level, format Format, service, version string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if _, secret := sensitiveKeys[strings.ToLower(a.Key)]; secret {
				return slog.String(a.Key, "[redacted]")
			}
			// Errors are logged as a plain string. slog renders an error value
			// through its Error method anyway, but an error carrying a struct
			// can serialise to something enormous, and a log line that is
			// megabytes long is dropped by the aggregator rather than
			// truncated.
			if err, ok := a.Value.Any().(error); ok {
				return slog.String(a.Key, err.Error())
			}
			return a
		},
	}

	var h slog.Handler
	if format == FormatText {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(h).With(
		slog.String("service", service),
		slog.String("version", version),
	)
}

// ParseLevel maps a configuration string to a level, defaulting to info for
// anything unrecognised: a typo in LOG_LEVEL should not silence the service.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type ctxKey int

const loggerKey ctxKey = iota

// WithContext attaches a logger carrying request-scoped attributes.
func WithContext(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// FromContext returns the request's logger, or the default. It never returns
// nil, so a caller never has to check - which is what stops a missing logger
// from turning an error path into a panic.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
