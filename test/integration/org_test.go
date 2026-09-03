//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/billing"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/org"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/tenant"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/service"
)

func orgServiceForTest(t *testing.T) *service.OrgService {
	t.Helper()
	tenancy := repo.NewTenancyRepository()
	return service.NewOrgService(
		service.NewScope(app, tenancy),
		repo.NewOrgRepository(),
		repo.NewBudgetRepository(),
		tenancy,
		repo.NewBillingRepository(),
	)
}

// subjectFor builds the token subject for a seeded member. The service
// resolves the actor from the database, so only the user id and tenant matter.
func subjectFor(t *testing.T, o orgFixture, membershipID uuid.UUID) auth.Subject {
	t.Helper()

	var userID uuid.UUID
	err := app.WithSystemTx(context.Background(), postgres.Binding{TenantID: o.TenantID},
		"test: resolve the user behind a membership",
		func(ctx context.Context, tc *postgres.TenantConn) error {
			return tc.QueryRow(ctx, `SELECT user_id FROM memberships WHERE id = $1`, membershipID).Scan(&userID)
		})
	if err != nil {
		t.Fatalf("resolve user: %v", err)
	}
	return auth.Subject{UserID: userID, TenantID: o.TenantID}
}

// grantPlan writes a live subscription for a tenant through the system path.
func grantPlan(t *testing.T, tenantID uuid.UUID, plan billing.PlanCode, seats int32) {
	t.Helper()
	now := time.Now().UTC()
	err := repo.NewBillingRepository().ApplySubscription(context.Background(), app, tenantID, repo.SubscriptionState{
		GatewaySubscriptionID: "sub_" + uuid.NewString(),
		GatewayCustomerRef:    tenantID.String(),
		PlanCode:              string(plan),
		Status:                string(billing.StatusActive),
		Seats:                 seats,
		CurrentPeriodStart:    now,
		CurrentPeriodEnd:      now.Add(30 * 24 * time.Hour),
		EventID:               "evt_" + uuid.NewString(),
		EventAt:               now,
	})
	if err != nil {
		t.Fatalf("grant plan: %v", err)
	}
}

// The check that did not exist: the entitlement matrix computed ceilings and
// nothing consulted them, so every tenant had the enterprise allowance whatever
// they were paying for.
func TestPlanLimitsAreEnforced(t *testing.T) {
	t.Run("departments are capped by the plan", func(t *testing.T) {
		o := seedOrg(t, "limit-departments")
		grantPlan(t, o.TenantID, billing.PlanStarter, 10) // 5 departments

		svc := orgServiceForTest(t)
		subject := subjectFor(t, o, o.Manager)
		admin := promote(t, o, o.Manager, tenant.RoleAdmin)
		_ = admin

		ctx := context.Background()
		// One department already exists from the fixture.
		created := 1
		var limitErr *service.ErrPlanLimit

		for i := 0; i < 10; i++ {
			_, err := svc.CreateDepartment(ctx, subject, org.DepartmentDraft{
				Name: "Department " + uuid.NewString()[:8],
			})
			if err == nil {
				created++
				continue
			}
			if errors.As(err, &limitErr) {
				break
			}
			t.Fatalf("unexpected error after %d departments: %v", created, err)
		}

		if limitErr == nil {
			t.Fatalf("created %d departments with a 5-department plan and was never refused", created)
		}
		if created != 5 {
			t.Fatalf("the ceiling bit at %d departments, want 5", created)
		}
		if limitErr.Limit != 5 || limitErr.Plan != billing.PlanStarter {
			t.Errorf("the refusal does not name the ceiling or the plan: %+v", limitErr)
		}
	})

	t.Run("an unlimited plan is not capped", func(t *testing.T) {
		o := seedOrg(t, "limit-unlimited")
		grantPlan(t, o.TenantID, billing.PlanEnterprise, 500)

		svc := orgServiceForTest(t)
		subject := subjectFor(t, o, o.Manager)
		promote(t, o, o.Manager, tenant.RoleAdmin)

		for i := 0; i < 8; i++ {
			if _, err := svc.CreateDepartment(context.Background(), subject, org.DepartmentDraft{
				Name: "Dept " + uuid.NewString()[:8],
			}); err != nil {
				t.Fatalf("enterprise plan refused department %d: %v", i+1, err)
			}
		}
	})

	// A tenant that never subscribed falls to the free tier, which is one
	// department - and the fixture already has it.
	t.Run("an unsubscribed tenant gets the free ceiling", func(t *testing.T) {
		o := seedOrg(t, "limit-free")
		svc := orgServiceForTest(t)
		subject := subjectFor(t, o, o.Manager)
		promote(t, o, o.Manager, tenant.RoleAdmin)

		_, err := svc.CreateDepartment(context.Background(), subject, org.DepartmentDraft{Name: "Second"})

		var limitErr *service.ErrPlanLimit
		if !errors.As(err, &limitErr) {
			t.Fatalf("got %v, want a plan limit error", err)
		}
		if limitErr.Plan != billing.PlanFree {
			t.Errorf("plan = %s, want free", limitErr.Plan)
		}
	})

	t.Run("seats are capped by what was purchased, not by the plan ceiling", func(t *testing.T) {
		o := seedOrg(t, "limit-seats")
		// Growth allows 50, but this tenant pays for 4 - and the fixture has
		// already created 3 memberships.
		grantPlan(t, o.TenantID, billing.PlanGrowth, 4)

		svc := orgServiceForTest(t)
		subject := subjectFor(t, o, o.Manager)
		promote(t, o, o.Manager, tenant.RoleAdmin)
		ctx := context.Background()

		if _, err := svc.InviteMember(ctx, subject, "seat4-"+o.Slug+"@example.test",
			tenant.RoleMember, nil, nil); err != nil {
			t.Fatalf("the fourth seat was refused: %v", err)
		}

		_, err := svc.InviteMember(ctx, subject, "seat5-"+o.Slug+"@example.test",
			tenant.RoleMember, nil, nil)

		var limitErr *service.ErrPlanLimit
		if !errors.As(err, &limitErr) {
			t.Fatalf("a fifth seat on a four-seat subscription: got %v, want a plan limit error", err)
		}
		if limitErr.Limit != 4 {
			t.Errorf("the refusal names a limit of %d, want the purchased 4", limitErr.Limit)
		}
	})
}

