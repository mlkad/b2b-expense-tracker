package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/expense"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/repository/postgres/gen"
)

// ExpenseRepository is stateless: the transaction it works in arrives as an
// argument, not as a field.
//
// That shape is what makes the tenant binding impossible to get wrong. A
// repository holding a pool or a connection would have to be told which tenant
// each call is for, and "told" means an argument someone can pass the wrong
// value for. Here the tenant is a property of the handle, the handle can only
// come from WithTenantTx, and the repository reads it back with
// tc.TenantID() rather than accepting it.
type ExpenseRepository struct{}

func NewExpenseRepository() *ExpenseRepository { return &ExpenseRepository{} }

// Create persists a new claim and its 'created' ledger row in one transaction.
func (r *ExpenseRepository) Create(ctx context.Context, tc *postgres.TenantConn, e *expense.Expense, ev expense.Event) error {
	q := gen.New(tc)

	row, err := q.CreateExpense(ctx, gen.CreateExpenseParams{
		ID:                   e.ID,
		TenantID:             tc.TenantID(),
		SubmitterID:          e.SubmitterID,
		DepartmentID:         e.DepartmentID,
		Status:               gen.ExpenseStatus(e.Status),
		Category:             gen.ExpenseCategory(e.Category),
		AmountMinor:          e.Amount.Minor,
		Currency:             string(e.Amount.Currency),
		Merchant:             e.Merchant,
		Description:          e.Description,
		SpentAt:              e.SpentAt,
		Revision:             e.Revision,
		Version:              e.Version,
		SourceSubscriptionID: e.SourceSubscriptionID,
		CreatedAt:            e.CreatedAt,
		UpdatedAt:            e.UpdatedAt,
	})
	if err != nil {
		return translate(err)
	}
	if err := r.appendEvent(ctx, tc, ev); err != nil {
		return err
	}

	*e = *toDomainExpense(row)
	return nil
}

// ErrAlreadyMaterialised means a claim for this charge already exists. It is
// the expected outcome of a sweep re-reading a subscription whose previous run
// was interrupted after the insert, and is not a failure.
var ErrAlreadyMaterialised = errors.New("a claim for this charge already exists")

// CreateRecurring writes a claim generated from a vendor subscription.
//
// Separate from Create because a duplicate must not abort the transaction: the
// caller still has to advance the subscription's charge date, and a poisoned
// transaction means it cannot - so the same subscription is retried, and fails,
// every day thereafter.
func (r *ExpenseRepository) CreateRecurring(ctx context.Context, tc *postgres.TenantConn, e *expense.Expense, ev expense.Event) error {
	q := gen.New(tc)

	row, err := q.CreateRecurringExpense(ctx, gen.CreateRecurringExpenseParams{
		ID:                   e.ID,
		TenantID:             tc.TenantID(),
		SubmitterID:          e.SubmitterID,
		DepartmentID:         e.DepartmentID,
		Status:               gen.ExpenseStatus(e.Status),
		Category:             gen.ExpenseCategory(e.Category),
		AmountMinor:          e.Amount.Minor,
		Currency:             string(e.Amount.Currency),
		Merchant:             e.Merchant,
		Description:          e.Description,
		SpentAt:              e.SpentAt,
		Revision:             e.Revision,
		Version:              e.Version,
		SourceSubscriptionID: e.SourceSubscriptionID,
		CreatedAt:            e.CreatedAt,
		UpdatedAt:            e.UpdatedAt,
	})
	if err != nil {
		// No row means ON CONFLICT DO NOTHING fired, which is the duplicate
		// case rather than a missing row.
		if translate(err) == shared.ErrNotFound {
			return ErrAlreadyMaterialised
		}
		return translate(err)
	}

	if err := r.appendEvent(ctx, tc, ev); err != nil {
		return err
	}
	*e = *toDomainExpense(row)
	return nil
}

func (r *ExpenseRepository) Get(ctx context.Context, tc *postgres.TenantConn, id uuid.UUID) (*expense.Expense, error) {
	row, err := gen.New(tc).GetExpense(ctx, gen.GetExpenseParams{TenantID: tc.TenantID(), ID: id})
	if err != nil {
		return nil, translate(err)
	}
	return toDomainExpense(row), nil
}

