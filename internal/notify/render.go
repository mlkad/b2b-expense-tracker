package notify

import (
	"fmt"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/expense"
)

// templates holds the parsed bodies.
//
// The HTML and text versions are separate template sets on purpose, and the
// HTML one uses html/template rather than text/template. That is the whole
// escaping story: a merchant named `<script>` reaches these bodies unchanged
// from the database, and text/template would render it verbatim into an email
// that some clients execute.
type templates struct {
	expenseText *texttemplate.Template
	expenseHTML *htmltemplate.Template
	budgetText  *texttemplate.Template
	budgetHTML  *htmltemplate.Template
}

func loadTemplates() (*templates, error) {
	funcs := texttemplate.FuncMap{
		"date":    func(t time.Time) string { return t.UTC().Format("2 January 2006") },
		"percent": func(bps int64) string { return fmt.Sprintf("%.1f%%", float64(bps)/100) },
	}

	t := &templates{}
	var err error

	if t.expenseText, err = texttemplate.New("expense.txt").Funcs(funcs).Parse(expenseTextBody); err != nil {
		return nil, fmt.Errorf("parse expense text template: %w", err)
	}
	if t.expenseHTML, err = htmltemplate.New("expense.html").Funcs(htmltemplate.FuncMap(funcs)).Parse(expenseHTMLBody); err != nil {
		return nil, fmt.Errorf("parse expense html template: %w", err)
	}
	if t.budgetText, err = texttemplate.New("budget.txt").Funcs(funcs).Parse(budgetTextBody); err != nil {
		return nil, fmt.Errorf("parse budget text template: %w", err)
	}
	if t.budgetHTML, err = htmltemplate.New("budget.html").Funcs(htmltemplate.FuncMap(funcs)).Parse(budgetHTMLBody); err != nil {
		return nil, fmt.Errorf("parse budget html template: %w", err)
	}
	return t, nil
}

// expenseView is what the templates see. A view type rather than the event
// itself, so a field added to ExpenseEvent does not silently appear in an
// email nobody reviewed.
type expenseView struct {
	Headline     string
	TenantName   string
	Merchant     string
	Amount       string
	Currency     string
	SpentAt      time.Time
	Submitter    string
	DecidedBy    string
	Note         string
	PaymentRef   string
	Department   string
	Revision     int32
	Link         string
	CallToAction string
}

func (t *templates) expense(dashboard string, e ExpenseEvent, to []Recipient) (Message, error) {
	headline, subject, cta := expenseWording(e)

	view := expenseView{
		Headline:     headline,
		TenantName:   e.TenantName,
		Merchant:     e.Merchant,
		Amount:       e.Amount.String(),
		Currency:     string(e.Amount.Currency),
		SpentAt:      e.SpentAt,
		Submitter:    e.SubmitterName,
		DecidedBy:    e.DecidedByName,
		Department:   derefOr(e.DepartmentName, "Unassigned"),
		Revision:     e.Revision,
		CallToAction: cta,
	}
	if e.DecisionNote != nil {
		view.Note = *e.DecisionNote
	}
	if e.PaymentRef != nil {
		view.PaymentRef = *e.PaymentRef
	}
	if dashboard != "" {
		view.Link = fmt.Sprintf("%s/expenses/%s", dashboard, e.ExpenseID)
	}

	text, err := render(t.expenseText, view)
	if err != nil {
		return Message{}, err
	}
	html, err := renderHTML(t.expenseHTML, view)
	if err != nil {
		return Message{}, err
	}

	return Message{
		To: to, Subject: subject, Text: text, HTML: html,
		Category: "expense." + string(e.Action),
	}, nil
}

// expenseWording picks the sentence for each transition.
//
// One switch rather than a template branching on the action: the subject line
// is the part a recipient reads before deciding whether to open anything, and
// having them side by side is the only way to see that they read consistently.
func expenseWording(e ExpenseEvent) (headline, subject, cta string) {
	amount := e.Amount.String() + " " + string(e.Amount.Currency)

	switch e.Action {
	case expense.ActionSubmit:
		if e.Revision > 1 {
			return fmt.Sprintf("%s resubmitted a claim for %s", e.SubmitterName, amount),
				fmt.Sprintf("[%s] Revised claim needs approval - %s %s", e.TenantName, e.Merchant, amount),
				"Review it"
		}
		return fmt.Sprintf("%s submitted a claim for %s", e.SubmitterName, amount),
			fmt.Sprintf("[%s] Claim needs approval - %s %s", e.TenantName, e.Merchant, amount),
			"Review it"

	case expense.ActionApprove:
		return fmt.Sprintf("%s approved your claim for %s", e.DecidedByName, amount),
			fmt.Sprintf("[%s] Claim approved - %s %s", e.TenantName, e.Merchant, amount),
			"View the claim"

	case expense.ActionReject:
		return fmt.Sprintf("%s rejected your claim for %s", e.DecidedByName, amount),
			fmt.Sprintf("[%s] Claim rejected - %s %s", e.TenantName, e.Merchant, amount),
			"Revise it"

	case expense.ActionPay:
		return fmt.Sprintf("Your claim for %s has been paid", amount),
			fmt.Sprintf("[%s] Claim paid - %s %s", e.TenantName, e.Merchant, amount),
			"View the claim"

	case expense.ActionWithdraw:
		return fmt.Sprintf("%s withdrew a claim for %s", e.SubmitterName, amount),
			fmt.Sprintf("[%s] Claim withdrawn - %s %s", e.TenantName, e.Merchant, amount),
			"View the claim"

	default:
		return fmt.Sprintf("A claim for %s changed", amount),
			fmt.Sprintf("[%s] Claim updated - %s", e.TenantName, e.Merchant),
			"View the claim"
	}
}

