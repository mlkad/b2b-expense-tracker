// Package shared holds the vocabulary every other domain package needs:
// errors callers can branch on, money that cannot silently lose precision, and
// the pagination cursor the list endpoints agree on.
//
// It imports nothing from the rest of the project and performs no I/O.
package shared

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors. Repositories translate driver and constraint failures into
// these, so a service can branch on errors.Is without importing pgx, and the
// HTTP layer can map an error to a status code without knowing which layer
// produced it.
var (
	ErrNotFound   = errors.New("resource not found")
	ErrConflict   = errors.New("resource conflict")
	ErrValidation = errors.New("validation failed")

	// ErrForbidden means the caller is authenticated and their tenant is
	// correct, but their role does not permit this action. Distinct from
	// ErrNotFound on purpose - see the note on ErrTenantMismatch for the one
	// case where that distinction is deliberately collapsed.
	ErrForbidden = errors.New("action not permitted for this role")

	// ErrTenantMismatch means a request named a resource that exists but
	// belongs to another tenant.
	//
	// In practice the HTTP layer never sees this: RLS filters the row out
	// before the query returns, so the repository reports ErrNotFound and the
	// caller learns nothing about whether the id exists elsewhere. It is
	// defined for the paths that compare ids in Go - the export filters, the
	// worker payloads - where returning "forbidden" would confirm the id.
	ErrTenantMismatch = errors.New("resource belongs to another tenant")

	// ErrNoTenantContext means a repository call was made outside a
	// tenant-bound transaction. It is a wiring mistake, not a client error, and
	// must surface as 500: reporting it as 403 would make an unprotected code
	// path look like a protected one.
	ErrNoTenantContext = errors.New("no tenant bound to this transaction")

	// ErrStaleWrite means a compare-and-swap update matched no row because
	// another transaction changed it first. The caller re-reads and decides;
	// it is never retried blindly, because the second attempt would be acting
	// on a state the user never saw.
	ErrStaleWrite = errors.New("row changed since it was read")
)

// FieldError attributes a validation failure to a specific input field, so the
// dashboard can put the message next to the input that caused it.
type FieldError struct {
	Field  string `json:"field"`
	Detail string `json:"detail"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Detail }
func (e FieldError) Unwrap() error { return ErrValidation }

// FieldErrors aggregates every failure on one payload so the caller fixes all
// of them in one round trip rather than one per submission.
type FieldErrors []FieldError

func (e FieldErrors) Error() string {
	parts := make([]string, len(e))
	for i, fe := range e {
		parts[i] = fe.Error()
	}
	return strings.Join(parts, "; ")
}

func (e FieldErrors) Unwrap() error { return ErrValidation }

// Validator accumulates field errors. Zero value is ready to use.
type Validator struct{ errs FieldErrors }

func (v *Validator) Add(field, detail string) {
	v.errs = append(v.errs, FieldError{Field: field, Detail: detail})
}

func (v *Validator) Addf(field, format string, args ...any) {
	v.Add(field, fmt.Sprintf(format, args...))
}

// Err returns nil when nothing was added, so `return v.Err()` is always
// correct at the end of a Validate method.
func (v *Validator) Err() error {
	if len(v.errs) == 0 {
		return nil
	}
	return v.errs
}
