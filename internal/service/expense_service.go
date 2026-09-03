package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/expense"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/tenant"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
)

// Enqueuer is the slice of the job queue this service needs. It is an
// interface so the service can be constructed in a test without Redis, and so
// that a failure to enqueue is visible as a dependency rather than hidden
// inside a package-level client.
type Enqueuer interface {
	NotifyExpenseTransition(ctx context.Context, tenantID, expenseID uuid.UUID, action expense.Action) error
	CheckBudgetThreshold(ctx context.Context, tenantID uuid.UUID, departmentID *uuid.UUID) error
}

type ExpenseService struct {
	scope    *Scope
	expenses *repo.ExpenseRepository
	queue    Enqueuer

	// now is injectable so tests can pin the clock. Every timestamp the state
	// machine writes comes from here, so a test can assert an exact value
	// rather than a tolerance.
	now func() time.Time
}

func NewExpenseService(scope *Scope, expenses *repo.ExpenseRepository, queue Enqueuer) *ExpenseService {
	return &ExpenseService{scope: scope, expenses: expenses, queue: queue, now: time.Now}
}

// Create files a new claim in draft.
func (s *ExpenseService) Create(ctx context.Context, subject auth.Subject, draft expense.Draft) (*expense.Expense, error) {
	var created *expense.Expense

	err := s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermExpenseCreate); err != nil {
			return err
		}
		// A member may only file against their own department. A tenant-wide
		// actor may file against any, including none.
		if !actor.GovernsDepartment(draft.DepartmentID) {
			return fmt.Errorf("%w: you may only file claims against your own department", shared.ErrForbidden)
		}

		e, ev, err := expense.New(tc.TenantID(), actor.MembershipID, draft, s.now())
		if err != nil {
			return err
		}
		if err := s.expenses.Create(ctx, tc, e, ev); err != nil {
			return err
		}
		created = e
		return nil
	})

	return created, err
}

// Get loads one claim, with the actions this actor may take on it.
func (s *ExpenseService) Get(ctx context.Context, subject auth.Subject, id uuid.UUID) (*expense.Expense, []expense.Action, tenant.Actor, error) {
	var (
		found   *expense.Expense
		allowed []expense.Action
		who     tenant.Actor
	)

	err := s.scope.Read(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		e, err := s.expenses.Get(ctx, tc, id)
		if err != nil {
			return err
		}
		if err := s.canRead(actor, e); err != nil {
			return err
		}
		found, allowed, who = e, e.AllowedActions(actor), actor
		return nil
	})

	return found, allowed, who, err
}

// Update edits a draft.
func (s *ExpenseService) Update(ctx context.Context, subject auth.Subject, id uuid.UUID, draft expense.Draft) (*expense.Expense, error) {
	var updated *expense.Expense

	err := s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		e, err := s.expenses.GetForUpdate(ctx, tc, id)
		if err != nil {
			return err
		}
		if !actor.SameMembership(e.SubmitterID) {
			return fmt.Errorf("%w: only the person who filed a claim may edit it", shared.ErrForbidden)
		}
		if err := Require(actor, tenant.PermExpenseCreate); err != nil {
			return err
		}

		expected := e.Version
		ev, err := e.Edit(draft, actor.MembershipID, s.now())
		if err != nil {
			return err
		}
		if err := s.expenses.UpdateDraft(ctx, tc, e, ev, expected); err != nil {
			return err
		}
		updated = e
		return nil
	})

	return updated, err
}

// Transition applies a command from the state machine.
//
// This method is the whole design in fourteen lines, and the order of those
// lines is the point:
//
//  1. Open a read-write transaction. WithTenantTx binds app.tenant_id for its
//     duration, so every statement below is filtered by RLS to this tenant and
//     the binding is reverted when the transaction ends.
//  2. Resolve the actor from the database, in the same transaction.
//  3. Load the claim FOR UPDATE, so no other approver can act on it until
//     this transaction commits.
//  4. Ask the domain. The state machine decides; this method does not.
//  5. Persist with a compare-and-swap on the version, and append the ledger
//     row in the same transaction.
//  6. Enqueue the notification.
//
// Steps 3 through 5 are what make two simultaneous approvals produce one
// approval and one 409 rather than two ledger rows.
func (s *ExpenseService) Transition(
	ctx context.Context,
	subject auth.Subject,
	id uuid.UUID,
	action expense.Action,
	reason, paymentRef *string,
) (*expense.Expense, error) {
	var moved *expense.Expense

	err := s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		e, err := s.expenses.GetForUpdate(ctx, tc, id)
		if err != nil {
			return err
		}

		expected := e.Version

		ev, err := e.Apply(expense.Command{
			Action:     action,
			Actor:      actor,
			Reason:     reason,
			PaymentRef: paymentRef,
		}, s.now())
		if err != nil {
			return err
		}

		if err := s.expenses.Save(ctx, tc, e, ev, expected); err != nil {
			return err
		}
		moved = e
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Enqueued after the commit, deliberately.
	//
	// Enqueuing inside the transaction would mean a job could be picked up by
	// a worker before the transaction it describes had committed, and the
	// worker would load a claim in its previous state. Enqueuing after means
	// that if the process dies in this gap the notification is lost - which is
	// the better of the two failures, and the reconciliation sweep catches it.
	s.notify(ctx, moved, action)

	return moved, nil
}

func (s *ExpenseService) notify(ctx context.Context, e *expense.Expense, action expense.Action) {
	if s.queue == nil || e == nil {
		return
	}
	// A failure to enqueue must not fail the request: the state change is
	// already committed and telling the user it failed would invite them to
	// retry an action that has happened.
	_ = s.queue.NotifyExpenseTransition(ctx, e.TenantID, e.ID, action)

	if e.Status.CountsAgainstBudget() {
		_ = s.queue.CheckBudgetThreshold(ctx, e.TenantID, e.DepartmentID)
	}
}

