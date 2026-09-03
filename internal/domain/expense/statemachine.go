package expense

import (
	"fmt"
	"strings"
	"time"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/tenant"
)

// The claim lifecycle.
//
//	                    ┌──────────────── withdraw ────────────────┐
//	                    v                                          │
//	  (new) ──────> draft ────── submit ──────> pending_approval ──┤
//	                  ^                              │             │
//	                  │                     approve  │  reject     │
//	                  │                         ┌────┴────┐        │
//	               revise                       v         v        │
//	                  └───────────────────── rejected   approved ──┘
//	                                                       │
//	                                                      pay
//	                                                       v
//	                                                     paid  (terminal)
//
// Three absences are as deliberate as the edges that are present:
//
//   - rejected -> pending_approval does not exist. A rejected claim returns to
//     draft through `revise`, which increments the revision counter. Without
//     that step, a submitter could re-present the identical claim until an
//     approver clicked the wrong button, and the ledger would show two
//     decisions on "the same" claim with nothing distinguishing them. With it,
//     an approver deciding on revision 2 can see what happened to revision 1.
//
//   - approved -> rejected does not exist. Reversing an approval after the
//     fact rewrites a decision someone made; a mistaken approval is corrected
//     by a compensating claim, which leaves both facts in the ledger.
//
//   - paid has no outgoing edges at all. Money has left the company.

// Command is an attempt to move a claim. It carries the actor rather than
// taking one as a separate argument so that a caller cannot accidentally
// authorise one actor's request with another's credentials.
type Command struct {
	Action Action
	Actor  tenant.Actor

	// Reason is required to reject. An approver who rejects without saying why
	// generates a support ticket and a resubmission of the same claim.
	Reason *string

	// PaymentRef is required to settle: the bank reference, payment run id, or
	// whatever the finance team can trace the transfer by. A `paid` row with no
	// reference cannot be reconciled against a bank statement.
	PaymentRef *string
}

// transition is one edge, together with everything that has to be true for it
// to be taken.
//
// The guards are fields rather than closures on purpose. A table of data can
// be printed, diffed and asserted against in a test; a table of function
// pointers can only be executed. TestTransitionTableIsExhaustive reads these
// fields directly.
type transition struct {
	From   Status
	Action Action
	To     Status

	// Permission the actor's role must carry. Necessary, never sufficient.
	Permission tenant.Permission

	// Event recorded in the ledger.
	Event EventAction

	// MustBeSubmitter restricts the edge to the person who filed the claim:
	// submitting, withdrawing and revising are things you do to your own work.
	MustBeSubmitter bool

	// MustNotBeSubmitter is separation of duties. Nobody approves, rejects or
	// settles their own claim, whatever their role - and an owner holds every
	// permission there is, so a role check alone would not stop it.
	MustNotBeSubmitter bool

	// MustNotBeDecider stops the same person from approving a claim and then
	// paying it. Role separation already prevents this for admin and finance,
	// but an owner carries both permissions and would otherwise be able to move
	// money end to end alone.
	MustNotBeDecider bool

	// WithinApprovalLimit checks the amount against the actor's ceiling.
	//
	// Applied to rejection as well as approval, which is not obvious. The
	// argument for exempting rejection is that refusing money is always safe;
	// the argument against - which wins - is that a manager who cannot approve
	// a 50,000 claim should not be able to dispose of it either, because that
	// closes the escalation path without anyone senior ever seeing it.
	WithinApprovalLimit bool

	// WithinDepartmentScope confines a department-scoped manager to their own
	// department's claims.
	WithinDepartmentScope bool

	RequiresReason     bool
	RequiresPaymentRef bool

	// BumpsRevision marks this edge as starting a new draft iteration.
	BumpsRevision bool
}

