// Package expense owns the expense claim and the finite state machine that
// governs its life.
//
// Everything that decides whether a claim may move is in this package, and
// nothing in this package performs I/O. A service loads a claim, asks it to
// apply a command, and persists whatever comes back; it never sets a status
// itself. That is what makes the invariants checkable by reading one file
// instead of auditing every call site.
package expense

import (
	"fmt"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
)

// Status mirrors the expense_status enum in migration 00004.
type Status string

const (
	StatusDraft           Status = "draft"
	StatusPendingApproval Status = "pending_approval"
	StatusApproved        Status = "approved"
	StatusRejected        Status = "rejected"
	StatusPaid            Status = "paid"
)

// AllStatuses is the closed set. Tests iterate it to prove the transition
// table covers every state, so a status added here without a corresponding
// entry fails the suite rather than becoming a silent dead end.
var AllStatuses = []Status{
	StatusDraft, StatusPendingApproval, StatusApproved, StatusRejected, StatusPaid,
}

func (s Status) Valid() bool {
	switch s {
	case StatusDraft, StatusPendingApproval, StatusApproved, StatusRejected, StatusPaid:
		return true
	}
	return false
}

// Terminal reports whether a claim in this status will never move again.
//
// Only paid is terminal. Rejected is not: a rejected claim can be revised into
// a new draft, which is the whole reason the state machine has no direct
// rejected -> pending_approval edge.
func (s Status) Terminal() bool { return s == StatusPaid }

// Editable reports whether the claim's own fields - amount, merchant, dates -
// may still be changed. Only a draft is: once an approver has seen a claim,
// changing what they approved without another decision is the fraud this
// product exists to make impossible.
func (s Status) Editable() bool { return s == StatusDraft }

// CountsAgainstBudget reports whether a claim in this status consumes its
// department's envelope.
//
// Approved and paid, not pending. A pending claim is a request, and counting
// requests would let anybody exhaust a budget on paper by submitting claims
// nobody has agreed to. This predicate is exactly the WHERE clause of
// expenses_budget_rollup_idx; changing one without the other drops the
// rollup query onto a sequential scan.
func (s Status) CountsAgainstBudget() bool {
	return s == StatusApproved || s == StatusPaid
}

func ParseStatus(s string) (Status, error) {
	status := Status(s)
	if !status.Valid() {
		return "", shared.FieldError{Field: "status", Detail: fmt.Sprintf("%q is not a known expense status", s)}
	}
	return status, nil
}

// Action is a command a caller may attempt against a claim. Actions are named
// for what the caller intends, not for the state they produce, because two
// actions can lead to the same state for entirely different reasons -
// withdrawing a pending claim and revising a rejected one both end at draft.
type Action string

const (
	ActionSubmit   Action = "submit"
	ActionApprove  Action = "approve"
	ActionReject   Action = "reject"
	ActionWithdraw Action = "withdraw"
	ActionRevise   Action = "revise"
	ActionPay      Action = "pay"
)

var AllActions = []Action{
	ActionSubmit, ActionApprove, ActionReject, ActionWithdraw, ActionRevise, ActionPay,
}

func (a Action) Valid() bool {
	switch a {
	case ActionSubmit, ActionApprove, ActionReject, ActionWithdraw, ActionRevise, ActionPay:
		return true
	}
	return false
}

// EventAction mirrors the expense_action enum, which is a superset of Action:
// it also records the two things that happen to a claim without being
// transitions - its creation, and edits to a draft.
type EventAction string

const (
	EventCreated   EventAction = "created"
	EventUpdated   EventAction = "updated"
	EventSubmitted EventAction = "submitted"
	EventApproved  EventAction = "approved"
	EventRejected  EventAction = "rejected"
	EventWithdrawn EventAction = "withdrawn"
	EventRevised   EventAction = "revised"
	EventPaid      EventAction = "paid"
)