// promote raises a seeded member's role, so a test can act as somebody with
// the permission under test without a second fixture.
func promote(t *testing.T, o orgFixture, membershipID uuid.UUID, role tenant.Role) uuid.UUID {
	t.Helper()
	err := app.WithSystemTx(context.Background(), postgres.Binding{TenantID: o.TenantID},
		"test: promote a seeded member",
		func(ctx context.Context, tc *postgres.TenantConn) error {
			_, err := tc.Exec(ctx,
				`UPDATE memberships SET role = $2, department_id = NULL WHERE id = $1`, membershipID, string(role))
			return err
		})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	return membershipID
}

// Nobody may create or promote somebody to their own level or above. Without
// this an admin could invite an owner and then act through them.
func TestPrivilegeEscalationIsRefused(t *testing.T) {
	o := seedOrg(t, "escalation")
	grantPlan(t, o.TenantID, billing.PlanEnterprise, 100)

	svc := orgServiceForTest(t)
	adminSubject := subjectFor(t, o, o.Manager)
	promote(t, o, o.Manager, tenant.RoleAdmin)
	ctx := context.Background()

	t.Run("an admin cannot invite an owner", func(t *testing.T) {
		_, err := svc.InviteMember(ctx, adminSubject, "newowner-"+o.Slug+"@example.test",
			tenant.RoleOwner, nil, nil)
		if !errors.Is(err, shared.ErrForbidden) {
			t.Fatalf("got %v, want ErrForbidden", err)
		}
	})

	t.Run("an admin cannot invite another admin", func(t *testing.T) {
		_, err := svc.InviteMember(ctx, adminSubject, "peer-"+o.Slug+"@example.test",
			tenant.RoleAdmin, nil, nil)
		if !errors.Is(err, shared.ErrForbidden) {
			t.Fatalf("got %v, want ErrForbidden - inviting a peer is one step from inviting a superior", err)
		}
	})

	t.Run("an admin can invite below themselves", func(t *testing.T) {
		if _, err := svc.InviteMember(ctx, adminSubject, "junior-"+o.Slug+"@example.test",
			tenant.RoleMember, nil, nil); err != nil {
			t.Fatalf("a legitimate invitation was refused: %v", err)
		}
	})

	t.Run("nobody may change their own standing", func(t *testing.T) {
		err := func() error {
			_, err := svc.UpdateMember(ctx, adminSubject, o.Manager,
				tenant.RoleOwner, tenant.MembershipActive, nil, nil)
			return err
		}()
		if !errors.Is(err, shared.ErrForbidden) {
			t.Fatalf("self-promotion: got %v, want ErrForbidden", err)
		}
	})

	t.Run("an admin cannot promote a member above themselves", func(t *testing.T) {
		_, err := svc.UpdateMember(ctx, adminSubject, o.Submitter,
			tenant.RoleOwner, tenant.MembershipActive, nil, nil)
		if !errors.Is(err, shared.ErrForbidden) {
			t.Fatalf("got %v, want ErrForbidden", err)
		}
	})
}

