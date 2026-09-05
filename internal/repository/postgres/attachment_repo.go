package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/expense"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/repository/postgres/gen"
)

// Attachment is a receipt stored against a claim. The bytes live in the object
// store; this is the record that says which object belongs to which claim.
type Attachment struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	ExpenseID   uuid.UUID `json:"expense_id"`
	ObjectKey   string    `json:"-"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`

	// Checksum is the raw SHA-256 of the object. Rendered as hex for clients
	// rather than returned as bytes, so it is comparable against what a
	// command-line tool prints.
	Checksum []byte `json:"-"`

	UploadedBy uuid.UUID `json:"uploaded_by"`
	CreatedAt  time.Time `json:"created_at"`
}

// AttachmentWithClaim carries the claim's state alongside the attachment, so
// the authorisation checks need one query rather than two.
type AttachmentWithClaim struct {
	Attachment
	ExpenseStatus       expense.Status
	ExpenseSubmitterID  uuid.UUID
	ExpenseDepartmentID *uuid.UUID
}

type AttachmentRepository struct{}

func NewAttachmentRepository() *AttachmentRepository { return &AttachmentRepository{} }

// MaxAttachmentsPerClaim bounds how many receipts one claim may carry.
//
// Not a plan limit: it is a guard against a client looping on the presign
// endpoint, which would otherwise fill a bucket at the cost of one request per
// object. Ten is well past what any real expense needs.
const MaxAttachmentsPerClaim = 10

func (r *AttachmentRepository) Add(
	ctx context.Context,
	tc *postgres.TenantConn,
	expenseID uuid.UUID,
	objectKey, filename, contentType string,
	sizeBytes int64,
	checksum []byte,
	uploadedBy uuid.UUID,
) (*Attachment, error) {
	row, err := gen.New(tc).AddExpenseAttachment(ctx, gen.AddExpenseAttachmentParams{
		TenantID:    tc.TenantID(),
		ExpenseID:   expenseID,
		ObjectKey:   objectKey,
		Filename:    filename,
		ContentType: contentType,
		SizeBytes:   sizeBytes,
		Checksum:    checksum,
		UploadedBy:  uploadedBy,
	})
	if err != nil {
		return nil, translate(err)
	}
	return toDomainAttachment(row), nil
}

func (r *AttachmentRepository) List(ctx context.Context, tc *postgres.TenantConn, expenseID uuid.UUID) ([]*Attachment, error) {
	rows, err := gen.New(tc).ListExpenseAttachments(ctx, gen.ListExpenseAttachmentsParams{
		TenantID:  tc.TenantID(),
		ExpenseID: expenseID,
	})
	if err != nil {
		return nil, translate(err)
	}
	out := make([]*Attachment, len(rows))
	for i, row := range rows {
		out[i] = toDomainAttachment(row)
	}
	return out, nil
}

func (r *AttachmentRepository) Count(ctx context.Context, tc *postgres.TenantConn, expenseID uuid.UUID) (int, error) {
	n, err := gen.New(tc).CountExpenseAttachments(ctx, gen.CountExpenseAttachmentsParams{
		TenantID:  tc.TenantID(),
		ExpenseID: expenseID,
	})
	return int(n), translate(err)
}

// Get returns the attachment together with the state of the claim it belongs
// to, because every authorisation decision about a receipt is really a
// decision about the claim.
func (r *AttachmentRepository) Get(ctx context.Context, tc *postgres.TenantConn, id uuid.UUID) (*AttachmentWithClaim, error) {
	row, err := gen.New(tc).GetExpenseAttachment(ctx, gen.GetExpenseAttachmentParams{
		TenantID: tc.TenantID(),
		ID:       id,
	})
	if err != nil {
		return nil, translate(err)
	}

	return &AttachmentWithClaim{
		Attachment: Attachment{
			ID:          row.ID,
			TenantID:    row.TenantID,
			ExpenseID:   row.ExpenseID,
			ObjectKey:   row.ObjectKey,
			Filename:    row.Filename,
			ContentType: row.ContentType,
			SizeBytes:   row.SizeBytes,
			Checksum:    row.Checksum,
			UploadedBy:  row.UploadedBy,
			CreatedAt:   row.CreatedAt,
		},
		ExpenseStatus:       expense.Status(row.ExpenseStatus),
		ExpenseSubmitterID:  row.ExpenseSubmitterID,
		ExpenseDepartmentID: row.ExpenseDepartmentID,
	}, nil
}

// Delete removes the row. The object itself is deleted by the caller after the
// transaction commits: an object store deletion cannot be rolled back, so
// doing it inside the transaction would destroy the file even when the
// transaction is abandoned.
func (r *AttachmentRepository) Delete(ctx context.Context, tc *postgres.TenantConn, id uuid.UUID) error {
	n, err := gen.New(tc).DeleteExpenseAttachment(ctx, gen.DeleteExpenseAttachmentParams{
		TenantID: tc.TenantID(),
		ID:       id,
	})
	if err != nil {
		return translate(err)
	}
	if n == 0 {
		// Either it is gone, or the RLS policy refused because the claim is no
		// longer a draft. The two are indistinguishable from here and the
		// service supplies the better message, having already loaded the claim.
		return shared.ErrNotFound
	}
	return nil
}

func toDomainAttachment(row gen.ExpenseAttachment) *Attachment {
	return &Attachment{
		ID:          row.ID,
		TenantID:    row.TenantID,
		ExpenseID:   row.ExpenseID,
		ObjectKey:   row.ObjectKey,
		Filename:    row.Filename,
		ContentType: row.ContentType,
		SizeBytes:   row.SizeBytes,
		Checksum:    row.Checksum,
		UploadedBy:  row.UploadedBy,
		CreatedAt:   row.CreatedAt,
	}
}