type budgetView struct {
	TenantName  string
	Department  string
	Budget      string
	Consumed    string
	Remaining   string
	Currency    string
	Usage       string
	Threshold   string
	Overspent   bool
	PeriodStart time.Time
	PeriodEnd   time.Time
	Link        string
}

func (t *templates) budget(dashboard string, e BudgetEvent, to []Recipient) (Message, error) {
	view := budgetView{
		TenantName:  e.TenantName,
		Department:  e.DepartmentName,
		Budget:      e.Budget.String(),
		Consumed:    e.Consumed.String(),
		Remaining:   e.Remaining.String(),
		Currency:    string(e.Budget.Currency),
		Usage:       fmt.Sprintf("%.1f%%", float64(e.UsageBps)/100),
		Threshold:   fmt.Sprintf("%.0f%%", float64(e.ThresholdBps)/100),
		Overspent:   e.Remaining.Minor < 0,
		PeriodStart: e.PeriodStart,
		PeriodEnd:   e.PeriodEnd,
	}
	if dashboard != "" {
		view.Link = dashboard + "/budgets"
	}

	subject := fmt.Sprintf("[%s] %s budget at %s", e.TenantName, e.DepartmentName, view.Usage)
	if view.Overspent {
		subject = fmt.Sprintf("[%s] %s budget overspent", e.TenantName, e.DepartmentName)
	}

	text, err := render(t.budgetText, view)
	if err != nil {
		return Message{}, err
	}
	html, err := renderHTML(t.budgetHTML, view)
	if err != nil {
		return Message{}, err
	}

	return Message{To: to, Subject: subject, Text: text, HTML: html, Category: "budget.threshold"}, nil
}

func render(t *texttemplate.Template, data any) (string, error) {
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("render %s: %w", t.Name(), err)
	}
	return b.String(), nil
}

func renderHTML(t *htmltemplate.Template, data any) (string, error) {
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("render %s: %w", t.Name(), err)
	}
	return b.String(), nil
}

func derefOr(s *string, fallback string) string {
	if s == nil || *s == "" {
		return fallback
	}
	return *s
}

// The bodies. Inline rather than embedded files because they are short, and
// keeping them next to expenseWording is what makes an inconsistency between
// the subject and the body visible.

const expenseTextBody = `{{ .Headline }}

Merchant:    {{ .Merchant }}
Amount:      {{ .Amount }} {{ .Currency }}
Date:        {{ date .SpentAt }}
Department:  {{ .Department }}
Submitted by:{{ if .Submitter }} {{ .Submitter }}{{ else }} -{{ end }}
{{- if .DecidedBy }}
Decided by:  {{ .DecidedBy }}
{{- end }}
{{- if .Note }}
Note:        {{ .Note }}
{{- end }}
{{- if .PaymentRef }}
Payment ref: {{ .PaymentRef }}
{{- end }}
{{- if gt .Revision 1 }}
Revision:    {{ .Revision }}
{{- end }}
{{ if .Link }}
{{ .CallToAction }}: {{ .Link }}
{{ end }}
--
{{ .TenantName }} expense tracker
`

