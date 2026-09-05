package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/org"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/repository/postgres/gen"
)

// OrgRepository covers departments, budget envelopes and the customer's own
// vendor subscriptions.
type OrgRepository struct{}

func NewOrgRepository() *OrgRepository { return &OrgRepository{} }

// -----------------------------------------------------------------------------
// Departments
// -----------------------------------------------------------------------------

func (r *OrgRepository) CreateDepartment(ctx context.Context, tc *postgres.TenantConn, d org.DepartmentDraft) (*org.Department, error) {
	row, err := gen.New(tc).CreateDepartment(ctx, gen.CreateDepartmentParams{
		TenantID:   tc.TenantID(),
		Name:       d.Name,
		ParentID:   d.ParentID,
		HeadUserID: d.HeadUserID,
	})
	if err != nil {
		return nil, translate(err)
	}
	return toDomainDepartment(row), nil
}

func (r *OrgRepository) ListDepartments(ctx context.Context, tc *postgres.TenantConn, includeArchived bool) ([]*org.Department, error) {
	rows, err := gen.New(tc).ListDepartments(ctx, gen.ListDepartmentsParams{
		TenantID:        tc.TenantID(),
		IncludeArchived: includeArchived,
	})
	if err != nil {
		return nil, translate(err)
	}
	out := make([]*org.Department, len(rows))
	for i, row := range rows {
		out[i] = toDomainDepartment(row)
	}
	return out, nil
}

// GetDepartment loads one department. Removed once as dead code and restored
// here: the notification path needs the name to put in an email.
func (r *OrgRepository) GetDepartment(ctx context.Context, tc *postgres.TenantConn, id uuid.UUID) (*org.Department, error) {
	row, err := gen.New(tc).GetDepartment(ctx, gen.GetDepartmentParams{TenantID: tc.TenantID(), ID: id})
	if err != nil {
		return nil, translate(err)
	}
	return toDomainDepartment(row), nil
}

func (r *OrgRepository) UpdateDepartment(ctx context.Context, tc *postgres.TenantConn, id uuid.UUID, d org.DepartmentDraft) (*org.Department, error) {
	row, err := gen.New(tc).UpdateDepartment(ctx, gen.UpdateDepartmentParams{
		TenantID:   tc.TenantID(),
		ID:         id,
		Name:       d.Name,
		ParentID:   d.ParentID,
		HeadUserID: d.HeadUserID,
	})
	if err != nil {
		return nil, translate(err)
	}
	return toDomainDepartment(row), nil
}

// ArchiveDepartment retires a department without deleting it.
//
// Deleting is not offered: departments_parent_fk and expenses_department_fk are
// ON DELETE RESTRICT, so a department with any history cannot be removed
// anyway - and archiving keeps historical claims attributable, which is the
// whole point of having had a department.
func (r *OrgRepository) ArchiveDepartment(ctx context.Context, tc *postgres.TenantConn, id uuid.UUID) error {
	n, err := gen.New(tc).ArchiveDepartment(ctx, gen.ArchiveDepartmentParams{TenantID: tc.TenantID(), ID: id})
	if err != nil {
		return translate(err)
	}
	if n == 0 {
		return shared.ErrNotFound
	}
	return nil
}

func (r *OrgRepository) CountLiveDepartments(ctx context.Context, tc *postgres.TenantConn) (int, error) {
	n, err := gen.New(tc).CountLiveDepartments(ctx, tc.TenantID())
	return int(n), translate(err)
}

// -----------------------------------------------------------------------------
// Budgets
// -----------------------------------------------------------------------------

func (r *OrgRepository) CreateBudget(ctx context.Context, tc *postgres.TenantConn, b org.BudgetDraft) (*org.Budget, error) {
	row, err := gen.New(tc).CreateBudget(ctx, gen.CreateBudgetParams{
		TenantID:          tc.TenantID(),
		DepartmentID:      b.DepartmentID,
		PeriodStart:       b.PeriodStart,
		PeriodEnd:         b.PeriodEnd,
		AmountMinor:       b.Amount.Minor,
		Currency:          string(b.Amount.Currency),
		AlertThresholdBps: b.AlertThresholdBps,
	})
	if err != nil {
		return nil, translate(err)
	}
	return toDomainBudget(row), nil
}