// transitions is the entire state machine. Every legal move in the product is
// a row here, and a move that is not a row is not legal.
var transitions = []transition{
	{
		From: StatusDraft, Action: ActionSubmit, To: StatusPendingApproval,
		Permission: tenant.PermExpenseSubmit, Event: EventSubmitted,
		MustBeSubmitter: true,
	},
	{
		From: StatusPendingApproval, Action: ActionApprove, To: StatusApproved,
		Permission: tenant.PermExpenseApprove, Event: EventApproved,
		MustNotBeSubmitter: true, WithinApprovalLimit: true, WithinDepartmentScope: true,
	},
	{
		From: StatusPendingApproval, Action: ActionReject, To: StatusRejected,
		Permission: tenant.PermExpenseApprove, Event: EventRejected,
		MustNotBeSubmitter: true, WithinApprovalLimit: true, WithinDepartmentScope: true,
		RequiresReason: true,
	},
	{
		// Withdrawing does not bump the revision: nobody decided anything, so
		// there is no decision for a future approver to need context on.
		From: StatusPendingApproval, Action: ActionWithdraw, To: StatusDraft,
		Permission: tenant.PermExpenseSubmit, Event: EventWithdrawn,
		MustBeSubmitter: true,
	},
	{
		From: StatusRejected, Action: ActionRevise, To: StatusDraft,
		Permission: tenant.PermExpenseCreate, Event: EventRevised,
		MustBeSubmitter: true, BumpsRevision: true,
	},
	{
		From: StatusApproved, Action: ActionPay, To: StatusPaid,
		Permission: tenant.PermExpensePay, Event: EventPaid,
		MustNotBeSubmitter: true, MustNotBeDecider: true, RequiresPaymentRef: true,
	},
}

// index is the lookup built once at init. The table above stays in lifecycle
// order for reading; this makes Apply a map hit rather than a scan.
type edgeKey struct {
	from   Status
	action Action
}

var (
	index            = map[edgeKey]transition{}
	actionPermission = map[Action]tenant.Permission{}
)

func init() {
	for _, t := range transitions {
		key := edgeKey{t.From, t.Action}
		if _, dup := index[key]; dup {
			// A duplicate edge means two rows disagree about what an action
			// does from a state, and which one wins would depend on map
			// iteration order. Failing at process start is the only safe
			// response; there is no request to return an error to yet.
			panic(fmt.Sprintf("expense: duplicate transition %s/%s", t.From, t.Action))
		}
		index[key] = t

		// One action must map to one permission across every state it appears
		// in, or the pre-flight permission check in Apply would depend on which
		// state the claim happened to be in.
		if existing, ok := actionPermission[t.Action]; ok && existing != t.Permission {
			panic(fmt.Sprintf("expense: action %s requires %s in one transition and %s in another",
				t.Action, existing, t.Permission))
		}
		actionPermission[t.Action] = t.Permission
	}
}

// TransitionError means the claim is not in a state this action applies to.
// It unwraps to ErrConflict, which the HTTP layer renders as 409: the request
// was well formed and the caller may be perfectly entitled to it, but the
// world moved.
type TransitionError struct {
	From   Status
	Action Action
	Reason string
}

func (e *TransitionError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("cannot %s a claim in %s: %s", e.Action, e.From, e.Reason)
	}
	return fmt.Sprintf("cannot %s a claim in %s", e.Action, e.From)
}

func (e *TransitionError) Unwrap() error { return shared.ErrConflict }

// AuthorizationError means the transition exists and the claim is in the right
// state, but this actor may not take it. It unwraps to ErrForbidden -> 403.
//
// Keeping this distinct from TransitionError matters for the dashboard: a 409
// means "reload and look again", a 403 means "ask someone else".
type AuthorizationError struct {
	Action Action
	Reason string
}

func (e *AuthorizationError) Error() string {
	return fmt.Sprintf("not permitted to %s this claim: %s", e.Action, e.Reason)
}

func (e *AuthorizationError) Unwrap() error { return shared.ErrForbidden }

