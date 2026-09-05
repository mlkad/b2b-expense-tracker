// Package org models the structures a tenant organises its spending around:
// departments, the budget envelopes attached to them, and the recurring vendor
// charges it tracks.
//
// Like the other domain packages it performs no I/O and imports nothing from
// the rest of the project.
package org

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
)

// -----------------------------------------------------------------------------
// Department
// -----------------------------------------------------------------------------

type Department struct {
	ID       uuid.UUID `json:"id"`
	TenantID uuid.UUID `json:"tenant_id"`
	Name     string    `json:"name"`

	ParentID   *uuid.UUID `json:"parent_id,omitempty"`
	HeadUserID *uuid.UUID `json:"head_user_id,omitempty"`

	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// DepartmentDraft is the writable surface. Separate from the entity so a
// handler cannot bind JSON straight onto something carrying TenantID and
// ArchivedAt.
type DepartmentDraft struct {
	Name       string
	ParentID   *uuid.UUID
	HeadUserID *uuid.UUID
}

func (d *DepartmentDraft) Validate() error {
	var v shared.Validator

	// Normalised in place: the repository persists this value, so trimming a
	// copy leaves " Engineering" to be written verbatim and caught one round
	// trip later by departments_name_len_chk.
	d.Name = strings.TrimSpace(d.Name)

	if n := len([]rune(d.Name)); n == 0 || n > 120 {
		v.Add("name", "must be between 1 and 120 characters")
	}
	return v.Err()
}

func (d *Department) Archived() bool { return d.ArchivedAt != nil }

// -----------------------------------------------------------------------------
// Budget
// -----------------------------------------------------------------------------

type Budget struct {
	ID       uuid.UUID `json:"id"`
	TenantID uuid.UUID `json:"tenant_id"`

	// DepartmentID nil is a tenant-wide envelope.
	DepartmentID *uuid.UUID `json:"department_id,omitempty"`

	// Inclusive on both ends, and dates rather than timestamps: a fiscal
	// quarter is a calendar fact, and giving it a time zone means the same
	// quarter starts at different instants for different readers.
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`

	Amount shared.Money `json:"amount"`

	// AlertThresholdBps is the fraction of the envelope at which the alerting
	// worker warns, in basis points. Stored per budget because a marketing
	// budget and a payroll budget do not want the same threshold.
	AlertThresholdBps int32 `json:"alert_threshold_bps"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type BudgetDraft struct {
	DepartmentID      *uuid.UUID
	PeriodStart       time.Time
	PeriodEnd         time.Time
	Amount            shared.Money
	AlertThresholdBps int32
}

// DefaultAlertThresholdBps matches the column default: warn at 80% consumed.
const DefaultAlertThresholdBps int32 = 8000

// MaxBudgetPeriodDays bounds how long one envelope may run.
//
// Two years, because a budget covering a decade makes the alert threshold
// meaningless - 80% of a ten-year envelope is reached somewhere in year eight,
// long after anyone could have acted on it.
const MaxBudgetPeriodDays = 366 * 2

func (d *BudgetDraft) Validate(now time.Time) error {
	var v shared.Validator

	if d.AlertThresholdBps == 0 {
		d.AlertThresholdBps = DefaultAlertThresholdBps
	}

	// Truncated to whole days on the way in, so a client sending an ISO
	// timestamp does not produce an envelope that starts at noon.
	d.PeriodStart = d.PeriodStart.UTC().Truncate(24 * time.Hour)
	d.PeriodEnd = d.PeriodEnd.UTC().Truncate(24 * time.Hour)

	switch {
	case d.PeriodStart.IsZero():
		v.Add("period_start", "is required")
	case d.PeriodEnd.IsZero():
		v.Add("period_end", "is required")
	case d.PeriodEnd.Before(d.PeriodStart):
		v.Add("period_end", "must not be before the start of the period")
	default:
		if days := d.PeriodEnd.Sub(d.PeriodStart).Hours() / 24; days > MaxBudgetPeriodDays {
			v.Addf("period_end", "must be at most %d days after the start", MaxBudgetPeriodDays)
		}
	}

	if !d.Amount.Currency.Valid() {
		v.Add("currency", "must be a three-letter ISO 4217 code")
	}
	if !d.Amount.IsPositive() {
		v.Add("amount_minor", "must be greater than zero")
	}
	if d.AlertThresholdBps < 1 || d.AlertThresholdBps > 10000 {
		v.Add("alert_threshold_bps", "must be between 1 and 10000 basis points")
	}
	return v.Err()
}

// -----------------------------------------------------------------------------
// Vendor subscription
// -----------------------------------------------------------------------------

// Cadence mirrors the billing_cadence enum.
type Cadence string

const (
	CadenceWeekly    Cadence = "weekly"
	CadenceMonthly   Cadence = "monthly"
	CadenceQuarterly Cadence = "quarterly"
	CadenceAnnual    Cadence = "annual"
)

var AllCadences = []Cadence{CadenceWeekly, CadenceMonthly, CadenceQuarterly, CadenceAnnual}

func (c Cadence) Valid() bool {
	for _, known := range AllCadences {
		if c == known {
			return true
		}
	}
	return false
}

// VendorStatus mirrors vendor_subscription_status.
type VendorStatus string

const (
	VendorActive    VendorStatus = "active"
	VendorPaused    VendorStatus = "paused"
	VendorCancelled VendorStatus = "cancelled"
)

func (s VendorStatus) Valid() bool {
	switch s {
	case VendorActive, VendorPaused, VendorCancelled:
		return true
	}
	return false
}

// VendorSubscription is a recurring charge the customer pays to somebody else -
// their Figma seat, their AWS bill. Not to be confused with the tenant's own
// subscription to this product, which lives in internal/domain/billing.
type VendorSubscription struct {
	ID       uuid.UUID `json:"id"`
	TenantID uuid.UUID `json:"tenant_id"`

	Vendor   string  `json:"vendor"`
	PlanName *string `json:"plan_name,omitempty"`

	DepartmentID *uuid.UUID `json:"department_id,omitempty"`
	OwnerID      *uuid.UUID `json:"owner_id,omitempty"`

	Amount  shared.Money `json:"amount"`
	Cadence Cadence      `json:"cadence"`
	Status  VendorStatus `json:"status"`

	NextChargeOn    time.Time  `json:"next_charge_on"`
	LastGeneratedOn *time.Time `json:"last_generated_on,omitempty"`

	// AutoCreateExpense false means "track the cost, but do not file a claim" -
	// a subscription paid by corporate card that reconciles elsewhere.
	AutoCreateExpense bool `json:"auto_create_expense"`

	CancelledAt *time.Time `json:"cancelled_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type VendorSubscriptionDraft struct {
	Vendor            string
	PlanName          *string
	DepartmentID      *uuid.UUID
	OwnerID           *uuid.UUID
	Amount            shared.Money
	Cadence           Cadence
	NextChargeOn      time.Time
	AutoCreateExpense bool
}

func (d *VendorSubscriptionDraft) Validate(now time.Time) error {
	var v shared.Validator

	d.Vendor = strings.TrimSpace(d.Vendor)
	if d.PlanName != nil {
		trimmed := strings.TrimSpace(*d.PlanName)
		if trimmed == "" {
			d.PlanName = nil
		} else {
			d.PlanName = &trimmed
		}
	}

	if n := len([]rune(d.Vendor)); n == 0 || n > 200 {
		v.Add("vendor", "must be between 1 and 200 characters")
	}
	if !d.Cadence.Valid() {
		v.Add("cadence", "must be one of weekly, monthly, quarterly or annual")
	}
	if !d.Amount.Currency.Valid() {
		v.Add("currency", "must be a three-letter ISO 4217 code")
	}
	if !d.Amount.IsPositive() {
		v.Add("amount_minor", "must be greater than zero")
	}

	d.NextChargeOn = d.NextChargeOn.UTC().Truncate(24 * time.Hour)
	switch {
	case d.NextChargeOn.IsZero():
		v.Add("next_charge_on", "is required")
	case d.NextChargeOn.Before(now.UTC().Truncate(24 * time.Hour)):
		// A charge date in the past would make the sweep materialise a claim
		// immediately, and then another for every period since - which is how
		// a typo becomes fifty draft claims.
		v.Add("next_charge_on", "must not be in the past")
	}
	return v.Err()
}

// AnnualisedMinor is what this subscription costs per year, which is the number
// a customer comparing vendors actually wants. Weekly is multiplied by 52
// rather than by 365/7: subscriptions are billed on a weekday cadence, and 52
// is what the vendor charges for.
func (s *VendorSubscription) AnnualisedMinor() int64 {
	switch s.Cadence {
	case CadenceWeekly:
		return s.Amount.Minor * 52
	case CadenceMonthly:
		return s.Amount.Minor * 12
	case CadenceQuarterly:
		return s.Amount.Minor * 4
	case CadenceAnnual:
		return s.Amount.Minor
	default:
		return 0
	}
}