// Table layout and inline styles, because that is what mail clients render.
// A stylesheet in <head> is stripped by Gmail and several others, and flexbox
// is not supported by Outlook at all.
const expenseHTMLBody = `<!doctype html>
<html><body style="margin:0;padding:24px;background:#f6f7f9;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#1f2933">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:560px;margin:0 auto;background:#ffffff;border-radius:8px;border:1px solid #e4e7eb">
  <tr><td style="padding:24px 24px 8px">
    <h1 style="margin:0;font-size:18px;line-height:1.4;font-weight:600">{{ .Headline }}</h1>
  </td></tr>
  <tr><td style="padding:8px 24px 0">
    <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="font-size:14px">
      <tr><td style="padding:6px 0;color:#616e7c;width:130px">Merchant</td><td style="padding:6px 0">{{ .Merchant }}</td></tr>
      <tr><td style="padding:6px 0;color:#616e7c">Amount</td><td style="padding:6px 0;font-weight:600">{{ .Amount }} {{ .Currency }}</td></tr>
      <tr><td style="padding:6px 0;color:#616e7c">Date</td><td style="padding:6px 0">{{ date .SpentAt }}</td></tr>
      <tr><td style="padding:6px 0;color:#616e7c">Department</td><td style="padding:6px 0">{{ .Department }}</td></tr>
      {{- if .Submitter }}
      <tr><td style="padding:6px 0;color:#616e7c">Submitted by</td><td style="padding:6px 0">{{ .Submitter }}</td></tr>
      {{- end }}
      {{- if .DecidedBy }}
      <tr><td style="padding:6px 0;color:#616e7c">Decided by</td><td style="padding:6px 0">{{ .DecidedBy }}</td></tr>
      {{- end }}
      {{- if .PaymentRef }}
      <tr><td style="padding:6px 0;color:#616e7c">Payment ref</td><td style="padding:6px 0">{{ .PaymentRef }}</td></tr>
      {{- end }}
      {{- if gt .Revision 1 }}
      <tr><td style="padding:6px 0;color:#616e7c">Revision</td><td style="padding:6px 0">{{ .Revision }}</td></tr>
      {{- end }}
    </table>
  </td></tr>
  {{- if .Note }}
  <tr><td style="padding:12px 24px 0">
    <div style="padding:12px;background:#f6f7f9;border-left:3px solid #cbd2d9;font-size:14px">{{ .Note }}</div>
  </td></tr>
  {{- end }}
  {{- if .Link }}
  <tr><td style="padding:20px 24px 24px">
    <a href="{{ .Link }}" style="display:inline-block;padding:10px 18px;background:#1f3864;color:#ffffff;text-decoration:none;border-radius:6px;font-size:14px">{{ .CallToAction }}</a>
  </td></tr>
  {{- end }}
  <tr><td style="padding:16px 24px;border-top:1px solid #e4e7eb;color:#9aa5b1;font-size:12px">
    {{ .TenantName }} expense tracker
  </td></tr>
</table>
</body></html>
`

const budgetTextBody = `{{ if .Overspent }}The {{ .Department }} budget has been overspent.{{ else }}The {{ .Department }} budget has reached {{ .Usage }} of its limit.{{ end }}

Budget:    {{ .Budget }} {{ .Currency }}
Committed: {{ .Consumed }} {{ .Currency }} ({{ .Usage }})
Remaining: {{ .Remaining }} {{ .Currency }}
Period:    {{ date .PeriodStart }} to {{ date .PeriodEnd }}
Alerts at: {{ .Threshold }}

Committed means approved and paid claims. Claims still awaiting a decision are
not counted, so this figure is money the organisation has agreed to spend.
{{ if .Link }}
Review the budgets: {{ .Link }}
{{ end }}
--
{{ .TenantName }} expense tracker
`

const budgetHTMLBody = `<!doctype html>
<html><body style="margin:0;padding:24px;background:#f6f7f9;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#1f2933">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:560px;margin:0 auto;background:#ffffff;border-radius:8px;border:1px solid #e4e7eb">
  <tr><td style="padding:24px 24px 8px">
    <h1 style="margin:0;font-size:18px;line-height:1.4;font-weight:600;color:{{ if .Overspent }}#b91c1c{{ else }}#92400e{{ end }}">
      {{ if .Overspent }}The {{ .Department }} budget has been overspent{{ else }}The {{ .Department }} budget is at {{ .Usage }}{{ end }}
    </h1>
  </td></tr>
  <tr><td style="padding:8px 24px 0">
    <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="font-size:14px">
      <tr><td style="padding:6px 0;color:#616e7c;width:130px">Budget</td><td style="padding:6px 0">{{ .Budget }} {{ .Currency }}</td></tr>
      <tr><td style="padding:6px 0;color:#616e7c">Committed</td><td style="padding:6px 0;font-weight:600">{{ .Consumed }} {{ .Currency }} ({{ .Usage }})</td></tr>
      <tr><td style="padding:6px 0;color:#616e7c">Remaining</td><td style="padding:6px 0;color:{{ if .Overspent }}#b91c1c{{ else }}#1f2933{{ end }}">{{ .Remaining }} {{ .Currency }}</td></tr>
      <tr><td style="padding:6px 0;color:#616e7c">Period</td><td style="padding:6px 0">{{ date .PeriodStart }} &ndash; {{ date .PeriodEnd }}</td></tr>
    </table>
  </td></tr>
  <tr><td style="padding:12px 24px 0;font-size:13px;color:#616e7c">
    Committed means approved and paid claims. Claims still awaiting a decision are not counted,
    so this is money the organisation has agreed to spend.
  </td></tr>
  {{- if .Link }}
  <tr><td style="padding:20px 24px 24px">
    <a href="{{ .Link }}" style="display:inline-block;padding:10px 18px;background:#1f3864;color:#ffffff;text-decoration:none;border-radius:6px;font-size:14px">Review the budgets</a>
  </td></tr>
  {{- end }}
  <tr><td style="padding:16px 24px;border-top:1px solid #e4e7eb;color:#9aa5b1;font-size:12px">
    {{ .TenantName }} expense tracker
  </td></tr>
</table>
</body></html>
`
