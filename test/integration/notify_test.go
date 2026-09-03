//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/expense"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/logger"
	"github.com/mlkad/b2b-expense-tracker/internal/notify"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/worker"
)

// mailpitMessage is the shape the API returns for a received message.
type mailpitMessage struct {
	ID      string `json:"ID"`
	Subject string `json:"Subject"`
	To      []struct {
		Address string `json:"Address"`
		Name    string `json:"Name"`
	} `json:"To"`
	From struct {
		Address string `json:"Address"`
		Name    string `json:"Name"`
	} `json:"From"`
	Text string `json:"Text"`
	HTML string `json:"HTML"`
}

func smtpSenderForTest(t *testing.T) *notify.SMTPSender {
	t.Helper()

	host, port, err := splitHostPort(mailSMTP)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := notify.NewSMTPSender(notify.SMTPConfig{
		Host: host, Port: port,
		// Mailpit is on loopback and takes no credentials; anything else would
		// be refused by the sender's own guard.
		TLS:  notify.TLSNone,
		From: notify.Recipient{Name: "Expense Tracker", Email: "noreply@acme.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return sender
}

func splitHostPort(addr string) (string, int, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", 0, fmt.Errorf("%q is not host:port", addr)
	}
	port, err := strconv.Atoi(addr[i+1:])
	return addr[:i], port, err
}

// clearMailbox empties Mailpit so each test reads only its own messages.
func clearMailbox(t *testing.T) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, mailAPI+"/api/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("clear mailbox: %v", err)
	}
	resp.Body.Close()
}