func (r *OrgRepository) ListBudgets(ctx context.Context, tc *postgres.TenantConn, departmentID *uuid.UUID, on *time.Time) ([]*org.Budget, error) {
	rows, err := gen.New(tc).ListBudgets(ctx, gen.ListBudgetsParams{
		TenantID:     tc.TenantID(),
		DepartmentID: departmentID,
		OnDate:       on,
	})
	if err != nil {
		return nil, translate(err)
	}
	out := make([]*org.Budget, len(rows))
	for i, row := range rows {
		out[i] = toDomainBudget(row)
	}
	return out, nil
}

func (r *OrgRepository) UpdateBudget(ctx context.Context, tc *postgres.TenantConn, id uuid.UUID, b org.BudgetDraft) (*org.Budget, error) {
	row, err := gen.New(tc).UpdateBudget(ctx, gen.UpdateBudgetParams{
		TenantID:          tc.TenantID(),
		ID:                id,
		AmountMinor:       b.Amount.Minor,
		Currency:          string(b.Amount.Currency),
		AlertThresholdBps: b.AlertThresholdBps,
	})
	if err != nil {
		return nil, translate(err)
	}
	return toDomainBudget(row), nil
}

func (r *OrgRepository) DeleteBudget(ctx context.Context, tc *postgres.TenantConn, id uuid.UUID) error {
	n, err := gen.New(tc).DeleteBudget(ctx, gen.DeleteBudgetParams{TenantID: tc.TenantID(), ID: id})
	if err != nil {
		return translate(err)
	}
	if n == 0 {
		return shared.ErrNotFound
	}
	return nil
}

// -----------------------------------------------------------------------------
// Vendor subscriptions
// -----------------------------------------------------------------------------

func (r *OrgRepository) CreateVendorSubscription(ctx context.Context, tc *postgres.TenantConn, d org.VendorSubscriptionDraft) (*org.VendorSubscription, error) {
	row, err := gen.New(tc).CreateVendorSubscription(ctx, gen.CreateVendorSubscriptionParams{
		TenantID:          tc.TenantID(),
		Vendor:            d.Vendor,
		PlanName:          d.PlanName,
		DepartmentID:      d.DepartmentID,
		OwnerID:           d.OwnerID,
		AmountMinor:       d.Amount.Minor,
		Currency:          string(d.Amount.Currency),
		Cadence:           gen.BillingCadence(d.Cadence),
		NextChargeOn:      d.NextChargeOn,
		AutoCreateExpense: d.AutoCreateExpense,
	})
	if err != nil {
		return nil, translate(err)
	}
	return toDomainVendorSubscription(row), nil
}

// VendorSubscriptionRecord carries the department label alongside the entity so
// the listing needs no per-row lookup.
type VendorSubscriptionRecord struct {
	*org.VendorSubscription
	DepartmentName *string `json:"department_name,omitempty"`

	// AnnualisedMinor is what this costs per year. Computed here rather than
	// left to the dashboard, because the cadence arithmetic is a domain rule
	// and two clients would eventually disagree about it.
	AnnualisedMinor int64 `json:"annualised_minor"`
}

func (r *OrgRepository) ListVendorSubscriptions(ctx context.Context, tc *postgres.TenantConn, status *org.VendorStatus) ([]VendorSubscriptionRecord, error) {
	var filter *gen.VendorSubscriptionStatus
	if status != nil {
		v := gen.VendorSubscriptionStatus(*status)
		filter = &v
	}

	rows, err := gen.New(tc).ListVendorSubscriptions(ctx, gen.ListVendorSubscriptionsParams{
		TenantID: tc.TenantID(),
		Status:   filter,
	})
	if err != nil {
		return nil, translate(err)
	}

	out := make([]VendorSubscriptionRecord, len(rows))
	for i, row := range rows {
		sub := &org.VendorSubscription{
			ID:                row.ID,
			TenantID:          row.TenantID,
			Vendor:            row.Vendor,
			PlanName:          row.PlanName,
			DepartmentID:      row.DepartmentID,
			OwnerID:           row.OwnerID,
			Amount:            shared.Money{Minor: row.AmountMinor, Currency: currency(row.Currency)},
			Cadence:           org.Cadence(row.Cadence),
			Status:            org.VendorStatus(row.Status),
			NextChargeOn:      row.NextChargeOn,
			LastGeneratedOn:   row.LastGeneratedOn,
			AutoCreateExpense: row.AutoCreateExpense,
			CancelledAt:       row.CancelledAt,
			CreatedAt:         row.CreatedAt,
			UpdatedAt:         row.UpdatedAt,
		}
		out[i] = VendorSubscriptionRecord{
			VendorSubscription: sub,
			DepartmentName:     row.DepartmentName,
			AnnualisedMinor:    sub.AnnualisedMinor(),
		}
	}
	return out, nil
}

