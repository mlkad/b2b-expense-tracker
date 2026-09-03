package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/expense"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/service"
	"github.com/mlkad/b2b-expense-tracker/internal/transport/http/middleware"
)

type ExpenseHandler struct {
	expenses *service.ExpenseService
}

func NewExpenseHandler(expenses *service.ExpenseService) *ExpenseHandler {
	return &ExpenseHandler{expenses: expenses}
}

// draftRequest is the wire shape of a claim.
//
// It is a separate type from expense.Draft rather than the entity itself. The
// entity has a Status field, and a handler that unmarshalled JSON straight
// onto it would let a client set the status directly - filing a claim that
// arrives already approved. There is no such field here to set.
type draftRequest struct {
	DepartmentID *uuid.UUID `json:"department_id"`
	Category     string     `json:"category"`
	AmountMinor  int64      `json:"amount_minor"`
	Currency     string     `json:"currency"`
	Merchant     string     `json:"merchant"`
	Description  *string    `json:"description"`
	SpentAt      string     `json:"spent_at"`
}

func (d draftRequest) toDomain() (expense.Draft, error) {
	var v shared.Validator

	currency, err := shared.ParseCurrency(d.Currency)
	if err != nil {
		v.Add("currency", "must be a three-letter ISO 4217 code")
	}

	spentAt, err := time.Parse("2006-01-02", d.SpentAt)
	if err != nil {
		v.Add("spent_at", "must be a date in YYYY-MM-DD form")
	}

	if err := v.Err(); err != nil {
		return expense.Draft{}, err
	}

	// The remaining field rules live on expense.Draft, which is where the
	// domain enforces them. Duplicating them here would give two answers to
	// the same question.
	return expense.Draft{
		DepartmentID: d.DepartmentID,
		Category:     expense.Category(d.Category),
		Amount:       shared.Money{Minor: d.AmountMinor, Currency: currency},
		Merchant:     d.Merchant,
		Description:  d.Description,
		SpentAt:      spentAt,
	}, nil
}

// expenseResponse carries the claim plus what this caller may do with it, so
// the dashboard renders the right buttons without reimplementing the state
// machine in TypeScript.
type expenseResponse struct {
	*expense.Expense
	AllowedActions []expense.Action `json:"allowed_actions"`
}

func (h *ExpenseHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req draftRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	draft, err := req.toDomain()
	if err != nil {
		writeError(w, r, err)
		return
	}

	created, err := h.expenses.Create(r.Context(), middleware.MustSubject(r), draft)
	if err != nil {
		writeError(w, r, err)
		return
	}

	w.Header().Set("Location", "/api/v1/expenses/"+created.ID.String())
	writeJSON(w, http.StatusCreated, created)
}

func (h *ExpenseHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}

	found, allowed, _, err := h.expenses.Get(r.Context(), middleware.MustSubject(r), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, expenseResponse{Expense: found, AllowedActions: allowed})
}

func (h *ExpenseHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req draftRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	draft, err := req.toDomain()
	if err != nil {
		writeError(w, r, err)
		return
	}

	updated, err := h.expenses.Update(r.Context(), middleware.MustSubject(r), id, draft)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *ExpenseHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.expenses.Delete(r.Context(), middleware.MustSubject(r), id); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type transitionRequest struct {
	Reason     *string `json:"reason,omitempty"`
	PaymentRef *string `json:"payment_ref,omitempty"`
}

// Transition is one endpoint per action rather than a single PATCH that takes
// a status.
//
// A `PATCH {"status": "approved"}` API invites a client to think of the status
// as a field it sets, and every transition guard then has to be re-derived
// from the before and after values. `POST /expenses/{id}/approve` says what is
// being attempted, which is what the state machine takes as input, and it
// makes the audit ledger's action column a direct record of the request.
func (h *ExpenseHandler) Transition(action expense.Action) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathUUID(r, "id")
		if err != nil {
			writeError(w, r, err)
			return
		}

		var req transitionRequest
		// Reason and payment reference are optional in the wire shape; the
		// state machine decides which action requires which, so an empty body
		// is valid input that fails validation one layer down with a message
		// naming the field.
		if r.ContentLength > 0 {
			if err := decodeJSON(w, r, &req); err != nil {
				writeError(w, r, err)
				return
			}
		}

		moved, err := h.expenses.Transition(r.Context(), middleware.MustSubject(r), id, action, req.Reason, req.PaymentRef)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, moved)
	}
}

func (h *ExpenseHandler) List(w http.ResponseWriter, r *http.Request) {
	q := newQueryReader(r)
	query := service.ListQuery{
		Filter: q.filter(),
		Cursor: q.cursor("cursor"),
		Limit:  q.intDefault("limit", shared.DefaultPageSize),
	}
	if err := q.err(); err != nil {
		writeError(w, r, err)
		return
	}

	page, err := h.expenses.List(r.Context(), middleware.MustSubject(r), query)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *ExpenseHandler) PendingQueue(w http.ResponseWriter, r *http.Request) {
	q := newQueryReader(r)
	cursor := q.cursor("cursor")
	limit := q.intDefault("limit", shared.DefaultPageSize)
	if err := q.err(); err != nil {
		writeError(w, r, err)
		return
	}

	page, err := h.expenses.PendingQueue(r.Context(), middleware.MustSubject(r), cursor, limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *ExpenseHandler) History(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}

	events, err := h.expenses.History(r.Context(), middleware.MustSubject(r), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": events})
}

// pathUUID parses a URL parameter, returning a field error rather than a 404
// for a malformed id: "not a uuid" is a fixable client mistake, and answering
// 404 makes it look like a missing resource.
func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	raw := chi.URLParam(r, name)
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, shared.FieldError{Field: name, Detail: "is not a valid id"}
	}
	return id, nil
}
