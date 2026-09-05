package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/expense"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/service"
)

// Notifier is whatever actually sends a message. It is an interface because
// this package should not care whether that is email, Slack or a webhook, and
// because a test needs to assert what would have been sent without sending it.
type Notifier interface {
	ExpenseDecided(ctx context.Context, tenantID, expenseID string, action expense.Action) error
	BudgetThresholdBreached(ctx context.Context, tenantID string, c repo.Consumption) error
}

type Handlers struct {
	db       *postgres.DB
	expenses *repo.ExpenseRepository
	budgets  *repo.BudgetRepository
	tenancy  *repo.TenancyRepository
	billing  *service.BillingService
	notifier Notifier
	log      *slog.Logger

	// queue lets a sweep fan out into per-item jobs. Nil disables the fan-out
	// sweeps, which is what a test wiring only the direct handlers wants.
	queue *Client
}

func NewHandlers(
	db *postgres.DB,
	expenses *repo.ExpenseRepository,
	budgets *repo.BudgetRepository,
	tenancy *repo.TenancyRepository,
	billing *service.BillingService,
	queue *Client,
	notifier Notifier,
	log *slog.Logger,
) *Handlers {
	return &Handlers{
		db: db, expenses: expenses, budgets: budgets, tenancy: tenancy,
		billing: billing, queue: queue, notifier: notifier, log: log,
	}
}

// Register wires the task types to their handlers.
func (h *Handlers) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(TaskExpenseTransition, h.HandleExpenseTransition)
	mux.HandleFunc(TaskBudgetThreshold, h.HandleBudgetThreshold)
	mux.HandleFunc(TaskBillingReconcile, h.HandleBillingReconcile)
	mux.HandleFunc(TaskRecurringSweep, h.HandleRecurringSweep)
	mux.HandleFunc(TaskBillingReconcileSweep, h.HandleBillingReconcileSweep)
	mux.HandleFunc(TaskRelaySweep, h.HandleRelaySweep)
	mux.HandleFunc(TaskSessionCleanup, h.HandleSessionCleanup)
}

// HandleExpenseTransition notifies whoever needs to know about a decision.
//
// It loads the claim rather than trusting the payload, inside a transaction
// bound to the tenant from the payload. That binding is what applies RLS to a
// worker: a job carrying a mismatched tenant and expense id finds nothing,
// because the row is filtered out - not because the worker remembered to
// check.
func (h *Handlers) HandleExpenseTransition(ctx context.Context, t *asynq.Task) error {
	var payload ExpenseTransitionPayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		// A payload this process cannot parse will never parse. Retrying it
		// burns the retry budget and fills the queue; SkipRetry archives it
		// where someone can look.
		return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
	}

	return h.db.WithTenantTx(ctx, postgres.Binding{TenantID: payload.TenantID, ReadOnly: true},
		func(ctx context.Context, tc *postgres.TenantConn) error {
			claim, err := h.expenses.Get(ctx, tc, payload.ExpenseID)
			if err != nil {
				if errors.Is(err, shared.ErrNotFound) {
					// Deleted between the enqueue and now, or a payload naming
					// a claim in another tenant. Neither is worth retrying.
					h.log.InfoContext(ctx, "notification skipped: claim is gone",
						slog.String("expense_id", payload.ExpenseID.String()))
					return nil
				}
				return err
			}

			if h.notifier == nil {
				return nil
			}
			return h.notifier.ExpenseDecided(ctx, payload.TenantID.String(), claim.ID.String(), payload.Action)
		})
}

// HandleBudgetThreshold raises an alert when an envelope crosses its warning
// line.
func (h *Handlers) HandleBudgetThreshold(ctx context.Context, t *asynq.Task) error {
	var payload BudgetThresholdPayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
	}

	return h.db.WithTenantTx(ctx, postgres.Binding{TenantID: payload.TenantID, ReadOnly: true},
		func(ctx context.Context, tc *postgres.TenantConn) error {
			today := time.Now().UTC()
			envelopes, err := h.budgets.Consumption(ctx, tc, &today)
			if err != nil {
				return err
			}

			for _, envelope := range envelopes {
				if payload.DepartmentID != nil && !sameDepartment(envelope.DepartmentID, payload.DepartmentID) {
					continue
				}
				if !envelope.BreachesThreshold() {
					continue
				}

				h.log.InfoContext(ctx, "budget threshold breached",
					slog.String("tenant_id", payload.TenantID.String()),
					slog.String("budget_id", envelope.BudgetID.String()),
					slog.Int64("usage_bps", envelope.UsageBps()))

				if h.notifier != nil {
					if err := h.notifier.BudgetThresholdBreached(ctx, payload.TenantID.String(), envelope); err != nil {
						return err
					}
				}
			}
			return nil
		})
}

