package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/billing"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/org"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/tenant"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
)

// ErrPlanLimit means the tenant's subscription does not stretch to one more of
// something. It is a 402 rather than a 403: the caller has the authority, and
// the fix is a plan change rather than a permission change.
type ErrPlanLimit struct {
	// Singular and Plural are both carried rather than derived by trimming an
	// "s". It happens to work for every noun here today, and it is the kind of
	// shortcut that produces "1 tracked subscriptions" the first time somebody
	// adds a resource it does not fit.
	Singular string
	Plural   string

	Limit   int
	Current int
	Plan    billing.PlanCode
}

func (e *ErrPlanLimit) Error() string {
	noun := e.Plural
	if e.Limit == 1 {
		noun = e.Singular
	}
	return fmt.Sprintf("the %s plan includes %d %s and this organisation already has %d; upgrade to add more",
		e.Plan, e.Limit, noun, e.Current)
}

type OrgService struct {
	scope   *Scope
	orgs    *repo.OrgRepository
	budgets *repo.BudgetRepository
	tenancy *repo.TenancyRepository
	billing *repo.BillingRepository

	now func() time.Time
}

func NewOrgService(
	scope *Scope,
	orgs *repo.OrgRepository,
	budgets *repo.BudgetRepository,
	tenancy *repo.TenancyRepository,
	billingRepo *repo.BillingRepository,
) *OrgService {
	return &OrgService{scope: scope, orgs: orgs, budgets: budgets, tenancy: tenancy, billing: billingRepo, now: time.Now}
}

// enforceLimit is the check that was missing.
//
// The entitlement matrix computed seats, departments and vendor subscription
// ceilings, and nothing ever consulted them - so every tenant had the
// enterprise allowance whatever they were paying for. The count is taken inside
// the same transaction as the insert, so two concurrent creates cannot both see
// room for the last one.
//
// It is checked before the write rather than enforced by a constraint because
// the ceiling is a property of the subscription, not of the schema: a tenant
// that downgrades keeps the rows it already has and simply cannot add more.
// A database constraint would make the downgrade itself fail.
func (s *OrgService) enforceLimit(
	ctx context.Context,
	tc *postgres.TenantConn,
	singular, plural string,
	limitOf func(billing.Limits) int,
	countOf func(context.Context, *postgres.TenantConn) (int, error),
) error {
	entitlement, err := s.billing.GetEntitlement(ctx, tc)
	if err != nil {
		return err
	}

	limit := limitOf(entitlement.Limits())
	if limit == billing.Unlimited {
		return nil
	}

	current, err := countOf(ctx, tc)
	if err != nil {
		return err
	}
	if billing.Within(limit, current+1) {
		return nil
	}

	return &ErrPlanLimit{
		Singular: singular,
		Plural:   plural,
		Limit:    limit,
		Current:  current,
		Plan:     entitlement.EffectivePlan(),
	}
}

// -----------------------------------------------------------------------------
// Departments
// -----------------------------------------------------------------------------

func (s *OrgService) CreateDepartment(ctx context.Context, subject auth.Subject, draft org.DepartmentDraft) (*org.Department, error) {
	var created *org.Department

	err := s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermDepartmentManage); err != nil {
			return err
		}
		if err := draft.Validate(); err != nil {
			return err
		}
		if err := s.enforceLimit(ctx, tc, "department", "departments",
			func(l billing.Limits) int { return l.Departments },
			s.orgs.CountLiveDepartments); err != nil {
			return err
		}

		var err error
		created, err = s.orgs.CreateDepartment(ctx, tc, draft)
		return err
	})

	return created, err
}

func (s *OrgService) ListDepartments(ctx context.Context, subject auth.Subject, includeArchived bool) ([]*org.Department, error) {
	var out []*org.Department

	err := s.scope.Read(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		// Deliberately readable by anyone with an active membership. A member
		// filing a claim has to pick a department, so hiding the list behind
		// the management permission would make the create form unusable.
		var err error
		out, err = s.orgs.ListDepartments(ctx, tc, includeArchived)
		return err
	})

	return out, err
}

