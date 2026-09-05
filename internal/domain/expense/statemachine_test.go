package expense

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/tenant"
)

var (
	tenantID   = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	submitter  = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	approver   = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	payer      = uuid.MustParse("44444444-4444-4444-4444-444444444444")
	deptEng    = uuid.MustParse("55555555-5555-5555-5555-555555555555")
	deptSales  = uuid.MustParse("66666666-6666-6666-6666-666666666666")
	fixedClock = time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC)
)

func actor(id uuid.UUID, role tenant.Role, opts ...func(*tenant.Actor)) tenant.Actor {
	a := tenant.Actor{
		TenantID:     tenantID,
		UserID:       uuid.New(),
		MembershipID: id,
		Role:         role,
		Status:       tenant.MembershipActive,
		TenantStatus: tenant.StatusActive,
	}
	for _, o := range opts {
		o(&a)
	}
	return a
}

func scopedTo(dept uuid.UUID) func(*tenant.Actor) {
	return func(a *tenant.Actor) { a.DepartmentID = &dept }
}

func limit(minor int64) func(*tenant.Actor) {
	return func(a *tenant.Actor) { a.ApprovalLimitMinor = &minor }
}

func newDraft(t *testing.T, opts ...func(*Draft)) *Expense {
	t.Helper()
	d := Draft{
		DepartmentID: &deptEng,
		Category:     CategorySoftware,
		Amount:       shared.Money{Minor: 12_500, Currency: "USD"},
		Merchant:     "Figma",
		SpentAt:      fixedClock.AddDate(0, 0, -3),
	}
	for _, o := range opts {
		o(&d)
	}
	e, ev, err := New(tenantID, submitter, d, fixedClock)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if ev.Action != EventCreated || ev.ToStatus != StatusDraft {
		t.Fatalf("New event = %s/%s, want created/draft", ev.Action, ev.ToStatus)
	}
	return e
}

func apply(t *testing.T, e *Expense, cmd Command) Event {
	t.Helper()
	ev, err := e.Apply(cmd, fixedClock)
	if err != nil {
		t.Fatalf("Apply(%s): unexpected error: %v", cmd.Action, err)
	}
	return ev
}

func reason(s string) *string { return &s }

// -----------------------------------------------------------------------------
// Table integrity
// -----------------------------------------------------------------------------

// The transition table is data, so the properties that make it a well-formed
// state machine are checkable directly rather than by exercising every path
// through Apply.
func TestTransitionTableIsWellFormed(t *testing.T) {
	t.Run("every edge names known states and a known action", func(t *testing.T) {
		for _, tr := range transitions {
			if !tr.From.Valid() {
				t.Errorf("edge %s/%s: unknown from-state", tr.From, tr.Action)
			}
			if !tr.To.Valid() {
				t.Errorf("edge %s/%s: unknown to-state %q", tr.From, tr.Action, tr.To)
			}
			if !tr.Action.Valid() {
				t.Errorf("edge %s/%s: unknown action", tr.From, tr.Action)
			}
			if tr.From == tr.To {
				t.Errorf("edge %s/%s is a self-loop; expense_events_transition_chk would reject its ledger row",
					tr.From, tr.Action)
			}
		}
	})

	t.Run("every state except paid is reachable and leaves", func(t *testing.T) {
		reachable := map[Status]bool{StatusDraft: true}
		outgoing := map[Status]int{}
		for _, tr := range transitions {
			reachable[tr.To] = true
			outgoing[tr.From]++
		}
		for _, s := range AllStatuses {
			if !reachable[s] {
				t.Errorf("status %s is unreachable: no edge produces it", s)
			}
			if s.Terminal() {
				if outgoing[s] != 0 {
					t.Errorf("status %s is terminal but has %d outgoing edges", s, outgoing[s])
				}
				continue
			}
			if outgoing[s] == 0 {
				t.Errorf("status %s is a dead end but is not marked terminal", s)
			}
		}
	})

	t.Run("paid is the only terminal state", func(t *testing.T) {
		for _, s := range AllStatuses {
			if s.Terminal() != (s == StatusPaid) {
				t.Errorf("Terminal(%s) = %v", s, s.Terminal())
			}
		}
	})

	// The invariant the product depends on, asserted against the table rather
	// than against a call: a rejected claim has exactly one way forward, and it
	// is the one that starts a new revision.
	t.Run("rejected leads only to a new draft revision", func(t *testing.T) {
		var edges []transition
		for _, tr := range transitions {
			if tr.From == StatusRejected {
				edges = append(edges, tr)
			}
		}
		if len(edges) != 1 {
			t.Fatalf("rejected has %d outgoing edges, want exactly 1", len(edges))
		}
		if edges[0].To != StatusDraft || !edges[0].BumpsRevision {
			t.Fatalf("rejected -> %s (bumps revision: %v), want draft with a revision bump",
				edges[0].To, edges[0].BumpsRevision)
		}
	})

	t.Run("no edge both decides and settles without a second person", func(t *testing.T) {
		for _, tr := range transitions {
			switch tr.Action {
			case ActionApprove, ActionReject:
				if !tr.MustNotBeSubmitter {
					t.Errorf("edge %s/%s lets a submitter decide on their own claim", tr.From, tr.Action)
				}
			case ActionPay:
				if !tr.MustNotBeSubmitter || !tr.MustNotBeDecider {
					t.Errorf("edge %s/%s does not require a third party to settle", tr.From, tr.Action)
				}
			}
		}
	})
}