func (h *Handlers) HandleBillingReconcile(ctx context.Context, t *asynq.Task) error {
	var payload BillingReconcilePayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
	}
	return h.billing.Reconcile(ctx, payload.TenantID, payload.CustomerRef)
}

// RecurringBatchSize bounds one sweep transaction.
//
// The batch is claimed FOR UPDATE, so the locks are held until the transaction
// commits. A large batch means a long-held lock set and a long transaction
// pinning the oldest xmin, which stops VACUUM across the whole database.
const RecurringBatchSize = 100

// HandleRecurringSweep materialises draft claims for vendor subscriptions that
// are due.
//
// This is the one job that crosses tenants, so it runs in a system
// transaction. Everything it does is still per tenant - each claim it creates
// is written under that tenant's id and would fail the WITH CHECK clause
// otherwise - but the query that finds the due rows cannot be bound to a
// single tenant, because it is looking across all of them.
//
// It is idempotent twice over: the claim it takes excludes rows already
// generated for the current charge date, and
// expenses_recurring_once_per_charge_key refuses a duplicate at the database
// even if two sweeps somehow raced past that.
func (h *Handlers) HandleRecurringSweep(ctx context.Context, _ *asynq.Task) error {
	today := time.Now().UTC().Truncate(24 * time.Hour)

	for {
		var claimed int

		err := h.db.WithSystemTx(ctx, postgres.Binding{}, "materialise recurring vendor charges",
			func(ctx context.Context, tc *postgres.TenantConn) error {
				due, err := h.budgets.ClaimDue(ctx, tc, today, RecurringBatchSize)
				if err != nil {
					return err
				}
				claimed = len(due)

				for _, sub := range due {
					if err := h.materialise(ctx, tc, sub, today); err != nil {
						return err
					}
				}
				return nil
			})
		if err != nil {
			return err
		}

		// A short page means the queue is drained. Looping until then keeps
		// each transaction small while still finishing the day's work.
		if claimed < RecurringBatchSize {
			return nil
		}
	}
}

// materialise creates the draft claim for one recurring charge.
//
// The system transaction it runs in already satisfies the isolation policy,
// but the row is still written with the subscription's own tenant id, so the
// ordinary tenant clause holds too and the row is indistinguishable from one a
// person filed.
func (h *Handlers) materialise(ctx context.Context, tc *postgres.TenantConn, sub repo.DueSubscription, today time.Time) error {
	if sub.OwnerID == nil {
		// Without an owning membership there is nobody to attribute the claim
		// to, and expenses.submitter_id is NOT NULL. Advancing the date anyway
		// stops the sweep retrying it every day forever.
		h.log.WarnContext(ctx, "recurring subscription has no owner; skipping",
			slog.String("subscription_id", sub.ID.String()))
		return h.budgets.Advance(ctx, tc, sub.ID, repo.NextChargeDate(sub.NextChargeOn, sub.Cadence))
	}

	claim, event, err := expense.New(sub.TenantID, *sub.OwnerID, expense.Draft{
		DepartmentID: sub.DepartmentID,
		Category:     expense.CategorySoftware,
		Amount:       sub.Amount,
		Merchant:     sub.Vendor,
		Description:  sub.PlanName,
		SpentAt:      sub.NextChargeOn,
	}, time.Now().UTC())
	if err != nil {
		return err
	}
	claim.SourceSubscriptionID = &sub.ID
	event.ActorID = nil // the system acted, not a person

	if err := h.expenses.Create(ctx, tc, claim, event); err != nil {
		if errors.Is(err, shared.ErrConflict) || errors.Is(err, shared.ErrValidation) {
			// The unique index refused a duplicate for this charge date. The
			// work is already done; advance and move on.
			h.log.InfoContext(ctx, "recurring claim already exists for this charge",
				slog.String("subscription_id", sub.ID.String()))
		} else {
			return err
		}
	}

	return h.budgets.Advance(ctx, tc, sub.ID, repo.NextChargeDate(sub.NextChargeOn, sub.Cadence))
}

