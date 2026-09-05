// Package notify renders and sends the messages the background jobs produce.
//
// It knows nothing about the database. Whoever calls it has already worked out
// who should be told and what happened, and passes both in - which is what
// lets the rendering be tested against a table of cases rather than against a
// seeded organisation, and what keeps the SMTP client out of the transaction
// that decided to send anything.
package notify

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/expense"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
)

// Recipient is somebody to tell.
type Recipient struct {
	Email string
	Name  string
}

// Valid reports whether the address is worth attempting.
//
// A malformed address in the database is not a reason to fail a whole batch:
// it is dropped, and the send proceeds for everybody else. The alternative is
// one bad row silencing every notification for an organisation.
func (r Recipient) Valid() bool {
	if r.Email == "" {
		return false
	}
	_, err := mail.ParseAddress(r.Email)
	return err == nil
}

// String renders the address for an SMTP header, quoting the display name.
func (r Recipient) String() string {
	if r.Name == "" {
		return r.Email
	}
	return (&mail.Address{Name: r.Name, Address: r.Email}).String()
}

// Message is a rendered email.
//
// Both bodies are always produced. A text/plain part is not a courtesy: mail
// clients that refuse HTML, and the spam filters in front of them, treat a
// multipart message with no readable alternative far worse than one with.
type Message struct {
	To       []Recipient
	Subject  string
	Text     string
	HTML     string
	Category string // for logging and metrics, never sent
}

// Sender delivers a rendered message. It is an interface so the worker can be
// wired to a recorder in tests and to SMTP in production, and so a deployment
// without mail configured can drop messages deliberately rather than crash.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

var ErrNoRecipients = errors.New("message has no deliverable recipients")

// ExpenseEvent is everything a claim notification needs. The caller resolves
// it inside the transaction that read the claim.
type ExpenseEvent struct {
	To []Recipient

	TenantName string
	ExpenseID  uuid.UUID
	Action     expense.Action
	Status     expense.Status

	Merchant       string
	Amount         shared.Money
	SpentAt        time.Time
	SubmitterName  string
	DecidedByName  string
	DecisionNote   *string
	PaymentRef     *string
	DepartmentName *string
	Revision       int32
}

// BudgetEvent is a threshold breach.
type BudgetEvent struct {
	To []Recipient

	TenantName     string
	DepartmentName string
	Budget         shared.Money
	Consumed       shared.Money
	Remaining      shared.Money
	UsageBps       int64
	ThresholdBps   int32
	PeriodStart    time.Time
	PeriodEnd      time.Time
}

// Notifier turns events into messages and sends them.
type Notifier struct {
	sender    Sender
	dashboard string
	templates *templates
}

// New builds a notifier. dashboardURL is used to link back to the claim; an
// empty one simply omits the links rather than producing "/expenses/..." which
// resolves to nothing in a mail client.
func New(sender Sender, dashboardURL string) (*Notifier, error) {
	if sender == nil {
		return nil, errors.New("notify: a sender is required")
	}
	tpl, err := loadTemplates()
	if err != nil {
		return nil, err
	}
	return &Notifier{
		sender:    sender,
		dashboard: strings.TrimRight(dashboardURL, "/"),
		templates: tpl,
	}, nil
}

// ExpenseTransition tells the interested parties about a decision.
func (n *Notifier) ExpenseTransition(ctx context.Context, e ExpenseEvent) error {
	to := deliverable(e.To)
	if len(to) == 0 {
		// Not an error the caller can act on. A claim whose only approver has
		// been suspended has nobody to tell, and failing the job would retry
		// that forever.
		return nil
	}

	msg, err := n.templates.expense(n.dashboard, e, to)
	if err != nil {
		return err
	}
	return n.sender.Send(ctx, msg)
}

func (n *Notifier) BudgetThreshold(ctx context.Context, e BudgetEvent) error {
	to := deliverable(e.To)
	if len(to) == 0 {
		return nil
	}

	msg, err := n.templates.budget(n.dashboard, e, to)
	if err != nil {
		return err
	}
	return n.sender.Send(ctx, msg)
}

func deliverable(in []Recipient) []Recipient {
	seen := make(map[string]struct{}, len(in))
	out := make([]Recipient, 0, len(in))

	for _, r := range in {
		if !r.Valid() {
			continue
		}
		// Addresses are compared case-insensitively for deduplication only.
		// The local part is technically case-sensitive, so the address is sent
		// as it was stored - this only stops one person receiving two copies
		// because they are both an owner and the department head.
		key := strings.ToLower(r.Email)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, r)
	}
	return out
}

// LoggingSender records what would have been sent.
//
// It is what a deployment with no SMTP configured uses, and it is deliberately
// not a silent discard: an operator looking at the log can see that
// notifications are being produced and where they would have gone, which is
// the difference between "mail is not configured" and "the notification code
// is broken".
type LoggingSender struct {
	Log func(ctx context.Context, m Message)
}

func (s LoggingSender) Send(ctx context.Context, m Message) error {
	if s.Log != nil {
		s.Log(ctx, m)
	}
	return nil
}

// RecordingSender keeps messages in memory, for tests.
type RecordingSender struct {
	Messages []Message
}

func (s *RecordingSender) Send(_ context.Context, m Message) error {
	s.Messages = append(s.Messages, m)
	return nil
}

func (s *RecordingSender) Last() (Message, error) {
	if len(s.Messages) == 0 {
		return Message{}, fmt.Errorf("no messages recorded")
	}
	return s.Messages[len(s.Messages)-1], nil
}