// Two envelopes covering the same department and overlapping dates make
// "how much is left this quarter" ambiguous, and every downstream number
// inherits that.
func TestOverlappingBudgetsAreRefusedByTheDatabase(t *testing.T) {
	o := seedOrg(t, "budget-overlap")
	grantPlan(t, o.TenantID, billing.PlanGrowth, 20)

	svc := orgServiceForTest(t)
	subject := subjectFor(t, o, o.Finance)
	ctx := context.Background()

	dept := o.Department
	q1 := org.BudgetDraft{
		DepartmentID: &dept,
		PeriodStart:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:    time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
		Amount:       shared.Money{Minor: 1_000_000, Currency: "USD"},
	}
	if _, err := svc.CreateBudget(ctx, subject, q1); err != nil {
		t.Fatalf("first budget: %v", err)
	}

	overlapping := q1
	overlapping.PeriodStart = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	overlapping.PeriodEnd = time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

	_, err := svc.CreateBudget(ctx, subject, overlapping)
	if !errors.Is(err, shared.ErrValidation) && !errors.Is(err, shared.ErrConflict) {
		t.Fatalf("an overlapping budget was accepted: %v", err)
	}

	// Adjacent, not overlapping, must be fine.
	adjacent := q1
	adjacent.PeriodStart = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	adjacent.PeriodEnd = time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	if _, err := svc.CreateBudget(ctx, subject, adjacent); err != nil {
		t.Fatalf("an adjacent quarter was refused: %v", err)
	}
}

// The dashboard's headline figure, computed from real claims.
func TestBudgetConsumptionCountsOnlyCommittedSpend(t *testing.T) {
	o := seedOrg(t, "budget-consumption")
	grantPlan(t, o.TenantID, billing.PlanGrowth, 20)

	svc := orgServiceForTest(t)
	subject := subjectFor(t, o, o.Finance)
	ctx := context.Background()

	dept := o.Department
	today := time.Now().UTC()
	if _, err := svc.CreateBudget(ctx, subject, org.BudgetDraft{
		DepartmentID:      &dept,
		PeriodStart:       today.AddDate(0, 0, -30),
		PeriodEnd:         today.AddDate(0, 0, 30),
		Amount:            shared.Money{Minor: 10_000, Currency: "USD"},
		AlertThresholdBps: 8000,
	}); err != nil {
		t.Fatalf("create budget: %v", err)
	}

	// Only approved and paid count. Counting pending would let anyone exhaust
	// a budget on paper by filing claims nobody agreed to.
	seedClaim(t, o, "draft", 5_000)
	seedClaim(t, o, "pending_approval", 5_000)
	seedClaim(t, o, "rejected", 5_000)
	seedClaim(t, o, "approved", 3_000)
	seedClaim(t, o, "paid", 2_000)

	rows, err := svc.BudgetConsumption(ctx, subject, &today)
	if err != nil {
		t.Fatalf("consumption: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d envelopes, want 1", len(rows))
	}

	c := rows[0]
	if c.Consumed.Minor != 5_000 {
		t.Fatalf("consumed = %d, want 5000 (approved 3000 + paid 2000); "+
			"drafts, pending and rejected claims must not count", c.Consumed.Minor)
	}
	if c.Remaining().Minor != 5_000 {
		t.Errorf("remaining = %d, want 5000", c.Remaining().Minor)
	}
	if c.UsageBps() != 5000 {
		t.Errorf("usage = %d bps, want 5000", c.UsageBps())
	}
	if c.BreachesThreshold() {
		t.Error("50%% of the envelope reported as breaching an 80%% threshold")
	}
}