func sameDepartment(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// -----------------------------------------------------------------------------
// Periodic maintenance
// -----------------------------------------------------------------------------

// HandleBillingReconcileSweep enqueues one reconciliation per tenant.
//
// The relay is at-least-once but not guaranteed: a delivery that arrived during
// a deployment window, or one that failed every retry, leaves the local
// projection behind. Nothing on the request path notices, because the
// entitlement read is local and looks perfectly healthy - so the only way that
// error is ever found is by asking the gateway.
//
// The fan-out is per tenant rather than a loop inside this job so a single
// tenant whose gateway call fails is retried on its own, instead of aborting
// the sweep and leaving every tenant after it in the list unchecked.
func (h *Handlers) HandleBillingReconcileSweep(ctx context.Context, _ *asynq.Task) error {
	if h.queue == nil {
		h.log.WarnContext(ctx, "reconciliation sweep has no queue configured; skipping")
		return nil
	}

	tenants, err := h.billing.TenantsToReconcile(ctx)
	if err != nil {
		return err
	}

	var failed int
	for tenantID, customerRef := range tenants {
		if err := h.queue.EnqueueReconcile(ctx, tenantID, customerRef); err != nil {
			// One tenant that cannot be enqueued must not stop the rest. The
			// sweep runs again tomorrow, and the count below is what makes a
			// persistent problem visible.
			failed++
			h.log.ErrorContext(ctx, "could not enqueue reconciliation",
				slog.String("tenant_id", tenantID.String()),
				slog.String("error", err.Error()))
		}
	}

	h.log.InfoContext(ctx, "billing reconciliation fanned out",
		slog.Int("tenants", len(tenants)),
		slog.Int("failed_to_enqueue", failed))
	return nil
}

// RelayStaleAfter is how long a delivery may sit in 'processing' before the
// sweeper treats it as abandoned.
//
// It has to exceed the relay route's own timeout, or the sweeper reclaims
// deliveries that are still being handled and applies them twice. The route
// allows 25 seconds; five minutes leaves room for a slow gateway call inside
// the handler without ever overlapping.
const (
	RelayStaleAfter = 5 * time.Minute
	RelaySweepBatch = 50
)

// HandleRelaySweep reprocesses deliveries that were claimed and never settled.
//
// The receiver claims an event id before processing it, because claiming after
// verification is what stops a forged id from getting a genuine delivery
// discarded. The cost of that ordering is this failure mode: a process that
// dies between the claim and the settle leaves a row that no redelivery can
// get past, since the gateway's retry now looks like a duplicate.
//
// Without this sweep, one crash at the wrong moment drops a subscription
// change permanently, and nothing surfaces it - the tenant simply keeps the
// plan they had.
func (h *Handlers) HandleRelaySweep(ctx context.Context, _ *asynq.Task) error {
	stuck, err := h.billing.ReclaimStuckDeliveries(ctx, RelayStaleAfter, RelaySweepBatch)
	if err != nil {
		return err
	}
	if len(stuck) == 0 {
		return nil
	}

	h.log.WarnContext(ctx, "reclaiming billing deliveries that were never settled",
		slog.Int("count", len(stuck)))

	var applied, abandoned int
	for _, delivery := range stuck {
		outcome, err := h.billing.ReapplyStuck(ctx, delivery)
		switch {
		case err != nil:
			// Left in 'processing' with attempts bumped, so the next sweep
			// picks it up and eventually gives up on it.
			h.log.ErrorContext(ctx, "reclaimed delivery failed again",
				slog.String("event_id", delivery.EventID),
				slog.Int("attempts", int(delivery.Attempts)),
				slog.String("error", err.Error()))
		case outcome == service.RelayApplied:
			applied++
		default:
			abandoned++
		}
	}

	h.log.InfoContext(ctx, "relay sweep complete",
		slog.Int("applied", applied), slog.Int("skipped_or_abandoned", abandoned))
	return nil
}

// RefreshTokenGrace is how long an expired refresh token is kept.
//
// It is not zero on purpose: after a suspected session theft, an investigation
// needs to see whether a token was revoked or merely expired, and a row that
// has already been deleted answers neither question.
const RefreshTokenGrace = 30 * 24 * time.Hour

// HandleSessionCleanup removes long-expired refresh tokens.
//
// Every login writes a row here and, until this ran, nothing ever removed one -
// so the table and the index behind the rotation lookup grew in proportion to
// the product's entire history of sign-ins rather than to its live sessions.
func (h *Handlers) HandleSessionCleanup(ctx context.Context, _ *asynq.Task) error {
	deleted, err := h.tenancy.PurgeExpiredRefreshTokens(ctx, h.db, RefreshTokenGrace)
	if err != nil {
		return err
	}
	if deleted > 0 {
		h.log.InfoContext(ctx, "purged expired refresh tokens", slog.Int64("deleted", deleted))
	}
	return nil
}
