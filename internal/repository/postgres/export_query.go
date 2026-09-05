package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/expense"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
)

// ExportRow is one line of a report: the claim plus the labels a spreadsheet
// needs, resolved by the query rather than by a lookup per row.
//
// Denormalising in SQL is what keeps the export a single pass. A version that
// fetched department and submitter names per claim would issue one query per
// row, and a hundred thousand round trips is slower than the scan by orders of
// magnitude - and would hold a growing cache of names to avoid it, which is
// the memory the streaming design is trying not to spend.
type ExportRow struct {
	ID             uuid.UUID
	Status         expense.Status
	Category       expense.Category
	Amount         shared.Money
	Merchant       string
	Description    *string
	SpentAt        time.Time
	SubmittedAt    *time.Time
	DecidedAt      *time.Time
	PaidAt         *time.Time
	PaymentRef     *string
	Revision       int32
	DepartmentName *string
	SubmitterEmail *string
	SubmitterName  *string
	DeciderEmail   *string
}

// exportQuery is the one query in this service that sqlc does not generate.
//
// sqlc emits a :many query as a function returning a slice: it collects every
// row before returning. For an export covering a year of a large tenant's
// claims that is the entire result set resident in memory at once, which
// defeats the purpose of the streaming endpoint that consumes it. Iterating
// pgx.Rows directly holds one row at a time.
//
// The cost of stepping outside sqlc is that a column renamed in a migration
// would not break the build. TestExportQueryMatchesSchema in the integration
// suite prepares this statement against a live schema and asserts the column
// list, so the failure still happens in CI rather than in production.
//
// The parameters use the same `$n IS NULL OR column = $n` idiom as the
// generated queries, so there is no string building here either: the SQL text
// is a constant and every value is a bind parameter.
const exportQuery = `
SELECT e.id,
       e.status,
       e.category,
       e.amount_minor,
       e.currency,
       e.merchant,
       e.description,
       e.spent_at,
       e.submitted_at,
       e.decided_at,
       e.paid_at,
       e.payment_ref,
       e.revision,
       d.name       AS department_name,
       su.email     AS submitter_email,
       su.full_name AS submitter_name,
       du.email     AS decider_email
  FROM expenses e
  LEFT JOIN departments d  ON d.id = e.department_id AND d.tenant_id = e.tenant_id
  LEFT JOIN memberships sm ON sm.id = e.submitter_id AND sm.tenant_id = e.tenant_id
  LEFT JOIN users su       ON su.id = sm.user_id
  LEFT JOIN memberships dm ON dm.id = e.decided_by  AND dm.tenant_id = e.tenant_id
  LEFT JOIN users du       ON du.id = dm.user_id
 WHERE e.tenant_id = $1
   AND ($2::expense_status IS NULL OR e.status = $2)
   AND ($3::uuid IS NULL OR e.department_id = $3)
   AND ($4::date IS NULL OR e.spent_at >= $4)
   AND ($5::date IS NULL OR e.spent_at <= $5)
 ORDER BY e.spent_at ASC, e.id ASC`

// ExportColumns is the column list exportQuery returns, in order. The
// integration test compares it against what the server reports for the
// prepared statement.
var ExportColumns = []string{
	"id", "status", "category", "amount_minor", "currency", "merchant",
	"description", "spent_at", "submitted_at", "decided_at", "paid_at",
	"payment_ref", "revision", "department_name", "submitter_email",
	"submitter_name", "decider_email",
}

// StreamForExport walks the matching claims in chronological order, calling
// yield once per row.
//
// Memory is bounded by one row regardless of how many match: pgx reads DataRow
// messages from the connection as they arrive, and nothing here accumulates.
// The caller's yield writes each row straight to the HTTP response, so the
// peak footprint of a hundred-thousand-row export is a row struct and the
// encoder's buffer.
//
// The transaction must be REPEATABLE READ. Under READ COMMITTED each statement
// takes a fresh snapshot - and although this is one statement, an export that
// runs for ninety seconds against a busy tenant is exactly where a caller is
// tempted to add a second query for totals. Pinning the snapshot means the
// figures in a report agree with each other.
//
// Returning an error from yield stops the walk. That is how a client
// disconnecting mid-download stops the scan instead of streaming a hundred
// thousand rows into a closed socket.
func (r *ExpenseRepository) StreamForExport(
	ctx context.Context,
	tc *postgres.TenantConn,
	f Filter,
	yield func(ExportRow) error,
) error {
	rows, err := tc.Query(ctx, exportQuery,
		tc.TenantID(),
		nullString(f.Status),
		f.DepartmentID,
		f.SpentFrom,
		f.SpentTo,
	)
	if err != nil {
		return translate(err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			row      ExportRow
			status   string
			category string
			minor    int64
			curr     string
		)
		if err := rows.Scan(
			&row.ID, &status, &category, &minor, &curr, &row.Merchant,
			&row.Description, &row.SpentAt, &row.SubmittedAt, &row.DecidedAt,
			&row.PaidAt, &row.PaymentRef, &row.Revision, &row.DepartmentName,
			&row.SubmitterEmail, &row.SubmitterName, &row.DeciderEmail,
		); err != nil {
			return fmt.Errorf("scan export row: %w", translate(err))
		}

		row.Status = expense.Status(status)
		row.Category = expense.Category(category)
		row.Amount = shared.Money{Minor: minor, Currency: currency(curr)}

		if err := yield(row); err != nil {
			return err
		}
	}
	// rows.Err reports a failure that happened after the first row was
	// delivered - a lost connection, a statement timeout part way through.
	// Without this check the export would end early and look complete.
	return translate(rows.Err())
}

// nullString renders an optional status as a nullable text parameter. The
// query casts it to expense_status, so an unset filter is a genuine SQL NULL
// rather than the empty string, which would not be a valid enum label.
func nullString(s *expense.Status) *string {
	if s == nil {
		return nil
	}
	v := string(*s)
	return &v
}