// Delete discards a draft.
func (s *ExpenseService) Delete(ctx context.Context, subject auth.Subject, id uuid.UUID) error {
	return s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		e, err := s.expenses.GetForUpdate(ctx, tc, id)
		if err != nil {
			return err
		}
		if !actor.SameMembership(e.SubmitterID) {
			return fmt.Errorf("%w: only the person who filed a claim may discard it", shared.ErrForbidden)
		}
		if err := Require(actor, tenant.PermExpenseDeleteOwn); err != nil {
			return err
		}
		if !e.Status.Editable() {
			return fmt.Errorf("%w: a submitted claim is a record and cannot be deleted", shared.ErrConflict)
		}
		return s.expenses.DeleteDraft(ctx, tc, id)
	})
}

// ListQuery is the request the list endpoint decodes into.
type ListQuery struct {
	Filter repo.Filter
	Cursor *shared.Cursor
	Limit  int
}

// List returns a page of claims the actor is entitled to see.
//
// The scope narrowing happens here rather than in SQL for members and
// managers: the filter is rewritten so a member's list is their own claims and
// a department-scoped manager's is their department's. Rewriting the filter
// rather than post-filtering the page is what keeps the page size honest -
// filtering after the query would return short pages and a cursor that skips.
func (s *ExpenseService) List(ctx context.Context, subject auth.Subject, q ListQuery) (shared.Page[*expense.Expense], error) {
	var page shared.Page[*expense.Expense]

	err := s.scope.Read(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		filter, err := s.narrowToScope(actor, q.Filter)
		if err != nil {
			return err
		}

		limit := shared.ClampLimit(q.Limit)
		rows, err := s.expenses.List(ctx, tc, filter, q.Cursor, int32(limit+1))
		if err != nil {
			return err
		}

		page = shared.NewPage(rows, limit, func(e *expense.Expense) shared.Cursor {
			return shared.Cursor{SpentAt: e.SpentAt, ID: e.ID}
		})
		return nil
	})

	return page, err
}

// PendingQueue is the approver's work list.
func (s *ExpenseService) PendingQueue(ctx context.Context, subject auth.Subject, cursor *shared.Cursor, limit int) (shared.Page[*expense.Expense], error) {
	var page shared.Page[*expense.Expense]

	err := s.scope.Read(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermExpenseApprove); err != nil {
			return err
		}

		n := shared.ClampLimit(limit)
		rows, err := s.expenses.ListPendingForApproval(ctx, tc, actor.DepartmentID, cursor, int32(n+1))
		if err != nil {
			return err
		}

		page = shared.NewPage(rows, n, func(e *expense.Expense) shared.Cursor {
			at := e.SubmittedAt
			if at == nil {
				// Unreachable: the query selects only pending claims, and
				// expenses_status_timestamps_chk guarantees those have a
				// submitted_at. Falling back to spent_at keeps the cursor
				// well-formed rather than panicking if that ever changes.
				return shared.Cursor{SpentAt: e.SpentAt, ID: e.ID}
			}
			return shared.Cursor{SpentAt: *at, ID: e.ID}
		})
		return nil
	})

	return page, err
}

// History returns the audit ledger for one claim.
func (s *ExpenseService) History(ctx context.Context, subject auth.Subject, id uuid.UUID) ([]repo.EventRecord, error) {
	var events []repo.EventRecord

	err := s.scope.Read(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		e, err := s.expenses.Get(ctx, tc, id)
		if err != nil {
			return err
		}
		if err := s.canRead(actor, e); err != nil {
			return err
		}
		events, err = s.expenses.Events(ctx, tc, id)
		return err
	})

	return events, err
}

// canRead decides visibility of a single claim.
//
// RLS has already restricted the row to the caller's tenant; this is the
// narrower question of who within the tenant may see it. Returning ErrNotFound
// rather than ErrForbidden for a claim outside the actor's scope is
// deliberate: a 403 would confirm that a claim with that id exists, which is
// itself information a member should not be able to enumerate.
func (s *ExpenseService) canRead(actor tenant.Actor, e *expense.Expense) error {
	if actor.SameMembership(e.SubmitterID) {
		if actor.Can(tenant.PermExpenseReadOwn) {
			return nil
		}
		return shared.ErrForbidden
	}
	if actor.Can(tenant.PermExpenseReadAll) {
		return nil
	}
	if actor.Can(tenant.PermExpenseReadTeam) && actor.GovernsDepartment(e.DepartmentID) {
		return nil
	}
	return shared.ErrNotFound
}

// narrowToScope rewrites a client's filter so it cannot ask for more than the
// actor may see.
func (s *ExpenseService) narrowToScope(actor tenant.Actor, f repo.Filter) (repo.Filter, error) {
	switch {
	case actor.Can(tenant.PermExpenseReadAll):
		// No narrowing. The client's filter stands as given.

	case actor.Can(tenant.PermExpenseReadTeam):
		// A department-scoped actor is pinned to their department, whatever
		// the request asked for. Overwriting rather than validating means a
		// request naming another department returns that actor's own claims
		// instead of an error - which leaks nothing about whether the other
		// department exists.
		if actor.DepartmentID != nil {
			f.DepartmentID = actor.DepartmentID
		}

	case actor.Can(tenant.PermExpenseReadOwn):
		id := actor.MembershipID
		f.SubmitterID = &id

	default:
		return repo.Filter{}, fmt.Errorf("%w: role %s may not list claims", shared.ErrForbidden, actor.Role)
	}
	return f, nil
}