// waitForMessage polls until one message has arrived. SMTP delivery is not
// synchronous with the Send call returning, so a bare read races it.
func waitForMessage(t *testing.T, wantSubjectContains string) mailpitMessage {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(mailAPI + "/api/v1/messages?limit=20")
		if err != nil {
			t.Fatalf("list messages: %v", err)
		}
		var list struct {
			Messages []mailpitMessage `json:"messages"`
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatalf("decode message list: %v (%s)", err, body)
		}

		for _, m := range list.Messages {
			if wantSubjectContains == "" || strings.Contains(m.Subject, wantSubjectContains) {
				return fetchMessage(t, m.ID)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("no message matching %q arrived within the deadline", wantSubjectContains)
	return mailpitMessage{}
}

func fetchMessage(t *testing.T, id string) mailpitMessage {
	t.Helper()
	resp, err := http.Get(mailAPI + "/api/v1/message/" + url.PathEscape(id))
	if err != nil {
		t.Fatalf("fetch message: %v", err)
	}
	defer resp.Body.Close()

	var m mailpitMessage
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	return m
}

// The message building is hand-written, so it is verified against a server
// that parses mail for a living rather than against my own reading of the RFCs.
func TestMessagesArriveAndParse(t *testing.T) {
	clearMailbox(t)

	n, err := notify.New(smtpSenderForTest(t), "https://app.acme.test")
	if err != nil {
		t.Fatal(err)
	}

	note := "no receipt attached"
	err = n.ExpenseTransition(context.Background(), notify.ExpenseEvent{
		To: []notify.Recipient{
			{Email: "ada@acme.test", Name: "Ada Lovelace"},
			{Email: "grace@acme.test", Name: "Grace Hopper"},
		},
		TenantName:    "Acme Ltd",
		ExpenseID:     uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		Action:        expense.ActionReject,
		Status:        expense.StatusRejected,
		Merchant:      "Café Ünïcödé",
		Amount:        shared.Money{Minor: 12500, Currency: "USD"},
		SpentAt:       time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		SubmitterName: "Ada Lovelace",
		DecidedByName: "Grace Hopper",
		DecisionNote:  &note,
		Revision:      1,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	msg := waitForMessage(t, "rejected")

	t.Run("headers", func(t *testing.T) {
		if msg.From.Address != "noreply@acme.test" || msg.From.Name != "Expense Tracker" {
			t.Errorf("from = %+v", msg.From)
		}
		if len(msg.To) != 2 {
			t.Fatalf("delivered to %d recipients, want 2", len(msg.To))
		}
		// The display name survives the round trip, which is the part quoting
		// gets wrong.
		if msg.To[0].Name != "Ada Lovelace" {
			t.Errorf("recipient name = %q", msg.To[0].Name)
		}
	})

	t.Run("a non-ASCII subject decodes back to what was written", func(t *testing.T) {
		// Encoded on the way out as an RFC 2047 encoded-word; if that were
		// wrong the server would report the raw bytes or mojibake.
		if !strings.Contains(msg.Subject, "Café Ünïcödé") {
			t.Errorf("subject = %q, want the accented merchant name intact", msg.Subject)
		}
	})

	t.Run("both bodies arrive and agree", func(t *testing.T) {
		if msg.Text == "" {
			t.Error("no text/plain part; spam filters treat that far worse")
		}
		if msg.HTML == "" {
			t.Error("no text/html part")
		}
		for _, want := range []string{"Café Ünïcödé", "125.00", "Grace Hopper", "no receipt attached"} {
			if !strings.Contains(msg.Text, want) {
				t.Errorf("the text body is missing %q:\n%s", want, msg.Text)
			}
			if !strings.Contains(msg.HTML, want) {
				t.Errorf("the html body is missing %q", want)
			}
		}
		if !strings.Contains(msg.HTML, "https://app.acme.test/expenses/11111111-2222-3333-4444-555555555555") {
			t.Error("no link back to the claim in the html body")
		}
	})

	// Quoted-printable exists because SMTP has a 998-octet line limit and the
	// HTML bodies exceed it. If the encoding were wrong the markup would
	// arrive with "=" and line breaks in the middle of tags.
	t.Run("long html lines survive the transfer encoding", func(t *testing.T) {
		if strings.Contains(msg.HTML, "=\n") || strings.Contains(msg.HTML, "=3D") {
			t.Errorf("the html arrived still quoted-printable encoded:\n%s", msg.HTML[:min(400, len(msg.HTML))])
		}
		if !strings.Contains(msg.HTML, `<a href="https://app.acme.test`) {
			t.Errorf("a tag was broken by line wrapping")
		}
	})
}

// The whole path: a claim is submitted, the worker job runs, and the approvers
// receive an email - resolved from the database, not from the job payload.
func TestSubmittingAClaimEmailsTheApprovers(t *testing.T) {
	clearMailbox(t)

	o := seedOrg(t, "notify-submit")
	claim := seedClaim(t, o, "pending_approval", 4200)

	n, err := notify.New(smtpSenderForTest(t), "https://app.acme.test")
	if err != nil {
		t.Fatal(err)
	}
	handlers := workerHandlersForTest(t, n)

	if err := handlers.HandleExpenseTransition(context.Background(),
		expenseTask(t, o.TenantID, claim, expense.ActionSubmit)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	msg := waitForMessage(t, "needs approval")

	// The manager is scoped to Engineering, which is the claim's department,
	// so they are told. Finance is not an approver and must not be.
	var addresses []string
	for _, to := range msg.To {
		addresses = append(addresses, to.Address)
	}
	joined := strings.Join(addresses, ",")

	if !strings.Contains(joined, "manager-notify-submit@example.test") {
		t.Errorf("the department's manager was not told: %v", addresses)
	}
	if strings.Contains(joined, "finance-notify-submit@example.test") {
		t.Errorf("finance was told about a claim they cannot decide on: %v", addresses)
	}
	// The person who filed it knows they filed it.
	if strings.Contains(joined, "member-notify-submit@example.test") {
		t.Errorf("the submitter was sent their own submission: %v", addresses)
	}
}

// A decision goes back to the person who filed the claim, not to the approvers
// who already know.
func TestApprovingAClaimEmailsTheSubmitter(t *testing.T) {
	clearMailbox(t)

	o := seedOrg(t, "notify-approve")
	claim := seedClaim(t, o, "approved", 9900)

	n, err := notify.New(smtpSenderForTest(t), "https://app.acme.test")
	if err != nil {
		t.Fatal(err)
	}
	handlers := workerHandlersForTest(t, n)

	if err := handlers.HandleExpenseTransition(context.Background(),
		expenseTask(t, o.TenantID, claim, expense.ActionApprove)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	msg := waitForMessage(t, "approved")

	if len(msg.To) != 1 || msg.To[0].Address != "member-notify-approve@example.test" {
		t.Fatalf("delivered to %+v, want only the submitter", msg.To)
	}
	if !strings.Contains(msg.Text, "Engineering") {
		t.Errorf("the department name was not resolved:\n%s", msg.Text)
	}
}

// A job naming a claim that has since been deleted must not be retried
// forever.
func TestNotificationForAMissingClaimIsNotAnError(t *testing.T) {
	clearMailbox(t)

	o := seedOrg(t, "notify-missing")
	n, err := notify.New(smtpSenderForTest(t), "")
	if err != nil {
		t.Fatal(err)
	}
	handlers := workerHandlersForTest(t, n)

	err = handlers.HandleExpenseTransition(context.Background(),
		expenseTask(t, o.TenantID, uuid.New(), expense.ActionApprove))
	if err != nil {
		t.Fatalf("got %v, want the job to succeed and do nothing", err)
	}
}

// workerHandlersForTest builds the worker with a real notifier.
func workerHandlersForTest(t *testing.T, n worker.Notifier) *worker.Handlers {
	t.Helper()
	tenancy := repo.NewTenancyRepository()
	return worker.NewHandlers(
		app,
		repo.NewExpenseRepository(),
		repo.NewBudgetRepository(),
		tenancy,
		repo.NewOrgRepository(),
		nil, // the billing service is not on this path
		nil, // nor is the queue: no sweep fans out here
		n,
		logger.New(logger.ParseLevel("error"), logger.FormatText, "integration", "test"),
	)
}

func expenseTask(t *testing.T, tenantID, expenseID uuid.UUID, action expense.Action) *asynq.Task {
	t.Helper()
	payload, err := json.Marshal(worker.ExpenseTransitionPayload{
		TenantID: tenantID, ExpenseID: expenseID, Action: action,
	})
	if err != nil {
		t.Fatal(err)
	}
	return asynq.NewTask(worker.TaskExpenseTransition, payload)
}
