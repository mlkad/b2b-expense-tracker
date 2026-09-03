package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/repository/postgres/gen"
)

type BudgetRepository struct{}

func NewBudgetRepository() *BudgetRepository { return &BudgetRepository{} }

// Consumption is one budget envelope and what has been committed against it.
type Consumption struct {
	BudgetID          uuid.UUID
	DepartmentID      *uuid.UUID
	DepartmentName    *string
	PeriodStart       time.Time
	PeriodEnd         time.Time
	Budget            shared.Money
	Consumed          shared.Money
	ClaimCount        int64
	AlertThresholdBps int32
}

// Remaining is what is left. It can be negative: a budget can be overspent,
// and reporting a floor of zero would hide exactly the situation the number
// exists to surface.
func (c Consumption) Remaining() shared.Money {
	return shared.Money{Minor: c.Budget.Minor - c.Consumed.Minor, Currency: c.Budget.Currency}
}

// UsageBps is consumption as basis points of the envelope, which is the unit
// alert_threshold_bps is stored in - so the comparison is integer against
// integer with no rounding in between.
func (c Consumption) UsageBps() int64 {
	if c.Budget.Minor == 0 {
		return 0
	}
	return c.Consumed.Minor * 10000 / c.Budget.Minor
}

func (c Consumption) BreachesThreshold() bool {
	return c.UsageBps() >= int64(c.AlertThresholdBps)
}

// Consumption reports every envelope in effect on a date, with what has been
// committed against it.
func (r *BudgetRepository) Consumption(ctx context.Context, tc *postgres.TenantConn, on *time.Time) ([]Consumption, error) {
	rows, err := gen.New(tc).BudgetConsumption(ctx, gen.BudgetConsumptionParams{
		TenantID: tc.TenantID(),
		OnDate:   on,
	})
	if err != nil {
		return nil, translate(err)
	}

	out := make([]Consumption, len(rows))
	for i, row := range rows {
		cur := currency(row.Currency)
		out[i] = Consumption{
			BudgetID:          row.BudgetID,
			DepartmentID:      row.DepartmentID,
			DepartmentName:    row.DepartmentName,
			PeriodStart:       row.PeriodStart,
			PeriodEnd:         row.PeriodEnd,
			Budget:            shared.Money{Minor: row.BudgetMinor, Currency: cur},
			Consumed:          shared.Money{Minor: row.ConsumedMinor, Currency: cur},
			ClaimCount:        row.ClaimCount,
			AlertThresholdBps: row.AlertThresholdBps,
		}
	}
	return out, nil
}

// DueSubscription is a recurring vendor charge the sweep has claimed.
type DueSubscription struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	Vendor       string
	PlanName     *string
	DepartmentID *uuid.UUID
	OwnerID      *uuid.UUID
	Amount       shared.Money
	Cadence      string
	NextChargeOn time.Time
}

// ClaimDue takes a batch of recurring charges that are due.
//
// It runs in a system transaction because it crosses tenants - a sweep has no
// single tenant to bind to - and the SELECT holds FOR UPDATE SKIP LOCKED, so
// two workers running the sweep at once take disjoint batches instead of
// racing for the same rows. The caller must do its work and advance each row
// inside the same transaction, or the locks are released and the batch is
// claimed again.
func (r *BudgetRepository) ClaimDue(ctx context.Context, tc *postgres.TenantConn, dueOn time.Time, batch int32) ([]DueSubscription, error) {
	rows, err := gen.New(tc).ClaimDueVendorSubscriptions(ctx, gen.ClaimDueVendorSubscriptionsParams{
		DueOn:     dueOn,
		BatchSize: batch,
	})
	if err != nil {
		return nil, translate(err)
	}

	out := make([]DueSubscription, len(rows))
	for i, row := range rows {
		out[i] = DueSubscription{
			ID:           row.ID,
			TenantID:     row.TenantID,
			Vendor:       row.Vendor,
			PlanName:     row.PlanName,
			DepartmentID: row.DepartmentID,
			OwnerID:      row.OwnerID,
			Amount:       shared.Money{Minor: row.AmountMinor, Currency: currency(row.Currency)},
			Cadence:      string(row.Cadence),
			NextChargeOn: row.NextChargeOn,
		}
	}
	return out, nil
}

// Advance moves a subscription to its next charge date.
func (r *BudgetRepository) Advance(ctx context.Context, tc *postgres.TenantConn, id uuid.UUID, next time.Time) error {
	_, err := gen.New(tc).AdvanceVendorSubscription(ctx, gen.AdvanceVendorSubscriptionParams{
		TenantID:     tc.TenantID(),
		ID:           id,
		NextChargeOn: next,
	})
	return translate(err)
}

// NextChargeDate advances a date by one cadence.
//
// AddDate normalises overflow, so a monthly charge dated the 31st becomes the
// 1st or 2nd of the following month rather than failing. That is the standard
// Go behaviour and it is the behaviour wanted here: a subscription billed on
// the 31st is billed on the last possible day, and drifting forward by a day
// or two in February is better than skipping a month.
func NextChargeDate(from time.Time, cadence string) time.Time {
	switch cadence {
	case "weekly":
		return from.AddDate(0, 0, 7)
	case "monthly":
		return from.AddDate(0, 1, 0)
	case "quarterly":
		return from.AddDate(0, 3, 0)
	case "annual":
		return from.AddDate(1, 0, 0)
	default:
		return from.AddDate(0, 1, 0)
	}
}
