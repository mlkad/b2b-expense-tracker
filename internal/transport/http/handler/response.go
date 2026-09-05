// Package handler holds the HTTP handlers. They decode, delegate and encode,
// and they make no authorisation decisions: a handler that decided who may do
// what would be a second copy of the permission matrix, kept in sync by hand.
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/expense"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/gateway"
	"github.com/mlkad/b2b-expense-tracker/internal/logger"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/service"
	"github.com/mlkad/b2b-expense-tracker/internal/transport/http/middleware"
)

// MaxRequestBytes caps a JSON body. Without it, a request with a
// multi-gigabyte body is an out-of-memory condition that costs the sender
// nothing but bandwidth.
const MaxRequestBytes = 1 << 20 // 1 MiB

type errorBody struct {
	Status  int                 `json:"status"`
	Message string              `json:"message"`
	Fields  []shared.FieldError `json:"fields,omitempty"`
	TraceID string              `json:"trace_id,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// The status and headers are already on the wire, so there is no way
		// to report this to the client. It goes to the log, where it shows up
		// as a truncated response the client will have noticed too.
		slog.Default().Error("encode response", slog.String("error", err.Error()))
	}
}

// writeError maps a domain error onto a status code.
//
// This is the only place that mapping exists. Every handler returns errors
// from the services untouched, so a new sentinel is handled once here rather
// than in each handler that can produce it.
//
// The status codes are chosen for what the client should do next:
//
//	400  the request is malformed - fix it
//	401  no valid credential - log in
//	403  a valid credential without the authority - ask someone else
//	404  no such thing, or nothing you may see - stop asking
//	409  the world changed - reload and try again
//	413  too much - narrow the range
//	422  the request is well formed but the values are wrong - fix a field
//	503  a dependency is down - retry later
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	log := logger.FromContext(r.Context())
	traceID := middleware.RequestIDFromContext(r.Context())

	var fieldErrs shared.FieldErrors
	var fieldErr shared.FieldError

	switch {
	case errors.As(err, &fieldErrs):
		writeJSON(w, http.StatusUnprocessableEntity, errorBody{
			Status: http.StatusUnprocessableEntity, Message: "the request could not be processed",
			Fields: fieldErrs, TraceID: traceID,
		})
		return

	case errors.As(err, &fieldErr):
		writeJSON(w, http.StatusUnprocessableEntity, errorBody{
			Status: http.StatusUnprocessableEntity, Message: "the request could not be processed",
			Fields: []shared.FieldError{fieldErr}, TraceID: traceID,
		})
		return
	}

	status, message := classify(err)

	// 5xx is the only class where the error text is withheld. Everything else
	// is the caller's own mistake and telling them what it was is the point;
	// an internal failure's message can name a table, a constraint or a host.
	if status >= 500 {
		log.ErrorContext(r.Context(), "request failed",
			slog.String("error", err.Error()),
			slog.String("path", r.URL.Path))
		message = "internal server error"
	} else {
		log.DebugContext(r.Context(), "request rejected",
			slog.Int("status", status),
			slog.String("error", err.Error()))
	}

	writeJSON(w, status, errorBody{Status: status, Message: message, TraceID: traceID})
}

func classify(err error) (int, string) {
	var planLimit *service.ErrPlanLimit

	switch {
	case errors.Is(err, shared.ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, shared.ErrForbidden):
		return http.StatusForbidden, err.Error()
	case errors.Is(err, shared.ErrConflict), errors.Is(err, shared.ErrStaleWrite):
		return http.StatusConflict, err.Error()
	case errors.Is(err, shared.ErrValidation):
		return http.StatusUnprocessableEntity, err.Error()
	case errors.As(err, &planLimit):
		// 402, not 403. The caller has the authority; what they lack is the
		// plan. A 403 would send them to an administrator, who cannot help.
		// The message names the current count and the ceiling so the dashboard
		// can offer the upgrade rather than just refusing.
		return http.StatusPaymentRequired, planLimit.Error()
	case errors.Is(err, service.ErrExportTooLarge):
		return http.StatusRequestEntityTooLarge, err.Error()
	case errors.Is(err, gateway.ErrUnavailable):
		return http.StatusServiceUnavailable, "the payment service is temporarily unavailable"
	case errors.Is(err, gateway.ErrRejected):
		return http.StatusBadGateway, err.Error()
	case errors.Is(err, shared.ErrTenantMismatch), errors.Is(err, shared.ErrNoTenantContext):
		// Both are wiring bugs, not client errors. Reporting them as 403 would
		// make a broken code path look like a permission decision and it would
		// be triaged as one.
		return http.StatusInternalServerError, "internal server error"
	case errors.Is(err, middleware.ErrNoSubject):
		return http.StatusInternalServerError, "internal server error"
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

// decodeJSON reads a request body into v.
//
// DisallowUnknownFields is on. A client sending `{"amount_minor": 100,
// "aproval_limit": 5}` has made a typo that would otherwise be silently
// ignored, and the caller would spend an afternoon wondering why the limit did
// not apply.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if mediaType := strings.TrimSpace(strings.Split(ct, ";")[0]); mediaType != "application/json" {
			return shared.FieldError{Field: "content-type", Detail: "must be application/json"}
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			return shared.FieldError{Field: "body", Detail: "is required"}
		case errors.As(err, &maxErr):
			return shared.FieldError{Field: "body", Detail: fmt.Sprintf("must be at most %d bytes", MaxRequestBytes)}
		default:
			return shared.FieldError{Field: "body", Detail: "is not valid json: " + err.Error()}
		}
	}

	// A second value in the stream means the client sent two documents. It is
	// always a bug and quietly ignoring the second is how a request means
	// something different to the client than to the server.
	if dec.More() {
		return shared.FieldError{Field: "body", Detail: "must contain exactly one json document"}
	}
	return nil
}