// Every (state, action) pair is either an edge or a refusal. This is the test
// that catches a status or action added without a decision about what it means
// everywhere else.
func TestEveryStateActionPairIsDecided(t *testing.T) {
	// An owner carries every permission, so whatever refusal remains is a
	// per-claim guard rather than the role matrix. Which of the two owners is
	// used depends on the edge: MustBeSubmitter edges need the filer,
	// MustNotBeSubmitter edges need anybody else.
	filer := actor(submitter, tenant.RoleOwner)
	thirdParty := actor(approver, tenant.RoleOwner)

	for _, from := range AllStatuses {
		for _, action := range AllActions {
			edge, isEdge := index[edgeKey{from, action}]

			probe := thirdParty
			if isEdge && edge.MustBeSubmitter {
				probe = filer
			}

			e := newDraft(t)
			e.Status = from
			// Give the claim a decision trail consistent with the state, so
			// the guards see a plausible row rather than a contradictory one.
			switch from {
			case StatusPendingApproval:
				e.SubmittedAt = &fixedClock
			case StatusApproved, StatusRejected, StatusPaid:
				e.SubmittedAt = &fixedClock
				e.DecidedAt = &fixedClock
				e.DecidedBy = &payer
			}

			cmd := Command{Action: action, Actor: probe, Reason: reason("x"), PaymentRef: reason("ref")}
			err := e.CanApply(cmd)

			if isEdge {
				if err != nil {
					t.Errorf("%s/%s is an edge but a suitably placed owner was refused: %v", from, action, err)
				}
				continue
			}
			var te *TransitionError
			if !errors.As(err, &te) {
				t.Errorf("%s/%s is not an edge; want TransitionError, got %v", from, action, err)
			}
		}
	}
}

// -----------------------------------------------------------------------------
// Lifecycle
// -----------------------------------------------------------------------------

func TestHappyPathDraftToPaid(t *testing.T) {
	e := newDraft(t)
	sub := actor(submitter, tenant.RoleMember)
	mgr := actor(approver, tenant.RoleManager, scopedTo(deptEng))
	fin := actor(payer, tenant.RoleFinance)

	if got := e.Version; got != 1 {
		t.Fatalf("new claim version = %d, want 1", got)
	}

	ev := apply(t, e, Command{Action: ActionSubmit, Actor: sub})
	if e.Status != StatusPendingApproval || e.SubmittedAt == nil {
		t.Fatalf("after submit: status=%s submitted_at=%v", e.Status, e.SubmittedAt)
	}
	if ev.Action != EventSubmitted || *ev.FromStatus != StatusDraft || ev.ToStatus != StatusPendingApproval {
		t.Fatalf("submit event = %+v", ev)
	}

	apply(t, e, Command{Action: ActionApprove, Actor: mgr})
	if e.Status != StatusApproved || e.DecidedBy == nil || *e.DecidedBy != approver {
		t.Fatalf("after approve: status=%s decided_by=%v", e.Status, e.DecidedBy)
	}

	ev = apply(t, e, Command{Action: ActionPay, Actor: fin, PaymentRef: reason("BACS-2026-03-14-0007")})
	if e.Status != StatusPaid || e.PaidAt == nil || *e.PaymentRef != "BACS-2026-03-14-0007" {
		t.Fatalf("after pay: status=%s paid_at=%v ref=%v", e.Status, e.PaidAt, e.PaymentRef)
	}
	if ev.Amount != e.Amount || ev.Revision != e.Revision {
		t.Fatalf("ledger row does not carry the amount and revision it describes: %+v", ev)
	}
	if e.Version != 4 {
		t.Fatalf("version = %d after three transitions, want 4", e.Version)
	}
}

