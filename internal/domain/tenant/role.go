// Package tenant models who a caller is inside an organisation and what that
// permits. It is the authority on authorisation decisions; the HTTP layer and
// the services ask it, and neither one reimplements the matrix.
package tenant

import (
	"fmt"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
)

// Role mirrors the membership_role enum in migration 00002. The two must not
// drift: the enum's declaration order is the same as Rank below, and both are
// relied on.
type Role string

const (
	RoleOwner   Role = "owner"
	RoleAdmin   Role = "admin"
	RoleFinance Role = "finance"
	RoleManager Role = "manager"
	RoleMember  Role = "member"
	RoleViewer  Role = "viewer"
)

// AllRoles is ordered most to least privileged. Tests iterate it to assert
// that the permission matrix is monotonic, which is the property that stops a
// future edit from accidentally giving a viewer something a manager lacks.
var AllRoles = []Role{RoleOwner, RoleAdmin, RoleFinance, RoleManager, RoleMember, RoleViewer}

func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleFinance, RoleManager, RoleMember, RoleViewer:
		return true
	}
	return false
}

// Rank orders roles for comparison, lower being more privileged. It exists for
// "can this actor modify that member" questions, where the rule is that you
// may only administer someone strictly below you.
//
// Rank is deliberately NOT how permissions are decided. Seniority and
// capability are different things here: finance outranks a manager but cannot
// approve a claim, and a manager cannot settle one. A single ordering would
// force one of those two to be wrong, so permissions live in the matrix below
// and rank is used only where an ordering genuinely applies.
func (r Role) Rank() int {
	for i, candidate := range AllRoles {
		if candidate == r {
			return i
		}
	}
	return len(AllRoles) // unknown roles sort last, i.e. least privileged
}

// OutranksStrictly reports whether r may administer other.
func (r Role) OutranksStrictly(other Role) bool { return r.Rank() < other.Rank() }

func ParseRole(s string) (Role, error) {
	role := Role(s)
	if !role.Valid() {
		return "", shared.FieldError{Field: "role", Detail: fmt.Sprintf("%q is not a known role", s)}
	}
	return role, nil
}

// Permission is a single capability. The names are namespaced by resource so
// that a listing of the matrix reads as a table.
type Permission string

const (
	PermExpenseCreate    Permission = "expense:create"     // file a claim of one's own
	PermExpenseSubmit    Permission = "expense:submit"     // send one's own draft for approval
	PermExpenseApprove   Permission = "expense:approve"    // approve or reject someone else's
	PermExpensePay       Permission = "expense:pay"        // settle an approved claim
	PermExpenseReadOwn   Permission = "expense:read:own"   // see one's own claims
	PermExpenseReadTeam  Permission = "expense:read:team"  // see the claims of one's department
	PermExpenseReadAll   Permission = "expense:read:all"   // see every claim in the tenant
	PermExpenseDeleteOwn Permission = "expense:delete:own" // discard one's own draft
	PermDepartmentManage Permission = "department:manage"  //
	PermBudgetManage     Permission = "budget:manage"      //
	PermMemberManage     Permission = "member:manage"      // invite, change role, suspend
	PermVendorSubManage  Permission = "vendor_sub:manage"  // the customer's own recurring spend
	PermReportExport     Permission = "report:export"      // the streaming export endpoints
	PermBillingManage    Permission = "billing:manage"     // checkout, portal, plan changes
	PermTenantManage     Permission = "tenant:manage"      // rename, settings, lifecycle
	PermAuditRead        Permission = "audit:read"         // the expense event ledger
)

