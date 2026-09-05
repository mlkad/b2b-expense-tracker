package tenant

import (
	"testing"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
)

// The two rules the matrix exists to encode. They are asserted rather than
// commented because a future edit that grants one role both halves of a control
// would otherwise look like an ordinary feature change.
func TestSeparationOfDutiesInTheMatrix(t *testing.T) {
	for _, role := range AllRoles {
		approve := role.Allows(PermExpenseApprove)
		pay := role.Allows(PermExpensePay)

		if approve && pay && role != RoleOwner {
			t.Errorf("role %s can both approve and settle a claim; only owner may hold both, "+
				"and only because the state machine's MustNotBeDecider stops them using both on one claim", role)
		}
	}

	// Billing is the only action whose consequence is a charge to the company.
	for _, role := range AllRoles {
		if role.Allows(PermBillingManage) && role != RoleOwner {
			t.Errorf("role %s can change the subscription; only owner may", role)
		}
	}
}

// A viewer must not hold something a member lacks, and so on down the list.
// Anything else means the matrix has been edited into an inconsistent shape.
func TestMatrixIsMonotonicWhereItShouldBe(t *testing.T) {
	// Read permissions are the ones that genuinely nest. Approve and pay do
	// not - that is the separation of duties above - so they are excluded.
	nesting := []Permission{PermExpenseReadOwn, PermExpenseCreate, PermExpenseSubmit}

	for _, perm := range nesting {
		for _, senior := range []Role{RoleOwner, RoleAdmin} {
			for _, junior := range []Role{RoleMember} {
				if junior.Allows(perm) && !senior.Allows(perm) {
					t.Errorf("%s grants %s but %s does not", junior, perm, senior)
				}
			}
		}
	}
}

func TestRoleRankOrdersMostPrivilegedFirst(t *testing.T) {
	if !RoleOwner.OutranksStrictly(RoleAdmin) {
		t.Error("owner must outrank admin")
	}
	if RoleAdmin.OutranksStrictly(RoleOwner) {
		t.Error("admin must not outrank owner")
	}
	if RoleMember.OutranksStrictly(RoleMember) {
		t.Error("a role must not strictly outrank itself, or a member could administer themselves out of the tenant")
	}
	// An unknown role sorts last, so it can administer nobody.
	unknown := Role("auditor-from-the-future")
	if unknown.OutranksStrictly(RoleViewer) {
		t.Error("an unrecognised role must be treated as least privileged")
	}
}

func TestParseRole(t *testing.T) {
	for _, role := range AllRoles {
		if got, err := ParseRole(string(role)); err != nil || got != role {
			t.Errorf("ParseRole(%q) = %q, %v", role, got, err)
		}
	}
	if _, err := ParseRole("superadmin"); err == nil {
		t.Error("an unknown role was accepted")
	}
}

func TestDefaultApprovalLimits(t *testing.T) {
	if RoleOwner.DefaultApprovalLimitMinor() != UnlimitedApprovalLimitMinor {
		t.Error("owner must be unlimited: they are the escalation path when a limit is hit")
	}
	if RoleManager.DefaultApprovalLimitMinor() != DefaultManagerApprovalLimitMinor {
		t.Error("a manager must fall back to the role default")
	}
	if RoleManager.DefaultApprovalLimitMinor() < 0 {
		t.Error("a manager's default must be finite: the common failure is a large claim waved " +
			"through because nobody set a limit when the account was created")
	}
	if RoleMember.DefaultApprovalLimitMinor() != 0 {
		t.Error("a role with no approval permission must have a zero ceiling")
	}
}

// -----------------------------------------------------------------------------
// Actor
// -----------------------------------------------------------------------------

func activeActor(role Role) Actor {
	return Actor{
		TenantID:     uuid.New(),
		UserID:       uuid.New(),
		MembershipID: uuid.New(),
		Role:         role,
		Status:       MembershipActive,
		TenantStatus: StatusActive,
	}
}

func TestActorMustBeActiveForEverything(t *testing.T) {
	cases := map[string]func(*Actor){
		"invited membership":   func(a *Actor) { a.Status = MembershipInvited },
		"suspended membership": func(a *Actor) { a.Status = MembershipSuspended },
		"suspended tenant":     func(a *Actor) { a.TenantStatus = StatusSuspended },
		"cancelled tenant":     func(a *Actor) { a.TenantStatus = StatusCancelled },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			// An owner holds every permission there is, so if the check is
			// missing anywhere it shows here.
			actor := activeActor(RoleOwner)
			mutate(&actor)

			if actor.Active() {
				t.Fatal("actor reports active")
			}
			for _, perm := range RoleOwner.Permissions() {
				if actor.Can(perm) {
					t.Fatalf("an inactive actor still holds %s", perm)
				}
			}
		})
	}
}

func TestApprovalCeiling(t *testing.T) {
	t.Run("a membership override wins over the role default", func(t *testing.T) {
		actor := activeActor(RoleManager)
		override := int64(50)
		actor.ApprovalLimitMinor = &override

		if got := actor.ApprovalCeilingMinor(); got != 50 {
			t.Fatalf("ceiling = %d, want the override 50", got)
		}
		if actor.WithinApprovalLimit(shared.Money{Minor: 51, Currency: "USD"}) {
			t.Error("a claim above the override was accepted")
		}
		if !actor.WithinApprovalLimit(shared.Money{Minor: 50, Currency: "USD"}) {
			t.Error("a claim exactly at the ceiling was refused; the limit is inclusive")
		}
	})

	t.Run("no override falls back to the role default", func(t *testing.T) {
		actor := activeActor(RoleManager)
		if got := actor.ApprovalCeilingMinor(); got != DefaultManagerApprovalLimitMinor {
			t.Fatalf("ceiling = %d, want the role default", got)
		}
	})

	t.Run("a negative ceiling means unlimited", func(t *testing.T) {
		actor := activeActor(RoleOwner)
		huge := shared.Money{Minor: 9_000_000_000_000, Currency: "USD"}
		if !actor.WithinApprovalLimit(huge) {
			t.Error("an unlimited approver was refused")
		}
	})

	t.Run("an override of zero is a real limit, not 'unset'", func(t *testing.T) {
		actor := activeActor(RoleManager)
		zero := int64(0)
		actor.ApprovalLimitMinor = &zero

		if actor.WithinApprovalLimit(shared.Money{Minor: 1, Currency: "USD"}) {
			t.Error("a zero ceiling was treated as absent; a suspended approver would regain authority")
		}
	})
}

func TestDepartmentScope(t *testing.T) {
	engineering := uuid.New()
	sales := uuid.New()

	t.Run("a tenant-wide actor governs everything, including unassigned", func(t *testing.T) {
		actor := activeActor(RoleAdmin) // DepartmentID is nil
		if !actor.GovernsDepartment(&engineering) || !actor.GovernsDepartment(nil) {
			t.Fatal("a tenant-wide actor was refused")
		}
	})

	t.Run("a scoped actor governs only their own", func(t *testing.T) {
		actor := activeActor(RoleManager)
		actor.DepartmentID = &engineering

		if !actor.GovernsDepartment(&engineering) {
			t.Error("refused their own department")
		}
		if actor.GovernsDepartment(&sales) {
			t.Error("governs another department")
		}
		if actor.GovernsDepartment(nil) {
			t.Error("a scoped actor claimed an unassigned resource; those need a tenant-wide approver")
		}
	})
}