// An archived department keeps its history attributable rather than being
// deleted out from under it.
func TestArchivingADepartmentKeepsItsClaims(t *testing.T) {
	o := seedOrg(t, "dept-archive")
	grantPlan(t, o.TenantID, billing.PlanGrowth, 20)

	claim := seedClaim(t, o, "paid", 1_234)

	svc := orgServiceForTest(t)
	subject := subjectFor(t, o, o.Manager)
	promote(t, o, o.Manager, tenant.RoleAdmin)
	ctx := context.Background()

	if err := svc.ArchiveDepartment(ctx, subject, o.Department); err != nil {
		t.Fatalf("archive: %v", err)
	}

	if n := countAsOwner(t, `SELECT count(*) FROM expenses WHERE id = $1 AND department_id = $2`,
		claim, o.Department); n != 1 {
		t.Fatal("archiving the department detached its claims")
	}

	// It disappears from the default listing but is still reachable.
	live, err := svc.ListDepartments(ctx, subject, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range live {
		if d.ID == o.Department {
			t.Fatal("an archived department is still in the default listing")
		}
	}

	all, err := svc.ListDepartments(ctx, subject, true)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range all {
		if d.ID == o.Department {
			found = true
			if !d.Archived() {
				t.Error("the department is not marked archived")
			}
		}
	}
	if !found {
		t.Fatal("the archived department is unreachable even with include_archived")
	}

	// Archiving twice is not an error the caller can act on differently, but
	// it must not silently report success either.
	if err := svc.ArchiveDepartment(ctx, subject, o.Department); !errors.Is(err, shared.ErrNotFound) {
		t.Errorf("re-archiving returned %v, want ErrNotFound", err)
	}
}

// The annualised total is the number a customer reviewing software spend
// actually asks for, and the cadence arithmetic is a domain rule rather than
// something each client re-derives.
func TestVendorSubscriptionAnnualisation(t *testing.T) {
	cases := map[org.Cadence]int64{
		org.CadenceWeekly:    52_000,
		org.CadenceMonthly:   12_000,
		org.CadenceQuarterly: 4_000,
		org.CadenceAnnual:    1_000,
	}
	for cadence, want := range cases {
		sub := &org.VendorSubscription{
			Amount:  shared.Money{Minor: 1_000, Currency: "USD"},
			Cadence: cadence,
		}
		if got := sub.AnnualisedMinor(); got != want {
			t.Errorf("%s annualised to %d, want %d", cadence, got, want)
		}
	}
}

// The summary must be computed in SQL rather than from a page of results: a
// dashboard summing its first page reports the wrong total for any tenant with
// more than one page, and is wrong in a way that looks plausible.
func TestSummaryAggregatesTheWholeTenant(t *testing.T) {
	o := seedOrg(t, "summary")
	grantPlan(t, o.TenantID, billing.PlanGrowth, 20)

	for i := 0; i < 40; i++ {
		seedClaim(t, o, "approved", 100)
	}
	seedClaim(t, o, "draft", 999)
	seedClaim(t, o, "paid", 500)

	svc := orgServiceForTest(t)
	subject := subjectFor(t, o, o.Finance)

	summary, err := svc.Summary(context.Background(), subject, nil, nil)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}

	totals := map[string]int64{}
	counts := map[string]int64{}
	for _, row := range summary.ByStatus {
		totals[row.Status] = row.Total.Minor
		counts[row.Status] = row.ClaimCount
	}

	if counts["approved"] != 40 || totals["approved"] != 4000 {
		t.Errorf("approved: %d claims totalling %d, want 40 and 4000 - "+
			"the aggregate covers more rows than one page holds",
			counts["approved"], totals["approved"])
	}
	if counts["draft"] != 1 || totals["draft"] != 999 {
		t.Errorf("draft: %d claims totalling %d, want 1 and 999", counts["draft"], totals["draft"])
	}

	// Only committed spend appears per department, matching the budget rollup.
	if len(summary.ByDepartment) != 1 {
		t.Fatalf("got %d departments, want 1", len(summary.ByDepartment))
	}
	if got := summary.ByDepartment[0].Total.Minor; got != 4500 {
		t.Errorf("department total = %d, want 4500 (approved 4000 + paid 500); a draft must not count", got)
	}
}

// A department-scoped manager must not learn what other departments spend.
func TestSummaryRequiresTenantWideRead(t *testing.T) {
	o := seedOrg(t, "summary-scope")
	svc := orgServiceForTest(t)

	// o.Manager is seeded scoped to the Engineering department.
	err := func() error {
		_, err := svc.Summary(context.Background(), subjectFor(t, o, o.Manager), nil, nil)
		return err
	}()
	if !errors.Is(err, shared.ErrForbidden) {
		t.Fatalf("a department-scoped manager got an organisation-wide total: %v", err)
	}

	if _, err := svc.Summary(context.Background(), subjectFor(t, o, o.Finance), nil, nil); err != nil {
		t.Fatalf("finance was refused a tenant-wide summary: %v", err)
	}
}