// The invariant the whole design turns on.
func TestRejectedCannotReturnStraightToApproval(t *testing.T) {
	e := newDraft(t)
	sub := actor(submitter, tenant.RoleMember)
	mgr := actor(approver, tenant.RoleManager, scopedTo(deptEng))

	apply(t, e, Command{Action: ActionSubmit, Actor: sub})
	apply(t, e, Command{Action: ActionReject, Actor: mgr, Reason: reason("no receipt attached")})

	if e.Status != StatusRejected || e.Revision != 1 {
		t.Fatalf("after reject: status=%s revision=%d", e.Status, e.Revision)
	}

	_, err := e.Apply(Command{Action: ActionSubmit, Actor: sub}, fixedClock)
	var te *TransitionError
	if !errors.As(err, &te) {
		t.Fatalf("resubmitting a rejected claim: got %v, want TransitionError", err)
	}
	if !errors.Is(err, shared.ErrConflict) {
		t.Fatalf("TransitionError must unwrap to ErrConflict so the API answers 409, got %v", err)
	}
	if te.Reason == "" {
		t.Error("the most common dead end in the product should carry a message telling the user what to do instead")
	}
	if e.Status != StatusRejected {
		t.Fatalf("a refused transition mutated the claim: status is now %s", e.Status)
	}

	// The legal route: revise, which starts a new iteration an approver can
	// tell apart from the one they already refused.
	apply(t, e, Command{Action: ActionRevise, Actor: sub})
	if e.Status != StatusDraft || e.Revision != 2 {
		t.Fatalf("after revise: status=%s revision=%d, want draft/2", e.Status, e.Revision)
	}
	if e.DecidedAt != nil || e.DecidedBy != nil || e.SubmittedAt != nil || e.DecisionNote != nil {
		t.Fatalf("revising must clear the decision trail; got submitted=%v decided=%v by=%v note=%v",
			e.SubmittedAt, e.DecidedAt, e.DecidedBy, e.DecisionNote)
	}

	apply(t, e, Command{Action: ActionSubmit, Actor: sub})
	if e.Status != StatusPendingApproval || e.Revision != 2 {
		t.Fatalf("revision 2 did not reach approval: status=%s revision=%d", e.Status, e.Revision)
	}
}

func TestWithdrawDoesNotStartANewRevision(t *testing.T) {
	e := newDraft(t)
	sub := actor(submitter, tenant.RoleMember)

	apply(t, e, Command{Action: ActionSubmit, Actor: sub})
	apply(t, e, Command{Action: ActionWithdraw, Actor: sub})

	if e.Status != StatusDraft {
		t.Fatalf("status = %s, want draft", e.Status)
	}
	if e.Revision != 1 {
		t.Fatalf("revision = %d after a withdrawal, want 1: nobody decided anything", e.Revision)
	}
	if e.SubmittedAt != nil {
		t.Fatalf("submitted_at survived a withdrawal: %v", e.SubmittedAt)
	}
}

// -----------------------------------------------------------------------------
// Authorisation guards
// -----------------------------------------------------------------------------

func TestSeparationOfDuties(t *testing.T) {
	t.Run("nobody approves their own claim, not even an owner", func(t *testing.T) {
		e := newDraft(t)
		self := actor(submitter, tenant.RoleOwner)

		apply(t, e, Command{Action: ActionSubmit, Actor: self})
		_, err := e.Apply(Command{Action: ActionApprove, Actor: self}, fixedClock)

		var ae *AuthorizationError
		if !errors.As(err, &ae) {
			t.Fatalf("self-approval: got %v, want AuthorizationError", err)
		}
		if !errors.Is(err, shared.ErrForbidden) {
			t.Fatalf("AuthorizationError must unwrap to ErrForbidden so the API answers 403, got %v", err)
		}
		if e.Status != StatusPendingApproval {
			t.Fatalf("refused approval mutated the claim: %s", e.Status)
		}
	})

	t.Run("the approver of a claim may not also settle it", func(t *testing.T) {
		e := newDraft(t)
		sub := actor(submitter, tenant.RoleMember)
		// An owner carries both approve and pay, which is exactly why the
		// guard cannot rely on the role matrix alone.
		boss := actor(approver, tenant.RoleOwner)

		apply(t, e, Command{Action: ActionSubmit, Actor: sub})
		apply(t, e, Command{Action: ActionApprove, Actor: boss})

		_, err := e.Apply(Command{Action: ActionPay, Actor: boss, PaymentRef: reason("BACS-1")}, fixedClock)
		if !errors.Is(err, shared.ErrForbidden) {
			t.Fatalf("approver settling their own approval: got %v, want ErrForbidden", err)
		}

		other := actor(payer, tenant.RoleFinance)
		apply(t, e, Command{Action: ActionPay, Actor: other, PaymentRef: reason("BACS-1")})
		if e.Status != StatusPaid {
			t.Fatalf("a third party could not settle: %s", e.Status)
		}
	})

	t.Run("roles that approve cannot pay and roles that pay cannot approve", func(t *testing.T) {
		for _, role := range tenant.AllRoles {
			canApprove := role.Allows(tenant.PermExpenseApprove)
			canPay := role.Allows(tenant.PermExpensePay)
			if canApprove && canPay && role != tenant.RoleOwner {
				t.Errorf("role %s holds both approve and pay; only owner may, and only because "+
					"MustNotBeDecider stops them using both on one claim", role)
			}
		}
	})
}

