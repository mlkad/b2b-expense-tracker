package expense

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
)

// Category mirrors the expense_category enum.
type Category string

const (
	CategoryTravel        Category = "travel"
	CategoryMeals         Category = "meals"
	CategoryAccommodation Category = "accommodation"
	CategorySoftware      Category = "software"
	CategoryHardware      Category = "hardware"
	CategoryMarketing     Category = "marketing"
	CategoryTraining      Category = "training"
	CategoryOffice        Category = "office"
	CategoryContractor    Category = "contractor"
	CategoryOther         Category = "other"
)

var AllCategories = []Category{
	CategoryTravel, CategoryMeals, CategoryAccommodation, CategorySoftware,
	CategoryHardware, CategoryMarketing, CategoryTraining, CategoryOffice,
	CategoryContractor, CategoryOther,
}

func (c Category) Valid() bool {
	for _, known := range AllCategories {
		if c == known {
			return true
		}
	}
	return false
}

// Expense is a claim for money spent.
//
// The status field is not exported for writing by anything outside this
// package's methods - it is exported for serialisation and for the repository
// to hydrate, and every mutation path in the package goes through Apply. There
// is no SetStatus.
type Expense struct {
	ID       uuid.UUID `json:"id"`
	TenantID uuid.UUID `json:"tenant_id"`

	// SubmitterID is a membership id, not a user id. Authority is a property of
	// someone's place in this tenant, and the same person in two tenants is two
	// submitters.
	SubmitterID  uuid.UUID  `json:"submitter_id"`
	DepartmentID *uuid.UUID `json:"department_id,omitempty"`

	Status   Status   `json:"status"`
	Category Category `json:"category"`

	Amount shared.Money `json:"amount"`

	Merchant    string  `json:"merchant"`
	Description *string `json:"description,omitempty"`

	// SpentAt is the date on the receipt. Reporting periods key on this rather
	// than on CreatedAt, because a claim filed in April for a March dinner
	// belongs to March.
	SpentAt time.Time `json:"spent_at"`

	SubmittedAt  *time.Time `json:"submitted_at,omitempty"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
	DecidedBy    *uuid.UUID `json:"decided_by,omitempty"`
	DecisionNote *string    `json:"decision_note,omitempty"`
	PaidAt       *time.Time `json:"paid_at,omitempty"`
	PaymentRef   *string    `json:"payment_ref,omitempty"`

	// Revision is the draft iteration. It increments when a rejected claim is
	// revised, so an approver looking at revision 2 knows there was a decision
	// on revision 1 and can find it in the ledger.
	Revision int32 `json:"revision"`

	// Version is the optimistic concurrency token. Every Apply bumps it, and
	// the repository's UPDATE carries `WHERE version = $expected`. Two
	// approvers deciding at the same moment produce one decision and one 409.
	Version int32 `json:"version"`

	SourceSubscriptionID *uuid.UUID `json:"source_subscription_id,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Draft describes a new or edited claim. It is the only way to get field
// values into an Expense, which keeps the set of writable fields visible in
// one place and stops a future handler from binding JSON straight onto the
// entity and setting Status along with everything else.
type Draft struct {
	DepartmentID *uuid.UUID
	Category     Category
	Amount       shared.Money
	Merchant     string
	Description  *string
	SpentAt      time.Time
}

// maxFutureSkew matches expenses_spent_at_chk. One day absorbs a client whose
// clock is in a time zone ahead of the server's; anything more is a claim for
// money that has not been spent.
const maxFutureSkew = 24 * time.Hour

func (d *Draft) validate(now time.Time) error {
	var v shared.Validator

	d.Merchant = strings.TrimSpace(d.Merchant)
	if d.Description != nil {
		trimmed := strings.TrimSpace(*d.Description)
		if trimmed == "" {
			d.Description = nil
		} else {
			d.Description = &trimmed
		}
	}

	if n := len(d.Merchant); n == 0 || n > 200 {
		v.Add("merchant", "must be between 1 and 200 characters")
	}
	if d.Description != nil && len(*d.Description) > 4000 {
		v.Add("description", "must be at most 4000 characters")
	}
	if !d.Category.Valid() {
		v.Add("category", "is not a known category")
	}
	if !d.Amount.Currency.Valid() {
		v.Add("currency", "must be a three-letter ISO 4217 code")
	}
	if !d.Amount.IsPositive() {
		v.Add("amount_minor", "must be greater than zero")
	}
	if d.SpentAt.IsZero() {
		v.Add("spent_at", "is required")
	} else if d.SpentAt.After(now.Add(maxFutureSkew)) {
		v.Add("spent_at", "must not be in the future")
	}
	return v.Err()
}

// New builds a claim in draft. It is the only constructor: an Expense that did
// not start as a draft cannot be created, which is what makes the state
// machine's entry point singular.
func New(tenantID, submitterID uuid.UUID, d Draft, now time.Time) (*Expense, Event, error) {
	if err := d.validate(now); err != nil {
		return nil, Event{}, err
	}

	e := &Expense{
		ID:           uuid.New(),
		TenantID:     tenantID,
		SubmitterID:  submitterID,
		DepartmentID: d.DepartmentID,
		Status:       StatusDraft,
		Category:     d.Category,
		Amount:       d.Amount,
		Merchant:     d.Merchant,
		Description:  d.Description,
		SpentAt:      d.SpentAt.UTC().Truncate(24 * time.Hour),
		Revision:     1,
		Version:      1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	return e, Event{
		TenantID:   tenantID,
		ExpenseID:  e.ID,
		Action:     EventCreated,
		FromStatus: nil,
		ToStatus:   StatusDraft,
		ActorID:    &submitterID,
		Amount:     e.Amount,
		Revision:   e.Revision,
		OccurredAt: now,
	}, nil
}

// Edit replaces the mutable fields of a draft.
//
// The status check is first and is not negotiable. Everything downstream -
// the budget rollup, the approver's decision, the audit trail - assumes that
// what was approved is what was submitted, and an edit after submission
// breaks that assumption silently rather than loudly.
func (e *Expense) Edit(d Draft, actorMembershipID uuid.UUID, now time.Time) (Event, error) {
	if !e.Status.Editable() {
		return Event{}, &TransitionError{
			From:   e.Status,
			Action: "edit",
			Reason: "only a draft may be edited; submit a revision instead",
		}
	}
	if err := d.validate(now); err != nil {
		return Event{}, err
	}

	e.DepartmentID = d.DepartmentID
	e.Category = d.Category
	e.Amount = d.Amount
	e.Merchant = d.Merchant
	e.Description = d.Description
	e.SpentAt = d.SpentAt.UTC().Truncate(24 * time.Hour)
	e.UpdatedAt = now
	e.Version++

	return Event{
		TenantID:   e.TenantID,
		ExpenseID:  e.ID,
		Action:     EventUpdated,
		FromStatus: &e.Status,
		ToStatus:   e.Status,
		ActorID:    &actorMembershipID,
		Amount:     e.Amount,
		Revision:   e.Revision,
		OccurredAt: now,
	}, nil
}

// Event is one row of the append-only ledger in expense_events.
//
// Amount and Revision are copied rather than joined back to the claim on read.
// An audit row saying "approved" without saying what was approved is useless
// the moment the claim is revised, and a join would report today's amount for
// a decision made against last month's.
type Event struct {
	TenantID   uuid.UUID
	ExpenseID  uuid.UUID
	Action     EventAction
	FromStatus *Status
	ToStatus   Status
	ActorID    *uuid.UUID
	Reason     *string
	Amount     shared.Money
	Revision   int32
	Metadata   map[string]any
	OccurredAt time.Time
}