// GetForUpdate loads a claim and holds its row for the rest of the
// transaction.
//
// Every state transition goes through this rather than Get. The compare-and-
// swap in Save would already reject a lost update, but only after the ledger
// row had been written - the append and the update are two statements, and
// without the lock a second approver can slot in between them. Taking the lock
// first makes the read-decide-write sequence serial for one claim, while
// leaving every other claim in the tenant untouched.
func (r *ExpenseRepository) GetForUpdate(ctx context.Context, tc *postgres.TenantConn, id uuid.UUID) (*expense.Expense, error) {
	row, err := gen.New(tc).GetExpenseForUpdate(ctx, gen.GetExpenseForUpdateParams{TenantID: tc.TenantID(), ID: id})
	if err != nil {
		return nil, translate(err)
	}
	return toDomainExpense(row), nil
}

// Save writes a transitioned claim and appends the ledger row describing it.
//
// expectedVersion is the version the caller read, before the state machine
// bumped it. A zero row count means somebody else moved the claim in between,
// and the caller is told so rather than having their write silently overwrite
// a decision the user never saw.
func (r *ExpenseRepository) Save(
	ctx context.Context,
	tc *postgres.TenantConn,
	e *expense.Expense,
	ev expense.Event,
	expectedVersion int32,
) error {
	q := gen.New(tc)

	row, err := q.TransitionExpense(ctx, gen.TransitionExpenseParams{
		TenantID:        tc.TenantID(),
		ID:              e.ID,
		ExpectedVersion: expectedVersion,
		Status:          gen.ExpenseStatus(e.Status),
		SubmittedAt:     e.SubmittedAt,
		DecidedAt:       e.DecidedAt,
		DecidedBy:       e.DecidedBy,
		DecisionNote:    e.DecisionNote,
		PaidAt:          e.PaidAt,
		PaymentRef:      e.PaymentRef,
		Revision:        e.Revision,
		Version:         e.Version,
		UpdatedAt:       e.UpdatedAt,
	})
	if err != nil {
		// pgx.ErrNoRows here is the compare-and-swap losing, not a missing
		// claim: the row was read moments ago in the same transaction.
		if translated := translate(err); translated == shared.ErrNotFound {
			return fmt.Errorf("%w: the claim was changed while you were deciding", shared.ErrStaleWrite)
		} else {
			return translated
		}
	}

	if err := r.appendEvent(ctx, tc, ev); err != nil {
		return err
	}

	*e = *toDomainExpense(row)
	return nil
}

// UpdateDraft persists an edit. Same compare-and-swap, plus a status predicate
// in the SQL: an edit can only ever touch a draft.
func (r *ExpenseRepository) UpdateDraft(
	ctx context.Context,
	tc *postgres.TenantConn,
	e *expense.Expense,
	ev expense.Event,
	expectedVersion int32,
) error {
	q := gen.New(tc)

	row, err := q.UpdateExpenseDraft(ctx, gen.UpdateExpenseDraftParams{
		TenantID:        tc.TenantID(),
		ID:              e.ID,
		ExpectedVersion: expectedVersion,
		DepartmentID:    e.DepartmentID,
		Category:        gen.ExpenseCategory(e.Category),
		AmountMinor:     e.Amount.Minor,
		Currency:        string(e.Amount.Currency),
		Merchant:        e.Merchant,
		Description:     e.Description,
		SpentAt:         e.SpentAt,
		Version:         e.Version,
		UpdatedAt:       e.UpdatedAt,
	})
	if err != nil {
		if translated := translate(err); translated == shared.ErrNotFound {
			return fmt.Errorf("%w: the claim was changed or submitted while you were editing", shared.ErrStaleWrite)
		} else {
			return translated
		}
	}

	if err := r.appendEvent(ctx, tc, ev); err != nil {
		return err
	}
	*e = *toDomainExpense(row)
	return nil
}