func TestApprovalLimit(t *testing.T) {
	big := shared.Money{Minor: 900_000, Currency: "USD"} // 9,000.00

	t.Run("a manager cannot approve above their ceiling", func(t *testing.T) {
		e := newDraft(t, func(d *Draft) { d.Amount = big })
		apply(t, e, Command{Action: ActionSubmit, Actor: actor(submitter, tenant.RoleMember)})

		mgr := actor(approver, tenant.RoleManager, scopedTo(deptEng), limit(500_000))
		_, err := e.Apply(Command{Action: ActionApprove, Actor: mgr}, fixedClock)
		if !errors.Is(err, shared.ErrForbidden) {
			t.Fatalf("over-limit approval: got %v, want ErrForbidden", err)
		}
	})

	t.Run("and cannot dispose of it by rejecting either", func(t *testing.T) {
		e := newDraft(t, func(d *Draft) { d.Amount = big })
		apply(t, e, Command{Action: ActionSubmit, Actor: actor(submitter, tenant.RoleMember)})

		mgr := actor(approver, tenant.RoleManager, scopedTo(deptEng), limit(500_000))
		_, err := e.Apply(Command{Action: ActionReject, Actor: mgr, Reason: reason("too much")}, fixedClock)
		if !errors.Is(err, shared.ErrForbidden) {
			t.Fatalf("over-limit rejection: got %v, want ErrForbidden - "+
				"otherwise a manager closes the escalation path for a claim they cannot approve", err)
		}
	})

	t.Run("an unlimited approver can", func(t *testing.T) {
		e := newDraft(t, func(d *Draft) { d.Amount = big })
		apply(t, e, Command{Action: ActionSubmit, Actor: actor(submitter, tenant.RoleMember)})
		apply(t, e, Command{Action: ActionApprove, Actor: actor(approver, tenant.RoleAdmin)})
		if e.Status != StatusApproved {
			t.Fatalf("status = %s", e.Status)
		}
	})

	t.Run("the role default applies when a membership sets no override", func(t *testing.T) {
		e := newDraft(t, func(d *Draft) {
			d.Amount = shared.Money{Minor: tenant.DefaultManagerApprovalLimitMinor + 1, Currency: "USD"}
		})
		apply(t, e, Command{Action: ActionSubmit, Actor: actor(submitter, tenant.RoleMember)})

		mgr := actor(approver, tenant.RoleManager, scopedTo(deptEng)) // no explicit limit
		if err := e.CanApply(Command{Action: ActionApprove, Actor: mgr}); !errors.Is(err, shared.ErrForbidden) {
			t.Fatalf("a manager with no configured limit must fall back to the role default, got %v", err)
		}
	})
}