func (r *OrgRepository) GetVendorSubscription(ctx context.Context, tc *postgres.TenantConn, id uuid.UUID) (*org.VendorSubscription, error) {
	row, err := gen.New(tc).GetVendorSubscription(ctx, gen.GetVendorSubscriptionParams{TenantID: tc.TenantID(), ID: id})
	if err != nil {
		return nil, translate(err)
	}
	return toDomainVendorSubscription(row), nil
}

// UpdateVendorSubscription writes the whole row, including status.
//
// cancelled_at is derived from the status rather than accepted from the caller:
// vendor_subscriptions_cancelled_chk requires the two to agree, and letting a
// client send them separately means the constraint rejects perfectly reasonable
// requests that simply set one and not the other.
func (r *OrgRepository) UpdateVendorSubscription(
	ctx context.Context,
	tc *postgres.TenantConn,
	id uuid.UUID,
	d org.VendorSubscriptionDraft,
	status org.VendorStatus,
	existing *time.Time,
	now time.Time,
) (*org.VendorSubscription, error) {
	cancelledAt := existing
	switch {
	case status == org.VendorCancelled && cancelledAt == nil:
		cancelledAt = &now
	case status != org.VendorCancelled:
		cancelledAt = nil
	}

	row, err := gen.New(tc).UpdateVendorSubscription(ctx, gen.UpdateVendorSubscriptionParams{
		TenantID:          tc.TenantID(),
		ID:                id,
		Vendor:            d.Vendor,
		PlanName:          d.PlanName,
		DepartmentID:      d.DepartmentID,
		OwnerID:           d.OwnerID,
		AmountMinor:       d.Amount.Minor,
		Currency:          string(d.Amount.Currency),
		Cadence:           gen.BillingCadence(d.Cadence),
		NextChargeOn:      d.NextChargeOn,
		AutoCreateExpense: d.AutoCreateExpense,
		Status:            gen.VendorSubscriptionStatus(status),
		CancelledAt:       cancelledAt,
	})
	if err != nil {
		return nil, translate(err)
	}
	return toDomainVendorSubscription(row), nil
}

func (r *OrgRepository) CountActiveVendorSubscriptions(ctx context.Context, tc *postgres.TenantConn) (int, error) {
	n, err := gen.New(tc).CountActiveVendorSubscriptions(ctx, tc.TenantID())
	return int(n), translate(err)
}

func (r *OrgRepository) CountActiveMembers(ctx context.Context, tc *postgres.TenantConn) (int, error) {
	n, err := gen.New(tc).CountActiveMembers(ctx, tc.TenantID())
	return int(n), translate(err)
}

// -----------------------------------------------------------------------------
// Mapping
// -----------------------------------------------------------------------------

func toDomainDepartment(row gen.Department) *org.Department {
	return &org.Department{
		ID:         row.ID,
		TenantID:   row.TenantID,
		Name:       row.Name,
		ParentID:   row.ParentID,
		HeadUserID: row.HeadUserID,
		ArchivedAt: row.ArchivedAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

func toDomainBudget(row gen.Budget) *org.Budget {
	return &org.Budget{
		ID:                row.ID,
		TenantID:          row.TenantID,
		DepartmentID:      row.DepartmentID,
		PeriodStart:       row.PeriodStart,
		PeriodEnd:         row.PeriodEnd,
		Amount:            shared.Money{Minor: row.AmountMinor, Currency: currency(row.Currency)},
		AlertThresholdBps: row.AlertThresholdBps,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}

func toDomainVendorSubscription(row gen.VendorSubscription) *org.VendorSubscription {
	return &org.VendorSubscription{
		ID:                row.ID,
		TenantID:          row.TenantID,
		Vendor:            row.Vendor,
		PlanName:          row.PlanName,
		DepartmentID:      row.DepartmentID,
		OwnerID:           row.OwnerID,
		Amount:            shared.Money{Minor: row.AmountMinor, Currency: currency(row.Currency)},
		Cadence:           org.Cadence(row.Cadence),
		Status:            org.VendorStatus(row.Status),
		NextChargeOn:      row.NextChargeOn,
		LastGeneratedOn:   row.LastGeneratedOn,
		AutoCreateExpense: row.AutoCreateExpense,
		CancelledAt:       row.CancelledAt,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}
