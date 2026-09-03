package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/billing"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/tenant"
	"github.com/mlkad/b2b-expense-tracker/internal/export"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
)

// ErrExportTooLarge means the requested range exceeds what the tenant's plan
// allows to be produced synchronously. The handler turns it into a 413 with a
// pointer at the asynchronous endpoint.
var ErrExportTooLarge = errors.New("export exceeds the synchronous row limit for this plan")

type ReportService struct {
	scope    *Scope
	expenses *repo.ExpenseRepository
	billing  *repo.BillingRepository
	tenancy  *repo.TenancyRepository
}

func NewReportService(scope *Scope, expenses *repo.ExpenseRepository, billingRepo *repo.BillingRepository, tenancy *repo.TenancyRepository) *ReportService {
	return &ReportService{scope: scope, expenses: expenses, billing: billingRepo, tenancy: tenancy}
}

// ExportRequest is a report to produce.
type ExportRequest struct {
	Format export.Format
	Filter repo.Filter
}

// exportColumns is the report's shape. It is one definition used by all three
// encoders, so a column added here appears in the spreadsheet, the CSV and the
// PDF without three separate edits that can disagree.
var exportColumns = []export.Column{
	{Header: "Date", Kind: export.KindDate},
	{Header: "Merchant", Kind: export.KindText, Width: 26},
	{Header: "Category", Kind: export.KindText, Width: 14},
	{Header: "Department", Kind: export.KindText, Width: 18},
	{Header: "Submitted by", Kind: export.KindText, Width: 26},
	{Header: "Status", Kind: export.KindText, Width: 16},
	{Header: "Amount", Kind: export.KindMoney},
	{Header: "Currency", Kind: export.KindText, Width: 9},
	{Header: "Rev", Kind: export.KindInt, Width: 6},
	{Header: "Approved by", Kind: export.KindText, Width: 26},
	{Header: "Decided", Kind: export.KindDateTime},
	{Header: "Paid", Kind: export.KindDateTime},
	{Header: "Payment ref", Kind: export.KindText, Width: 20},
	{Header: "Description", Kind: export.KindText, Width: 40},
}

// StreamExport writes a report to w as it is read from the database.
//
// Nothing is buffered between the two: the encoder's Write is called from
// inside the row iteration, so a row is scanned, converted, encoded and pushed
// onto the socket before the next one is read. The transaction is REPEATABLE
// READ, so a report that takes a minute describes one instant.
//
// prepare is called with the report metadata after the transaction has opened
// and the entitlement check has passed, but before the first byte is written.
// That is the handler's chance to set Content-Type and Content-Disposition:
// once a body byte is out, the status is committed and no error can change it.
func (s *ReportService) StreamExport(
	ctx context.Context,
	subject auth.Subject,
	req ExportRequest,
	prepare func(export.Report) error,
	w io.Writer,
) (rows int, err error) {
	encoder, err := export.NewEncoder(req.Format)
	if err != nil {
		return 0, err
	}

	err = s.scope.Snapshot(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermReportExport); err != nil {
			return err
		}

		entitlement, err := s.billing.GetEntitlement(ctx, tc)
		if err != nil {
			return err
		}
		if !entitlement.Allows(billing.FeatureStreamingExport) {
			return fmt.Errorf("%w: this plan does not include exports", shared.ErrForbidden)
		}
		maxRows := entitlement.Limits().ExportRows

		org, err := s.tenancy.GetTenant(ctx, tc)
		if err != nil {
			return err
		}

		filter, err := s.narrow(actor, req.Filter)
		if err != nil {
			return err
		}

		meta := export.Report{
			Title:      "Expense claims",
			Subtitle:   describeFilter(filter),
			TenantName: org.Name,
			Generated:  time.Now().UTC(),
			Columns:    exportColumns,
		}
		if err := prepare(meta); err != nil {
			return err
		}
		if err := encoder.Open(w, meta); err != nil {
			return err
		}

		streamErr := s.expenses.StreamForExport(ctx, tc, filter, func(row repo.ExportRow) error {
			// Checked per row rather than with a COUNT beforehand. A count
			// over a filtered history costs almost as much as the export
			// itself, and the answer would be stale by the time it was used.
			// Stopping mid-stream is honest: the client gets a truncated
			// download and a 413-shaped error, not a silently short report.
			if maxRows != billing.Unlimited && rows >= maxRows {
				return ErrExportTooLarge
			}
			rows++
			return encoder.Write(toCells(row))
		})

		// Close before returning the error. For XLSX it writes the ZIP central
		// directory, without which the response is not a readable archive at
		// all - and a corrupt file is a worse failure than a short one.
		closeErr := encoder.Close()
		if streamErr != nil {
			return streamErr
		}
		return closeErr
	})

	return rows, err
}

func toCells(r repo.ExportRow) []export.Cell {
	submitter := r.SubmitterEmail
	if r.SubmitterName != nil && *r.SubmitterName != "" {
		submitter = r.SubmitterName
	}

	return []export.Cell{
		export.Date(r.SpentAt),
		export.Text(r.Merchant),
		export.Text(string(r.Category)),
		export.TextPtr(r.DepartmentName),
		export.TextPtr(submitter),
		export.Text(string(r.Status)),
		export.Money(r.Amount),
		export.Text(string(r.Amount.Currency)),
		export.Int(int64(r.Revision)),
		export.TextPtr(r.DeciderEmail),
		export.DateTimePtr(r.DecidedAt),
		export.DateTimePtr(r.PaidAt),
		export.TextPtr(r.PaymentRef),
		export.TextPtr(r.Description),
	}
}

// narrow applies the same visibility rules as the list endpoint. An export
// that returned more than the caller can see on screen would be the obvious
// way around them.
func (s *ReportService) narrow(actor tenant.Actor, f repo.Filter) (repo.Filter, error) {
	switch {
	case actor.Can(tenant.PermExpenseReadAll):
		return f, nil
	case actor.Can(tenant.PermExpenseReadTeam) && actor.DepartmentID != nil:
		f.DepartmentID = actor.DepartmentID
		return f, nil
	default:
		return repo.Filter{}, fmt.Errorf("%w: role %s may not export claims", shared.ErrForbidden, actor.Role)
	}
}

// describeFilter renders the report's scope for its own subtitle. A printed
// report whose scope is not on its face gets misread, and the misreading is
// invisible.
func describeFilter(f repo.Filter) string {
	parts := make([]string, 0, 4)

	switch {
	case f.SpentFrom != nil && f.SpentTo != nil:
		parts = append(parts, fmt.Sprintf("%s to %s",
			f.SpentFrom.Format("2006-01-02"), f.SpentTo.Format("2006-01-02")))
	case f.SpentFrom != nil:
		parts = append(parts, "from "+f.SpentFrom.Format("2006-01-02"))
	case f.SpentTo != nil:
		parts = append(parts, "up to "+f.SpentTo.Format("2006-01-02"))
	default:
		parts = append(parts, "all dates")
	}

	if f.Status != nil {
		parts = append(parts, "status "+string(*f.Status))
	} else {
		parts = append(parts, "all statuses")
	}
	if f.DepartmentID != nil {
		parts = append(parts, "one department")
	} else {
		parts = append(parts, "all departments")
	}

	out := parts[0]
	for _, p := range parts[1:] {
		out += "  ·  " + p
	}
	return out
}