func (r *ExpenseRepository) DeleteDraft(ctx context.Context, tc *postgres.TenantConn, id uuid.UUID) error {
	n, err := gen.New(tc).DeleteExpenseDraft(ctx, gen.DeleteExpenseDraftParams{TenantID: tc.TenantID(), ID: id})
	if err != nil {
		return translate(err)
	}
	if n == 0 {
		// Either it does not exist, it belongs to another tenant, or it is no
		// longer a draft. The first two are indistinguishable under RLS and
		// must stay that way; ErrNotFound covers all three without saying
		// which, and the state machine gives a better message on the path
		// where the claim was actually loaded first.
		return shared.ErrNotFound
	}
	return nil
}

func (r *ExpenseRepository) appendEvent(ctx context.Context, tc *postgres.TenantConn, ev expense.Event) error {
	metadata := []byte("{}")
	if len(ev.Metadata) > 0 {
		encoded, err := json.Marshal(ev.Metadata)
		if err != nil {
			return fmt.Errorf("encode event metadata: %w", err)
		}
		metadata = encoded
	}

	var from *gen.ExpenseStatus
	if ev.FromStatus != nil {
		s := gen.ExpenseStatus(*ev.FromStatus)
		from = &s
	}

	_, err := gen.New(tc).AppendExpenseEvent(ctx, gen.AppendExpenseEventParams{
		TenantID:    tc.TenantID(),
		ExpenseID:   ev.ExpenseID,
		Action:      gen.ExpenseAction(ev.Action),
		FromStatus:  from,
		ToStatus:    gen.ExpenseStatus(ev.ToStatus),
		ActorID:     ev.ActorID,
		Reason:      ev.Reason,
		AmountMinor: ev.Amount.Minor,
		Currency:    string(ev.Amount.Currency),
		Revision:    ev.Revision,
		Metadata:    metadata,
		OccurredAt:  ev.OccurredAt,
	})
	return translate(err)
}

// Filter is the set of narrowing options the list and export endpoints share.
// Nil fields mean "no constraint", which is what the sqlc.narg parameters in
// the queries expect.
type Filter struct {
	Status       *expense.Status
	Category     *expense.Category
	DepartmentID *uuid.UUID
	SubmitterID  *uuid.UUID
	SpentFrom    *time.Time
	SpentTo      *time.Time
	MinMinor     *int64
	MaxMinor     *int64
	Search       *string
}

// List returns one page plus, when there are more, a single extra row. The
// caller passes limit+1 and uses the extra to decide has_more without a COUNT.
func (r *ExpenseRepository) List(
	ctx context.Context,
	tc *postgres.TenantConn,
	f Filter,
	cursor *shared.Cursor,
	limit int32,
) ([]*expense.Expense, error) {
	params := gen.ListExpensesParams{
		TenantID:     tc.TenantID(),
		Category:     nullCategory(f.Category),
		Status:       nullStatus(f.Status),
		DepartmentID: f.DepartmentID,
		SubmitterID:  f.SubmitterID,
		SpentFrom:    f.SpentFrom,
		SpentTo:      f.SpentTo,
		MinMinor:     f.MinMinor,
		MaxMinor:     f.MaxMinor,
		Search:       f.Search,
		PageLimit:    limit,
	}
	if cursor != nil {
		spentAt := cursor.SpentAt
		id := cursor.ID
		params.CursorSpentAt = &spentAt
		params.CursorID = &id
	}

	rows, err := gen.New(tc).ListExpenses(ctx, params)
	if err != nil {
		return nil, translate(err)
	}

	out := make([]*expense.Expense, len(rows))
	for i, row := range rows {
		out[i] = toDomainExpense(row)
	}
	return out, nil
}

// ListPendingForApproval serves the approver's queue from the partial index,
// oldest first.
func (r *ExpenseRepository) ListPendingForApproval(
	ctx context.Context,
	tc *postgres.TenantConn,
	departmentID *uuid.UUID,
	cursor *shared.Cursor,
	limit int32,
) ([]*expense.Expense, error) {
	params := gen.ListPendingForApprovalParams{
		TenantID:     tc.TenantID(),
		DepartmentID: departmentID,
		PageLimit:    limit,
	}
	if cursor != nil {
		at := cursor.SpentAt
		id := cursor.ID
		params.CursorSubmittedAt = &at
		params.CursorID = &id
	}

	rows, err := gen.New(tc).ListPendingForApproval(ctx, params)
	if err != nil {
		return nil, translate(err)
	}
	out := make([]*expense.Expense, len(rows))
	for i, row := range rows {
		out[i] = toDomainExpense(row)
	}
	return out, nil
}