// Apply performs a transition, mutating the claim and returning the ledger
// entry that describes it.
//
// The claim is left untouched if any check fails: every guard runs before the
// first field is written. A caller that ignores the error therefore still
// holds the object it loaded, rather than a half-moved one.
//
// The order of the checks is chosen so that the error a caller sees is the
// most actionable one:
//
//  1. Is the actor able to act at all (active membership, live tenant)?
//  2. Does their role carry this action's permission?  -> 403
//  3. Does this edge exist from the current state?     -> 409
//  4. Do the per-claim guards hold?                    -> 403
//  5. Is the command itself complete?                  -> 422
//
// Putting (3) before (2) would tell a member which claims are pending by the
// difference between a 409 and a 403.
func (e *Expense) Apply(cmd Command, now time.Time) (Event, error) {
	actor := cmd.Actor

	if !cmd.Action.Valid() {
		return Event{}, shared.FieldError{Field: "action", Detail: fmt.Sprintf("%q is not a known action", cmd.Action)}
	}
	if actor.TenantID != e.TenantID {
		// Unreachable through the HTTP layer, where RLS has already filtered
		// the row out and the load returned ErrNotFound. Reachable from the
		// worker, which loads claims for one tenant and could be handed an
		// actor for another by a malformed job payload.
		return Event{}, shared.ErrTenantMismatch
	}
	if !actor.Active() {
		return Event{}, &AuthorizationError{Action: cmd.Action, Reason: "membership is not active"}
	}

	if perm, ok := actionPermission[cmd.Action]; ok && !actor.Can(perm) {
		return Event{}, &AuthorizationError{
			Action: cmd.Action,
			Reason: fmt.Sprintf("role %s does not carry %s", actor.Role, perm),
		}
	}

	t, ok := index[edgeKey{e.Status, cmd.Action}]
	if !ok {
		return Event{}, &TransitionError{From: e.Status, Action: cmd.Action, Reason: suggest(e.Status, cmd.Action)}
	}

	if err := e.checkGuards(t, cmd); err != nil {
		return Event{}, err
	}

	return e.commit(t, cmd, now), nil
}

// checkGuards evaluates the per-claim conditions of an edge. Split out from
// Apply so that CanApply can ask the same question without mutating anything -
// two code paths answering the same question differently is the bug class this
// avoids.
func (e *Expense) checkGuards(t transition, cmd Command) error {
	actor := cmd.Actor

	if t.MustBeSubmitter && !actor.SameMembership(e.SubmitterID) {
		return &AuthorizationError{Action: t.Action, Reason: "only the person who filed a claim may " + string(t.Action) + " it"}
	}
	if t.MustNotBeSubmitter && actor.SameMembership(e.SubmitterID) {
		return &AuthorizationError{Action: t.Action, Reason: "you cannot " + string(t.Action) + " your own claim"}
	}
	if t.MustNotBeDecider && e.DecidedBy != nil && actor.SameMembership(*e.DecidedBy) {
		return &AuthorizationError{Action: t.Action, Reason: "the approver of a claim may not also settle it"}
	}
	if t.WithinDepartmentScope && !actor.GovernsDepartment(e.DepartmentID) {
		return &AuthorizationError{Action: t.Action, Reason: "this claim belongs to another department"}
	}
	if t.WithinApprovalLimit && !actor.WithinApprovalLimit(e.Amount) {
		return &AuthorizationError{
			Action: t.Action,
			Reason: fmt.Sprintf("claim of %s exceeds your approval limit; escalate to a tenant-wide approver",
				e.Amount.String()),
		}
	}

	var v shared.Validator
	if t.RequiresReason && blank(cmd.Reason) {
		v.Add("reason", "is required when rejecting a claim")
	}
	if t.RequiresPaymentRef && blank(cmd.PaymentRef) {
		v.Add("payment_ref", "is required when settling a claim")
	}
	if cmd.Reason != nil && len(*cmd.Reason) > 2000 {
		v.Add("reason", "must be at most 2000 characters")
	}
	if cmd.PaymentRef != nil && len(*cmd.PaymentRef) > 200 {
		v.Add("payment_ref", "must be at most 200 characters")
	}
	return v.Err()
}