func (s *OrgService) UpdateDepartment(ctx context.Context, subject auth.Subject, id uuid.UUID, draft org.DepartmentDraft) (*org.Department, error) {
	var updated *org.Department

	err := s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermDepartmentManage); err != nil {
			return err
		}
		if err := draft.Validate(); err != nil {
			return err
		}
		// A department cannot be its own parent, and the composite foreign key
		// stops it being parented into another tenant. A deeper cycle - A under
		// B under A - is not caught here; the tree is shallow in practice and
		// the listing renders orphans rather than looping.
		if draft.ParentID != nil && *draft.ParentID == id {
			return shared.FieldError{Field: "parent_id", Detail: "a department cannot be its own parent"}
		}

		var err error
		updated, err = s.orgs.UpdateDepartment(ctx, tc, id, draft)
		return err
	})

	return updated, err
}

func (s *OrgService) ArchiveDepartment(ctx context.Context, subject auth.Subject, id uuid.UUID) error {
	return s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermDepartmentManage); err != nil {
			return err
		}
		return s.orgs.ArchiveDepartment(ctx, tc, id)
	})
}

// -----------------------------------------------------------------------------
// Budgets
// -----------------------------------------------------------------------------

func (s *OrgService) CreateBudget(ctx context.Context, subject auth.Subject, draft org.BudgetDraft) (*org.Budget, error) {
	var created *org.Budget

	err := s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermBudgetManage); err != nil {
			return err
		}

		entitlement, err := s.billing.GetEntitlement(ctx, tc)
		if err != nil {
			return err
		}
		if !entitlement.Allows(billing.FeatureDepartmentBudgets) {
			return fmt.Errorf("%w: this plan does not include department budgets", shared.ErrForbidden)
		}
		if err := draft.Validate(s.now()); err != nil {
			return err
		}

		// Overlapping envelopes are refused by budgets_no_overlap, which
		// arrives here as a field error naming period_start. Letting the
		// database answer it keeps the check atomic: two concurrent creates
		// for the same quarter cannot both pass a read-then-write test.
		created, err = s.orgs.CreateBudget(ctx, tc, draft)
		return err
	})

	return created, err
}

func (s *OrgService) ListBudgets(ctx context.Context, subject auth.Subject, departmentID *uuid.UUID, on *time.Time) ([]*org.Budget, error) {
	var out []*org.Budget

	err := s.scope.Read(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermExpenseReadTeam); err != nil {
			return err
		}
		var err error
		out, err = s.orgs.ListBudgets(ctx, tc, departmentID, on)
		return err
	})

	return out, err
}

// BudgetConsumption reports each envelope with what has been committed against
// it. This is the dashboard's headline number.
func (s *OrgService) BudgetConsumption(ctx context.Context, subject auth.Subject, on *time.Time) ([]repo.Consumption, error) {
	var out []repo.Consumption

	err := s.scope.Read(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermExpenseReadTeam); err != nil {
			return err
		}

		all, err := s.budgets.Consumption(ctx, tc, on)
		if err != nil {
			return err
		}

		// A department-scoped manager sees their own envelope and the
		// tenant-wide one, and not their colleagues'. Filtering here rather
		// than in SQL keeps one query for every caller; the result set is one
		// row per budget, so it is small by construction.
		if actor.DepartmentID != nil && !actor.Can(tenant.PermExpenseReadAll) {
			filtered := out[:0]
			for _, c := range all {
				if c.DepartmentID == nil || *c.DepartmentID == *actor.DepartmentID {
					filtered = append(filtered, c)
				}
			}
			out = filtered
			return nil
		}

		out = all
		return nil
	})

	return out, err
}

func (s *OrgService) UpdateBudget(ctx context.Context, subject auth.Subject, id uuid.UUID, draft org.BudgetDraft) (*org.Budget, error) {
	var updated *org.Budget

	err := s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermBudgetManage); err != nil {
			return err
		}
		if err := draft.Validate(s.now()); err != nil {
			return err
		}
		var err error
		updated, err = s.orgs.UpdateBudget(ctx, tc, id, draft)
		return err
	})

	return updated, err
}

func (s *OrgService) DeleteBudget(ctx context.Context, subject auth.Subject, id uuid.UUID) error {
	return s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermBudgetManage); err != nil {
			return err
		}
		return s.orgs.DeleteBudget(ctx, tc, id)
	})
}