// EventRecord is one row of the audit ledger with the actor's email resolved.
type EventRecord struct {
	ID         int64
	ExpenseID  uuid.UUID
	Action     expense.EventAction
	FromStatus *expense.Status
	ToStatus   expense.Status
	ActorEmail *string
	Reason     *string
	Amount     shared.Money
	Revision   int32
	OccurredAt time.Time
}

func (r *ExpenseRepository) Events(ctx context.Context, tc *postgres.TenantConn, expenseID uuid.UUID) ([]EventRecord, error) {
	rows, err := gen.New(tc).ListExpenseEvents(ctx, gen.ListExpenseEventsParams{
		TenantID:  tc.TenantID(),
		ExpenseID: expenseID,
	})
	if err != nil {
		return nil, translate(err)
	}

	out := make([]EventRecord, len(rows))
	for i, row := range rows {
		rec := EventRecord{
			ID:         row.ID,
			ExpenseID:  row.ExpenseID,
			Action:     expense.EventAction(row.Action),
			ToStatus:   expense.Status(row.ToStatus),
			ActorEmail: row.ActorEmail,
			Reason:     row.Reason,
			Amount:     shared.Money{Minor: row.AmountMinor, Currency: currency(row.Currency)},
			Revision:   row.Revision,
			OccurredAt: row.OccurredAt,
		}
		if row.FromStatus != nil {
			s := expense.Status(*row.FromStatus)
			rec.FromStatus = &s
		}
		out[i] = rec
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Mapping
// -----------------------------------------------------------------------------

// toDomainExpense converts a generated row to the domain entity.
//
// It is written out field by field rather than done with reflection or a
// shared struct. The two shapes are allowed to diverge - the row is a
// persistence detail and the entity is the model - and when they do, this
// function stops compiling, which is where the divergence should be noticed.
func toDomainExpense(row gen.Expense) *expense.Expense {
	return &expense.Expense{
		ID:                   row.ID,
		TenantID:             row.TenantID,
		SubmitterID:          row.SubmitterID,
		DepartmentID:         row.DepartmentID,
		Status:               expense.Status(row.Status),
		Category:             expense.Category(row.Category),
		Amount:               shared.Money{Minor: row.AmountMinor, Currency: currency(row.Currency)},
		Merchant:             row.Merchant,
		Description:          row.Description,
		SpentAt:              row.SpentAt,
		SubmittedAt:          row.SubmittedAt,
		DecidedAt:            row.DecidedAt,
		DecidedBy:            row.DecidedBy,
		DecisionNote:         row.DecisionNote,
		PaidAt:               row.PaidAt,
		PaymentRef:           row.PaymentRef,
		Revision:             row.Revision,
		Version:              row.Version,
		SourceSubscriptionID: row.SourceSubscriptionID,
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
	}
}

// currency trims the column value before wrapping it.
//
// CHAR(3) is blank-padded by PostgreSQL, and while the CHECK constraint means
// every stored value is already three characters, a comparison against an
// untrimmed value from some other source would fail in a way that is very hard
// to see in a log.
func currency(s string) shared.Currency {
	return shared.Currency(strings.TrimSpace(s))
}

// nullStatus and nullCategory convert a domain filter into the pointer the
// generated parameters expect. The pointer is the sqlc.narg contract: nil is
// SQL NULL, which the `narg IS NULL OR column = narg` idiom reads as "no
// constraint on this column".
func nullStatus(s *expense.Status) *gen.ExpenseStatus {
	if s == nil {
		return nil
	}
	v := gen.ExpenseStatus(*s)
	return &v
}

func nullCategory(c *expense.Category) *gen.ExpenseCategory {
	if c == nil {
		return nil
	}
	v := gen.ExpenseCategory(*c)
	return &v
}