// permissions is the whole authorisation model.
//
// It is a literal rather than a set of `if role == ...` branches for one
// reason: a table can be read end to end in review, and a reviewer asked
// "which roles can settle a payment" can answer it by looking rather than by
// tracing. Adding a permission means adding a row to every role that should
// have it, which is more typing and exactly the amount of deliberation the
// change deserves.
//
// Two rules are visible in the shape of it and are asserted by tests:
//
//   - Approval and payment are never held by the same role. A manager approves
//     and cannot pay; finance pays and cannot approve. That is separation of
//     duties, and it is the control that makes a single compromised account
//     unable to move money on its own.
//   - Owner is the only role with billing, because billing is the only action
//     whose consequence is a charge to the company.
var permissions = map[Role]map[Permission]struct{}{
	RoleOwner: setOf(
		PermExpenseCreate, PermExpenseSubmit, PermExpenseApprove, PermExpensePay,
		PermExpenseReadOwn, PermExpenseReadTeam, PermExpenseReadAll, PermExpenseDeleteOwn,
		PermDepartmentManage, PermBudgetManage, PermMemberManage, PermVendorSubManage,
		PermReportExport, PermBillingManage, PermTenantManage, PermAuditRead,
	),
	RoleAdmin: setOf(
		PermExpenseCreate, PermExpenseSubmit, PermExpenseApprove,
		PermExpenseReadOwn, PermExpenseReadTeam, PermExpenseReadAll, PermExpenseDeleteOwn,
		PermDepartmentManage, PermBudgetManage, PermMemberManage, PermVendorSubManage,
		PermReportExport, PermTenantManage, PermAuditRead,
		// No PermExpensePay: an admin who can both approve and settle can move
		// money end to end without a second person.
		// No PermBillingManage: changing the plan is the owner's call.
	),
	RoleFinance: setOf(
		PermExpenseCreate, PermExpenseSubmit, PermExpensePay,
		PermExpenseReadOwn, PermExpenseReadTeam, PermExpenseReadAll, PermExpenseDeleteOwn,
		PermBudgetManage, PermVendorSubManage, PermReportExport, PermAuditRead,
		// No PermExpenseApprove: the other half of the separation.
	),
	RoleManager: setOf(
		PermExpenseCreate, PermExpenseSubmit, PermExpenseApprove,
		PermExpenseReadOwn, PermExpenseReadTeam, PermExpenseDeleteOwn,
		PermReportExport,
		// Scope, not just capability: a manager's approve and read:team are
		// bounded to their department by Actor.CanSee, and their approve is
		// bounded by amount. Both checks live on Actor because the answer
		// depends on the specific claim, not on the role alone.
	),
	RoleMember: setOf(
		PermExpenseCreate, PermExpenseSubmit, PermExpenseReadOwn, PermExpenseDeleteOwn,
	),
	RoleViewer: setOf(
		PermExpenseReadOwn, PermExpenseReadTeam,
	),
}

func setOf(ps ...Permission) map[Permission]struct{} {
	m := make(map[Permission]struct{}, len(ps))
	for _, p := range ps {
		m[p] = struct{}{}
	}
	return m
}

// Allows reports whether the role carries a permission at all, ignoring the
// per-claim scope checks. Callers that touch a specific expense must use the
// Actor methods instead; this answers only the route-level question.
func (r Role) Allows(p Permission) bool {
	_, ok := permissions[r][p]
	return ok
}

// Permissions returns the role's capabilities, for the /me endpoint. The
// dashboard uses it to hide controls the caller cannot use - a convenience,
// never an enforcement point, since the client is free to lie about what it
// hid.
func (r Role) Permissions() []Permission {
	out := make([]Permission, 0, len(permissions[r]))
	for p := range permissions[r] {
		out = append(out, p)
	}
	return out
}

// Default approval ceilings in minor units, applied when a membership has no
// explicit approval_limit_minor.
//
// A manager's default is finite and deliberately modest: the common failure is
// not a manager approving too little, it is a large claim being waved through
// because nobody set a limit when the account was created. Owners and admins
// are unlimited because they are the escalation path when a limit is hit.
const (
	DefaultManagerApprovalLimitMinor int64 = 500_000 // 5,000.00 in a 2-decimal currency
	UnlimitedApprovalLimitMinor      int64 = -1
)

// DefaultApprovalLimitMinor is the ceiling for a role with no override.
func (r Role) DefaultApprovalLimitMinor() int64 {
	switch r {
	case RoleOwner, RoleAdmin:
		return UnlimitedApprovalLimitMinor
	case RoleManager:
		return DefaultManagerApprovalLimitMinor
	default:
		return 0
	}
}