func TestDepartmentScope(t *testing.T) {
	e := newDraft(t) // deptEng
	apply(t, e, Command{Action: ActionSubmit, Actor: actor(submitter, tenant.RoleMember)})

	outsider := actor(approver, tenant.RoleManager, scopedTo(deptSales))
	if err := e.CanApply(Command{Action: ActionApprove, Actor: outsider}); !errors.Is(err, shared.ErrForbidden) {
		t.Fatalf("a manager approved another department's claim: %v", err)
	}

	insider := actor(approver, tenant.RoleManager, scopedTo(deptEng))
	if err := e.CanApply(Command{Action: ActionApprove, Actor: insider}); err != nil {
		t.Fatalf("a manager could not approve their own department's claim: %v", err)
	}

	t.Run("an unassigned claim needs a tenant-wide approver", func(t *testing.T) {
		u := newDraft(t, func(d *Draft) { d.DepartmentID = nil })
		apply(t, u, Command{Action: ActionSubmit, Actor: actor(submitter, tenant.RoleMember)})

		if err := u.CanApply(Command{Action: ActionApprove, Actor: insider}); !errors.Is(err, shared.ErrForbidden) {
			t.Fatalf("a department-scoped manager approved an unassigned claim: %v", err)
		}
		if err := u.CanApply(Command{Action: ActionApprove, Actor: actor(approver, tenant.RoleAdmin)}); err != nil {
			t.Fatalf("a tenant-wide approver was refused an unassigned claim: %v", err)
		}
	})
}

func TestRoleWithoutPermissionIsRefusedBeforeStateIsConsidered(t *testing.T) {
	e := newDraft(t)
	apply(t, e, Command{Action: ActionSubmit, Actor: actor(submitter, tenant.RoleMember)})

	// A viewer holds no action permission at all. The refusal must be 403 and
	// must not depend on the claim's state, or the difference between 403 and
	// 409 tells a viewer which claims are pending.
	viewer := actor(approver, tenant.RoleViewer)
	for _, action := range AllActions {
		err := e.CanApply(Command{Action: action, Actor: viewer, Reason: reason("x"), PaymentRef: reason("y")})
		if !errors.Is(err, shared.ErrForbidden) {
			t.Errorf("viewer/%s: got %v, want ErrForbidden", action, err)
		}
	}
}

func TestInactiveActorIsRefusedEverything(t *testing.T) {
	e := newDraft(t)

	suspended := actor(submitter, tenant.RoleOwner)
	suspended.Status = tenant.MembershipSuspended
	if err := e.CanApply(Command{Action: ActionSubmit, Actor: suspended}); !errors.Is(err, shared.ErrForbidden) {
		t.Fatalf("suspended member: got %v, want ErrForbidden", err)
	}

	suspendedTenant := actor(submitter, tenant.RoleOwner)
	suspendedTenant.TenantStatus = tenant.StatusSuspended
	if err := e.CanApply(Command{Action: ActionSubmit, Actor: suspendedTenant}); !errors.Is(err, shared.ErrForbidden) {
		t.Fatalf("member of a suspended tenant: got %v, want ErrForbidden", err)
	}
}

func TestActorFromAnotherTenantIsRefused(t *testing.T) {
	e := newDraft(t)
	stranger := actor(submitter, tenant.RoleOwner)
	stranger.TenantID = uuid.New()

	_, err := e.Apply(Command{Action: ActionSubmit, Actor: stranger}, fixedClock)
	if !errors.Is(err, shared.ErrTenantMismatch) {
		t.Fatalf("cross-tenant actor: got %v, want ErrTenantMismatch", err)
	}
}

// -----------------------------------------------------------------------------
// Command completeness
// -----------------------------------------------------------------------------

func TestRejectionRequiresAReasonAndSettlementARef(t *testing.T) {
	blankish := []*string{nil, reason(""), reason("   \t\n ")}

	for _, r := range blankish {
		e := newDraft(t)
		apply(t, e, Command{Action: ActionSubmit, Actor: actor(submitter, tenant.RoleMember)})

		_, err := e.Apply(Command{Action: ActionReject, Actor: actor(approver, tenant.RoleAdmin), Reason: r}, fixedClock)
		if !errors.Is(err, shared.ErrValidation) {
			t.Fatalf("reject with reason %v: got %v, want ErrValidation", r, err)
		}
		if e.Status != StatusPendingApproval {
			t.Fatalf("an incomplete command mutated the claim: %s", e.Status)
		}
	}

	for _, r := range blankish {
		e := newDraft(t)
		apply(t, e, Command{Action: ActionSubmit, Actor: actor(submitter, tenant.RoleMember)})
		apply(t, e, Command{Action: ActionApprove, Actor: actor(approver, tenant.RoleAdmin)})

		_, err := e.Apply(Command{Action: ActionPay, Actor: actor(payer, tenant.RoleFinance), PaymentRef: r}, fixedClock)
		if !errors.Is(err, shared.ErrValidation) {
			t.Fatalf("pay with ref %v: got %v, want ErrValidation", r, err)
		}
	}
}