// -----------------------------------------------------------------------------
// Vendor subscriptions
// -----------------------------------------------------------------------------

func (s *OrgService) CreateVendorSubscription(ctx context.Context, subject auth.Subject, draft org.VendorSubscriptionDraft) (*org.VendorSubscription, error) {
	var created *org.VendorSubscription

	err := s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermVendorSubManage); err != nil {
			return err
		}

		entitlement, err := s.billing.GetEntitlement(ctx, tc)
		if err != nil {
			return err
		}
		if !entitlement.Allows(billing.FeatureVendorSubTracking) {
			return fmt.Errorf("%w: this plan does not include vendor subscription tracking", shared.ErrForbidden)
		}
		if err := draft.Validate(s.now()); err != nil {
			return err
		}
		if err := s.enforceLimit(ctx, tc, "tracked subscription", "tracked subscriptions",
			func(l billing.Limits) int { return l.VendorSubscriptions },
			s.orgs.CountActiveVendorSubscriptions); err != nil {
			return err
		}

		created, err = s.orgs.CreateVendorSubscription(ctx, tc, draft)
		return err
	})

	return created, err
}

func (s *OrgService) ListVendorSubscriptions(ctx context.Context, subject auth.Subject, status *org.VendorStatus) ([]repo.VendorSubscriptionRecord, error) {
	var out []repo.VendorSubscriptionRecord

	err := s.scope.Read(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermExpenseReadTeam); err != nil {
			return err
		}
		var err error
		out, err = s.orgs.ListVendorSubscriptions(ctx, tc, status)
		return err
	})

	return out, err
}

func (s *OrgService) UpdateVendorSubscription(
	ctx context.Context,
	subject auth.Subject,
	id uuid.UUID,
	draft org.VendorSubscriptionDraft,
	status org.VendorStatus,
) (*org.VendorSubscription, error) {
	var updated *org.VendorSubscription

	err := s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermVendorSubManage); err != nil {
			return err
		}
		if !status.Valid() {
			return shared.FieldError{Field: "status", Detail: "must be one of active, paused or cancelled"}
		}

		existing, err := s.orgs.GetVendorSubscription(ctx, tc, id)
		if err != nil {
			return err
		}

		// A cancelled subscription's next charge date is in the past and must
		// stay there, so the draft is validated against the existing date
		// rather than against today when nothing about the schedule changed.
		if status != org.VendorCancelled || !draft.NextChargeOn.Equal(existing.NextChargeOn) {
			if err := draft.Validate(s.now()); err != nil {
				return err
			}
		} else {
			draft.NextChargeOn = existing.NextChargeOn
		}

		updated, err = s.orgs.UpdateVendorSubscription(ctx, tc, id, draft, status, existing.CancelledAt, s.now())
		return err
	})

	return updated, err
}

// -----------------------------------------------------------------------------
// Members
// -----------------------------------------------------------------------------

// Members lists the organisation with the user behind each membership.
func (s *OrgService) Members(ctx context.Context, subject auth.Subject) ([]repo.MemberRecord, error) {
	var out []repo.MemberRecord

	err := s.scope.Read(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermMemberManage); err != nil {
			return err
		}
		var err error
		out, err = s.tenancy.ListMembers(ctx, tc)
		return err
	})

	return out, err
}

