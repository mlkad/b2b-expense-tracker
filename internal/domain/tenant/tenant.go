package tenant

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
)

type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
	StatusCancelled Status = "cancelled"
)

// Tenant is one customer organisation.
type Tenant struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
	Name string    `json:"name"`

	Status          Status          `json:"status"`
	DefaultCurrency shared.Currency `json:"default_currency"`

	// BillingCustomerRef is this tenant's identity in the payment gateway
	// (project #1). Nil until the first checkout. It is never returned to a
	// client: the dashboard reaches billing through this service, which holds
	// the gateway credential, so the reference has no reason to leave.
	BillingCustomerRef *string `json:"-"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// slugPattern is enforced identically by tenants_slug_format_chk. Checking it
// here saves a round trip and produces a field error instead of a constraint
// violation; the database remains the authority.
func validSlug(s string) bool {
	if len(s) < 3 || len(s) > 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isLower := c >= 'a' && c <= 'z'
		isDigit := c >= '0' && c <= '9'
		if !isLower && !isDigit && c != '-' {
			return false
		}
	}
	return s[0] != '-' && s[len(s)-1] != '-'
}

func (t *Tenant) Validate() error {
	var v shared.Validator

	// Normalise in place rather than validating a copy: the repository
	// persists t.Slug, so trimming a copy leaves " acme" to be written
	// verbatim and caught one round trip later by the CHECK constraint.
	t.Slug = strings.ToLower(strings.TrimSpace(t.Slug))
	t.Name = strings.TrimSpace(t.Name)

	if !validSlug(t.Slug) {
		v.Add("slug", "must be 3-40 characters of lowercase letters, digits or hyphens, not starting or ending with a hyphen")
	}
	if n := len(t.Name); n == 0 || n > 200 {
		v.Add("name", "must be between 1 and 200 characters")
	}
	if !t.DefaultCurrency.Valid() {
		v.Add("default_currency", "must be a three-letter ISO 4217 code")
	}
	return v.Err()
}

// IsOperational reports whether the tenant may use the product at all.
// Suspension is what a failed subscription eventually leads to; it leaves the
// data readable through support tooling but closes the API.
func (t *Tenant) IsOperational() bool { return t.Status == StatusActive }

// MembershipStatus mirrors the membership_status enum.
type MembershipStatus string

const (
	MembershipInvited   MembershipStatus = "invited"
	MembershipActive    MembershipStatus = "active"
	MembershipSuspended MembershipStatus = "suspended"
)

// Membership joins a global user to a tenant with a role.
type Membership struct {
	ID       uuid.UUID `json:"id"`
	TenantID uuid.UUID `json:"tenant_id"`
	UserID   uuid.UUID `json:"user_id"`

	Role   Role             `json:"role"`
	Status MembershipStatus `json:"status"`

	// ApprovalLimitMinor overrides the role default. Nil means "use the role
	// default", which is resolved by Actor.ApprovalCeilingMinor so the default
	// can be changed without a data migration.
	ApprovalLimitMinor *int64 `json:"approval_limit_minor,omitempty"`

	// DepartmentID scopes a manager's authority. Nil is tenant-wide.
	DepartmentID *uuid.UUID `json:"department_id,omitempty"`

	InvitedBy *uuid.UUID `json:"invited_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Actor is the authenticated caller resolved against one tenant: the subject
// of every authorisation decision in the service layer.
//
// It is built once per request by the authentication middleware, from the
// database rather than from the token. The token asserts which user and which
// tenant; the role, the status and the limits are read fresh, because a token
// issued before a demotion would otherwise keep its old authority until it
// expired. That is a fifteen-minute window in which a removed employee can
// still approve payments, and it costs one indexed lookup to close.
type Actor struct {
	TenantID     uuid.UUID
	UserID       uuid.UUID
	MembershipID uuid.UUID

	Role   Role
	Status MembershipStatus

	DepartmentID       *uuid.UUID
	ApprovalLimitMinor *int64

	// TenantStatus is carried so a suspended tenant is refused by the same
	// check that refuses a suspended member, rather than by a separate one
	// somebody forgets to call.
	TenantStatus Status
}

// Active reports whether the actor may do anything at all.
func (a Actor) Active() bool {
	return a.Status == MembershipActive && a.TenantStatus == StatusActive
}

// Can answers the route-level question: does this actor hold this capability?
//
// It is necessary and not sufficient for anything that touches a specific
// expense. A manager holds PermExpenseApprove for every claim in the tenant as
// far as this method is concerned; CanApprove narrows that to the ones in
// their department and under their ceiling.
func (a Actor) Can(p Permission) bool {
	if !a.Active() {
		return false
	}
	return a.Role.Allows(p)
}

// ApprovalCeilingMinor resolves the effective limit: the membership override
// if present, otherwise the role default. A negative value means unlimited.
func (a Actor) ApprovalCeilingMinor() int64 {
	if a.ApprovalLimitMinor != nil {
		return *a.ApprovalLimitMinor
	}
	return a.Role.DefaultApprovalLimitMinor()
}

// WithinApprovalLimit reports whether the actor may decide on this amount.
func (a Actor) WithinApprovalLimit(amount shared.Money) bool {
	ceiling := a.ApprovalCeilingMinor()
	if ceiling < 0 {
		return true
	}
	return amount.Minor <= ceiling
}

// GovernsDepartment reports whether a department falls inside the actor's
// scope. A nil DepartmentID on the actor means tenant-wide authority; a nil
// department on the resource means it is unassigned, which only a tenant-wide
// actor may act on.
func (a Actor) GovernsDepartment(department *uuid.UUID) bool {
	if a.DepartmentID == nil {
		return true
	}
	if department == nil {
		return false
	}
	return *a.DepartmentID == *department
}

// SameMembership reports whether the actor is the given membership, which is
// how "your own claim" is decided everywhere.
func (a Actor) SameMembership(id uuid.UUID) bool { return a.MembershipID == id }
