// Package worker holds the background jobs and the Asynq client that enqueues
// them.
//
// Work belongs here when it must not fail a request that has already
// succeeded, or when it crosses tenants. Notifying an approver is the first
// kind: the claim has been approved and committed, and an email provider being
// down must not turn that into a 500 the user retries. The recurring-charge
// sweep is the second.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/expense"
)

// Task type names. They are strings on the wire, so a rename is a breaking
// change for any job already in the queue - the old name has to keep a handler
// until the backlog drains.
const (
	TaskExpenseTransition = "expense:transition"
	TaskBudgetThreshold   = "budget:threshold"
	TaskBillingReconcile  = "billing:reconcile"
	TaskRecurringSweep    = "recurring:sweep"
	TaskRelaySweep        = "billing:relay_sweep"
)

// Queues, in priority order. Asynq weights them, so a backlog of nightly
// reconciliation cannot starve a notification a user is waiting on.
const (
	QueueCritical = "critical"
	QueueDefault  = "default"
	QueueLow      = "low"
)

// QueuePriorities are the weights the server draws with.
var QueuePriorities = map[string]int{
	QueueCritical: 6,
	QueueDefault:  3,
	QueueLow:      1,
}

// ExpenseTransitionPayload carries only identifiers.
//
// Every payload in this package does. A payload carrying the claim itself
// would be a copy of the row as it was when the job was enqueued, and by the
// time a worker runs - after a retry, an hour later - it may describe a state
// that no longer exists. Carrying the id forces the worker to load the current
// row inside its own tenant-bound transaction, which is also the only way it
// gets RLS applied to what it reads.
type ExpenseTransitionPayload struct {
	TenantID  uuid.UUID      `json:"tenant_id"`
	ExpenseID uuid.UUID      `json:"expense_id"`
	Action    expense.Action `json:"action"`
}

type BudgetThresholdPayload struct {
	TenantID     uuid.UUID  `json:"tenant_id"`
	DepartmentID *uuid.UUID `json:"department_id,omitempty"`
}

type BillingReconcilePayload struct {
	TenantID    uuid.UUID `json:"tenant_id"`
	CustomerRef string    `json:"customer_ref"`
}

// Client enqueues jobs. It is the only thing the services depend on, behind
// the service.Enqueuer interface, so a service can be tested without Redis.
type Client struct {
	inner *asynq.Client
}

func NewClient(redisOpt asynq.RedisConnOpt) *Client {
	return &Client{inner: asynq.NewClient(redisOpt)}
}

func (c *Client) Close() error { return c.inner.Close() }

func (c *Client) NotifyExpenseTransition(ctx context.Context, tenantID, expenseID uuid.UUID, action expense.Action) error {
	payload, err := json.Marshal(ExpenseTransitionPayload{
		TenantID: tenantID, ExpenseID: expenseID, Action: action,
	})
	if err != nil {
		return fmt.Errorf("encode transition payload: %w", err)
	}

	_, err = c.inner.EnqueueContext(ctx,
		asynq.NewTask(TaskExpenseTransition, payload),
		asynq.Queue(QueueCritical),
		asynq.MaxRetry(5),
		asynq.Timeout(30*time.Second),
		// Deduplicated on the claim and the action for a minute. A user
		// double-clicking Approve produces one transition and one 409, but a
		// retried HTTP request that succeeded both times upstream would
		// otherwise send two emails.
		asynq.TaskID(fmt.Sprintf("%s:%s:%s", TaskExpenseTransition, expenseID, action)),
		asynq.Retention(time.Hour),
	)
	if err != nil && isDuplicate(err) {
		return nil
	}
	return err
}

func (c *Client) CheckBudgetThreshold(ctx context.Context, tenantID uuid.UUID, departmentID *uuid.UUID) error {
	payload, err := json.Marshal(BudgetThresholdPayload{TenantID: tenantID, DepartmentID: departmentID})
	if err != nil {
		return fmt.Errorf("encode budget payload: %w", err)
	}

	// Deliberately delayed and deduplicated on the department for the window.
	// Ten claims approved in a batch would otherwise produce ten identical
	// "budget at 80%" alerts; this collapses them into one.
	_, err = c.inner.EnqueueContext(ctx,
		asynq.NewTask(TaskBudgetThreshold, payload),
		asynq.Queue(QueueDefault),
		asynq.MaxRetry(3),
		asynq.ProcessIn(2*time.Minute),
		asynq.TaskID(fmt.Sprintf("%s:%s:%s", TaskBudgetThreshold, tenantID, deref(departmentID))),
	)
	if err != nil && isDuplicate(err) {
		return nil
	}
	return err
}

func (c *Client) EnqueueReconcile(ctx context.Context, tenantID uuid.UUID, customerRef string) error {
	payload, err := json.Marshal(BillingReconcilePayload{TenantID: tenantID, CustomerRef: customerRef})
	if err != nil {
		return fmt.Errorf("encode reconcile payload: %w", err)
	}
	_, err = c.inner.EnqueueContext(ctx,
		asynq.NewTask(TaskBillingReconcile, payload),
		asynq.Queue(QueueLow),
		asynq.MaxRetry(3),
		asynq.Timeout(time.Minute),
	)
	if err != nil && isDuplicate(err) {
		return nil
	}
	return err
}

// isDuplicate reports whether an enqueue failed because the deduplication id
// is already present. That is a successful outcome - the job is already
// queued - and treating it as an error would make every caller handle it.
func isDuplicate(err error) bool {
	return err != nil && (errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask))
}

func deref(id *uuid.UUID) string {
	if id == nil {
		return "unassigned"
	}
	return id.String()
}
