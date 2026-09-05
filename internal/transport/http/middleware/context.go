// Package middleware holds the standard net/http middleware the router mounts.
//
// Every function here has the signature func(http.Handler) http.Handler, so
// they compose with chi's Use, with anything from the standard library, and
// with third-party middleware. Nothing in this package depends on chi.
package middleware

import (
	"context"
	"errors"
	"net/http"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
)

// ctxKey is unexported, and the constants below are the only values of it that
// exist. No other package can write an authenticated subject into a request
// context, so a handler reading SubjectFromContext is reading something only
// this package could have put there.
//
// This is not a style preference. If the key were a string, any package -
// including one handling untrusted input - could call context.WithValue with
// the same key and forge an identity.
type ctxKey int

const (
	subjectKey ctxKey = iota
	requestIDKey
)

// ErrNoSubject means a handler asked for the caller's identity on a route that
// was never wrapped in RequireAuth.
//
// It is a wiring mistake, not a client error, and must never be reported as
// 401: that would make an unprotected route look protected, and the mistake
// would survive until someone noticed the route had no authentication rather
// than a broken token.
var ErrNoSubject = errors.New("no authenticated subject in context")

func WithSubject(ctx context.Context, s auth.Subject) context.Context {
	return context.WithValue(ctx, subjectKey, s)
}

// SubjectFromContext returns the verified caller: which user, in which tenant.
func SubjectFromContext(ctx context.Context) (auth.Subject, error) {
	s, ok := ctx.Value(subjectKey).(auth.Subject)
	if !ok {
		return auth.Subject{}, ErrNoSubject
	}
	return s, nil
}

// MustSubject is for handlers mounted only under RequireAuth. It panics on a
// missing subject, which the Recoverer turns into a 500 - the correct status
// for a routing bug, and loud enough that it is fixed rather than tolerated.
func MustSubject(r *http.Request) auth.Subject {
	s, err := SubjectFromContext(r.Context())
	if err != nil {
		panic("middleware: " + err.Error() + "; this route is missing RequireAuth")
	}
	return s
}

func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}