// commit applies the edge. Every write to the entity happens here and nowhere
// else, which is what makes "the claim is untouched on error" checkable rather
// than hoped for.
func (e *Expense) commit(t transition, cmd Command, now time.Time) Event {
	from := e.Status
	actorID := cmd.Actor.MembershipID

	e.Status = t.To
	e.Version++
	e.UpdatedAt = now

	// The timestamp columns are maintained to match expenses_status_timestamps_chk
	// exactly. When this switch and that constraint disagree, the write fails
	// loudly at the database rather than persisting an inconsistent row - which
	// is why the constraint is written out in full rather than trusting this.
	switch t.Action {
	case ActionSubmit:
		e.SubmittedAt = ptr(now)

	case ActionApprove, ActionReject:
		e.DecidedAt = ptr(now)
		e.DecidedBy = ptr(actorID)
		e.DecisionNote = trimmed(cmd.Reason)

	case ActionWithdraw, ActionRevise:
		// Back to draft: clear the whole decision trail. The claim must look
		// like a fresh draft to the CHECK constraint, and the history of what
		// was cleared lives in the ledger, which is the only place it belongs.
		e.SubmittedAt = nil
		e.DecidedAt = nil
		e.DecidedBy = nil
		e.DecisionNote = nil
		if t.BumpsRevision {
			e.Revision++
		}

	case ActionPay:
		e.PaidAt = ptr(now)
		e.PaymentRef = trimmed(cmd.PaymentRef)
	}

	return Event{
		TenantID:   e.TenantID,
		ExpenseID:  e.ID,
		Action:     t.Event,
		FromStatus: &from,
		ToStatus:   t.To,
		ActorID:    &actorID,
		Reason:     trimmed(cmd.Reason),
		Amount:     e.Amount,
		Revision:   e.Revision,
		OccurredAt: now,
	}
}

// CanApply reports whether Apply would succeed, without mutating anything.
//
// It exists for the list endpoint, which returns the set of actions each claim
// offers the caller so the dashboard can render buttons that work. It runs the
// identical guards - not a summary of them - so a button that appears is a
// button that will not 403.
func (e *Expense) CanApply(cmd Command) error {
	probe := *e
	_, err := probe.Apply(cmd, time.Now())
	return err
}

// AllowedActions lists what this actor may do to this claim right now.
//
// Commands that need a reason or a payment reference are probed with a
// placeholder: the question here is "would the state and the actor allow it",
// not "is this particular request complete", and the real call validates the
// real input.
func (e *Expense) AllowedActions(actor tenant.Actor) []Action {
	placeholder := "probe"
	out := make([]Action, 0, len(AllActions))
	for _, a := range AllActions {
		cmd := Command{Action: a, Actor: actor, Reason: &placeholder, PaymentRef: &placeholder}
		if e.CanApply(cmd) == nil {
			out = append(out, a)
		}
	}
	return out
}

// suggest turns a dead end into a usable message. The rejected/submit case is
// the one users actually hit: they see a rejected claim, want it looked at
// again, and reach for the button they used the first time.
func suggest(from Status, action Action) string {
	switch {
	case from == StatusRejected && action == ActionSubmit:
		return "revise it into a new draft first, so the approver can see it changed"
	case from == StatusPaid:
		return "a settled claim is final; file a compensating claim instead"
	case from == StatusApproved && action == ActionReject:
		return "an approval is not reversible; file a compensating claim instead"
	case from == StatusDraft && (action == ActionApprove || action == ActionReject):
		return "it has not been submitted for approval yet"
	default:
		return ""
	}
}

func blank(s *string) bool { return s == nil || strings.TrimSpace(*s) == "" }

func trimmed(s *string) *string {
	if blank(s) {
		return nil
	}
	t := strings.TrimSpace(*s)
	return &t
}

func ptr[T any](v T) *T { return &v }