// InviteMember adds somebody to the organisation, creating their global user
// identity if this is their first.
func (s *OrgService) InviteMember(
	ctx context.Context,
	subject auth.Subject,
	email string,
	role tenant.Role,
	departmentID *uuid.UUID,
	approvalLimit *int64,
) (tenant.Membership, error) {
	var invited tenant.Membership

	// The user lookup runs outside the tenant transaction because users are
	// global and deliberately outside the RLS model - see the note at the top
	// of migration 00006.
	user, err := s.tenancy.GetUserByEmail(ctx, s.scope.DB(), email)
	if err != nil {
		if err != shared.ErrNotFound {
			return invited, err
		}
		// No password: the invitee sets one when they accept. A nil hash is
		// "no credential", which ComparePassword refuses whatever is sent.
		user, err = s.tenancy.CreateUser(ctx, s.scope.DB(), email, nil, nil)
		if err != nil {
			return invited, err
		}
	}

	err = s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermMemberManage); err != nil {
			return err
		}
		if !role.Valid() {
			return shared.FieldError{Field: "role", Detail: "is not a known role"}
		}
		// You may not create somebody at or above your own standing. Without
		// this an admin could invite an owner and then act through them.
		if !actor.Role.OutranksStrictly(role) {
			return fmt.Errorf("%w: you may only invite members below your own role", shared.ErrForbidden)
		}
		if err := s.enforceLimit(ctx, tc, "seat", "seats",
			func(l billing.Limits) int { return l.Seats },
			s.orgs.CountActiveMembers); err != nil {
			return err
		}

		var err error
		// actor.UserID, not MembershipID: memberships.invited_by references
		// users, so that the record of who invited somebody survives the
		// inviter leaving the organisation.
		invited, err = s.tenancy.InviteMember(ctx, tc, user.ID, role, departmentID, approvalLimit, actor.UserID)
		return err
	})

	return invited, err
}

// UpdateMember changes somebody's role, status, department or approval limit.
func (s *OrgService) UpdateMember(
	ctx context.Context,
	subject auth.Subject,
	id uuid.UUID,
	role tenant.Role,
	status tenant.MembershipStatus,
	departmentID *uuid.UUID,
	approvalLimit *int64,
) (tenant.Membership, error) {
	var updated tenant.Membership

	err := s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermMemberManage); err != nil {
			return err
		}
		if !role.Valid() {
			return shared.FieldError{Field: "role", Detail: "is not a known role"}
		}

		// Changing your own standing is refused outright. The dangerous case is
		// self-promotion, but self-demotion is refused too: an owner demoting
		// themselves leaves an organisation nobody can administer, and
		// memberships_single_owner_key makes that unrecoverable through the API.
		if actor.SameMembership(id) {
			return fmt.Errorf("%w: you cannot change your own role or status", shared.ErrForbidden)
		}

		target, err := s.tenancy.GetMember(ctx, tc, id)
		if err != nil {
			return err
		}
		// You may only administer somebody strictly below you, and may not
		// promote them to or above your own level.
		if !actor.Role.OutranksStrictly(target.Role) {
			return fmt.Errorf("%w: you may only administer members below your own role", shared.ErrForbidden)
		}
		if !actor.Role.OutranksStrictly(role) {
			return fmt.Errorf("%w: you may not grant a role at or above your own", shared.ErrForbidden)
		}

		updated, err = s.tenancy.UpdateMember(ctx, tc, id, role, status, departmentID, approvalLimit)
		return err
	})

	return updated, err
}

// -----------------------------------------------------------------------------
// Dashboard summary
// -----------------------------------------------------------------------------

// Summary is the dashboard's headline strip: totals by status and by
// department over a window.
type Summary struct {
	From         *time.Time             `json:"from,omitempty"`
	To           *time.Time             `json:"to,omitempty"`
	ByStatus     []repo.StatusTotal     `json:"by_status"`
	ByDepartment []repo.DepartmentTotal `json:"by_department"`
}

// Summary computes both aggregates in one read-only transaction, so the two
// halves of the strip describe the same instant rather than two reads a second
// apart during a busy period.
func (s *OrgService) Summary(ctx context.Context, subject auth.Subject, from, to *time.Time) (Summary, error) {
	var out Summary

	err := s.scope.Read(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		// Deliberately requires tenant-wide read. A department-scoped manager
		// getting an organisation-wide total would learn what every other
		// department spends, which is exactly what the scoping exists to
		// prevent - and narrowing the aggregate per department would make it a
		// different endpoint rather than a filtered one.
		if err := Require(actor, tenant.PermExpenseReadAll); err != nil {
			return err
		}

		org, err := s.tenancy.GetTenant(ctx, tc)
		if err != nil {
			return err
		}

		byStatus, byDepartment, err := s.budgets.Summary(ctx, tc, from, to, org.DefaultCurrency)
		if err != nil {
			return err
		}

		out = Summary{From: from, To: to, ByStatus: byStatus, ByDepartment: byDepartment}
		return nil
	})

	return out, err
}