// -----------------------------------------------------------------------------
// Editing
// -----------------------------------------------------------------------------

func TestOnlyDraftsAreEditable(t *testing.T) {
	edit := Draft{
		DepartmentID: &deptEng,
		Category:     CategoryTravel,
		Amount:       shared.Money{Minor: 999, Currency: "USD"},
		Merchant:     "Trainline",
		SpentAt:      fixedClock.AddDate(0, 0, -1),
	}

	e := newDraft(t)
	if _, err := e.Edit(edit, submitter, fixedClock); err != nil {
		t.Fatalf("editing a draft: %v", err)
	}
	if e.Merchant != "Trainline" || e.Amount.Minor != 999 {
		t.Fatalf("edit did not apply: %+v", e)
	}

	apply(t, e, Command{Action: ActionSubmit, Actor: actor(submitter, tenant.RoleMember)})

	before := *e
	_, err := e.Edit(edit, submitter, fixedClock)
	if !errors.Is(err, shared.ErrConflict) {
		t.Fatalf("editing a submitted claim: got %v, want ErrConflict", err)
	}
	if e.Amount != before.Amount || e.Version != before.Version {
		t.Fatal("a refused edit changed the claim; an approver could then be shown different figures than they approved")
	}
}

func TestDraftValidation(t *testing.T) {
	cases := map[string]func(*Draft){
		"zero amount":     func(d *Draft) { d.Amount.Minor = 0 },
		"negative amount": func(d *Draft) { d.Amount.Minor = -1 },
		"no currency":     func(d *Draft) { d.Amount.Currency = "" },
		"bad currency":    func(d *Draft) { d.Amount.Currency = "usd" },
		"blank merchant":  func(d *Draft) { d.Merchant = "   " },
		"future date":     func(d *Draft) { d.SpentAt = fixedClock.AddDate(0, 0, 2) },
		"bad category":    func(d *Draft) { d.Category = "bribes" },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := Draft{
				Category: CategorySoftware,
				Amount:   shared.Money{Minor: 100, Currency: "USD"},
				Merchant: "Figma",
				SpentAt:  fixedClock.AddDate(0, 0, -1),
			}
			mutate(&d)
			if _, _, err := New(tenantID, submitter, d, fixedClock); !errors.Is(err, shared.ErrValidation) {
				t.Fatalf("got %v, want ErrValidation", err)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// AllowedActions
// -----------------------------------------------------------------------------

// The dashboard renders one button per allowed action. If this list and Apply
// ever disagree, users get buttons that 403 - so it is asserted to be exactly
// the set of actions that succeed.
func TestAllowedActionsMatchesWhatApplyAccepts(t *testing.T) {
	roles := []tenant.Actor{
		actor(submitter, tenant.RoleMember),
		actor(approver, tenant.RoleManager, scopedTo(deptEng)),
		actor(approver, tenant.RoleAdmin),
		actor(payer, tenant.RoleFinance),
		actor(approver, tenant.RoleViewer),
	}

	for _, from := range AllStatuses {
		for _, a := range roles {
			e := newDraft(t)
			e.Status = from
			switch from {
			case StatusPendingApproval:
				e.SubmittedAt = &fixedClock
			case StatusApproved, StatusRejected, StatusPaid:
				e.SubmittedAt = &fixedClock
				e.DecidedAt = &fixedClock
				e.DecidedBy = &approver
			}

			allowed := map[Action]bool{}
			for _, act := range e.AllowedActions(a) {
				allowed[act] = true
			}

			for _, act := range AllActions {
				probe := *e
				_, err := probe.Apply(
					Command{Action: act, Actor: a, Reason: reason("probe"), PaymentRef: reason("probe")},
					fixedClock,
				)
				if (err == nil) != allowed[act] {
					t.Errorf("status=%s role=%s action=%s: AllowedActions says %v, Apply says %v",
						from, a.Role, act, allowed[act], err == nil)
				}
			}
		}
	}
}

// AllowedActions probes with a scratch copy; a bug there would corrupt the
// caller's claim while merely rendering a page.
func TestProbingDoesNotMutate(t *testing.T) {
	e := newDraft(t)
	apply(t, e, Command{Action: ActionSubmit, Actor: actor(submitter, tenant.RoleMember)})
	before := *e

	_ = e.AllowedActions(actor(approver, tenant.RoleOwner))

	if *e != before {
		t.Fatalf("AllowedActions mutated the claim:\n before %+v\n after  %+v", before, *e)
	}
}