// -----------------------------------------------------------------------------
// Query parameter parsing
// -----------------------------------------------------------------------------

type queryReader struct {
	values map[string][]string
	errs   shared.Validator
}

func newQueryReader(r *http.Request) *queryReader {
	return &queryReader{values: r.URL.Query()}
}

func (q *queryReader) raw(name string) string {
	if v, ok := q.values[name]; ok && len(v) > 0 {
		return strings.TrimSpace(v[0])
	}
	return ""
}

func (q *queryReader) uuid(name string) *uuid.UUID {
	s := q.raw(name)
	if s == "" {
		return nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		q.errs.Add(name, "is not a valid id")
		return nil
	}
	return &id
}

func (q *queryReader) date(name string) *time.Time {
	s := q.raw(name)
	if s == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		q.errs.Add(name, "must be a date in YYYY-MM-DD form")
		return nil
	}
	return &t
}

func (q *queryReader) int64(name string) *int64 {
	s := q.raw(name)
	if s == "" {
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		q.errs.Add(name, "must be a whole number")
		return nil
	}
	return &v
}

func (q *queryReader) intDefault(name string, def int) int {
	s := q.raw(name)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		q.errs.Add(name, "must be a whole number")
		return def
	}
	return v
}

func (q *queryReader) status(name string) *expense.Status {
	s := q.raw(name)
	if s == "" {
		return nil
	}
	parsed, err := expense.ParseStatus(s)
	if err != nil {
		q.errs.Add(name, "is not a known status")
		return nil
	}
	return &parsed
}

func (q *queryReader) category(name string) *expense.Category {
	s := q.raw(name)
	if s == "" {
		return nil
	}
	c := expense.Category(s)
	if !c.Valid() {
		q.errs.Add(name, "is not a known category")
		return nil
	}
	return &c
}

// search caps the term. An unbounded string reaches a trigram index as a
// pattern, and a very long one is expensive to match against every row.
func (q *queryReader) search(name string) *string {
	s := q.raw(name)
	if s == "" {
		return nil
	}
	if len(s) > 100 {
		q.errs.Add(name, "must be at most 100 characters")
		return nil
	}
	return &s
}

func (q *queryReader) cursor(name string) *shared.Cursor {
	s := q.raw(name)
	if s == "" {
		return nil
	}
	c, err := shared.DecodeCursor(s)
	if err != nil {
		q.errs.Add(name, "is not a valid pagination cursor")
		return nil
	}
	return &c
}

func (q *queryReader) err() error { return q.errs.Err() }

// filter builds a repository filter from the standard query parameters, shared
// by the list and export endpoints so the two cannot drift.
func (q *queryReader) filter() repo.Filter {
	return repo.Filter{
		Status:       q.status("status"),
		Category:     q.category("category"),
		DepartmentID: q.uuid("department_id"),
		SubmitterID:  q.uuid("submitter_id"),
		SpentFrom:    q.date("from"),
		SpentTo:      q.date("to"),
		MinMinor:     q.int64("min_amount_minor"),
		MaxMinor:     q.int64("max_amount_minor"),
		Search:       q.search("q"),
	}
}

// WriteNotFound and WriteMethodNotAllowed give the router's fallbacks the same
// error envelope as every handler, so a client has one shape to parse.
func WriteNotFound(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, errorBody{
		Status: http.StatusNotFound, Message: "not found",
		TraceID: middleware.RequestIDFromContext(r.Context()),
	})
}

func WriteMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusMethodNotAllowed, errorBody{
		Status: http.StatusMethodNotAllowed, Message: "method not allowed",
		TraceID: middleware.RequestIDFromContext(r.Context()),
	})
}
