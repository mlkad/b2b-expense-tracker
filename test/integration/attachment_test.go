//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/logger"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/service"
	"github.com/mlkad/b2b-expense-tracker/internal/storage"
)

func attachmentServiceForTest(t *testing.T) *service.AttachmentService {
	t.Helper()
	if objectStore == nil {
		t.Skip("object storage is not available in this run")
	}
	tenancy := repo.NewTenancyRepository()
	return service.NewAttachmentService(
		service.NewScope(app, tenancy),
		repo.NewAttachmentRepository(),
		repo.NewExpenseRepository(),
		objectStore,
		5*time.Minute,
		5*time.Minute,
		logger.New(logger.ParseLevel("error"), logger.FormatText, "integration", "test"),
	)
}

// upload performs the client's half of the flow: PUT the bytes straight to the
// object store with the headers the signature covers.
func upload(t *testing.T, ticket *service.UploadTicket, content []byte) *http.Response {
	t.Helper()

	req, err := http.NewRequest(ticket.Upload.Method, ticket.Upload.URL, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range ticket.Upload.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	return resp
}

func checksumOf(b []byte) string {
	sum := sha256.Sum256(b)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// The whole flow, against a real S3-compatible store: sign, upload directly,
// confirm, download, delete.
func TestReceiptUploadRoundTrip(t *testing.T) {
	o := seedOrg(t, "receipt-roundtrip")
	claim := seedClaim(t, o, "draft", 4200)

	svc := attachmentServiceForTest(t)
	subject := subjectFor(t, o, o.Submitter)
	ctx := context.Background()

	content := []byte("%PDF-1.7\nreceipt for a keyboard\n")
	req := service.UploadRequest{
		// A space and a non-ASCII character, because both have to survive
		// signing and come back in Content-Disposition.
		Filename:       "café receipt.pdf",
		ContentType:    "application/pdf",
		SizeBytes:      int64(len(content)),
		ChecksumSHA256: checksumOf(content),
	}

	ticket, err := svc.PrepareUpload(ctx, subject, claim, req)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	if !strings.HasPrefix(ticket.ObjectKey, "tenants/"+o.TenantID.String()+"/expenses/"+claim.String()+"/") {
		t.Fatalf("object key is not scoped to the tenant and claim: %q", ticket.ObjectKey)
	}
	// The user's filename must not be in the key: object stores accept "../"
	// in keys quite happily.
	if strings.Contains(ticket.ObjectKey, "café") || strings.Contains(ticket.ObjectKey, " ") {
		t.Errorf("the client's filename leaked into the object key: %q", ticket.ObjectKey)
	}

	resp := upload(t, ticket, content)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presigned upload returned %d: %s", resp.StatusCode, body)
	}

	created, err := svc.ConfirmUpload(ctx, subject, claim, ticket.ObjectKey, req)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if created.SizeBytes != int64(len(content)) {
		t.Errorf("recorded %d bytes, want %d", created.SizeBytes, len(content))
	}
	if created.Filename != "café receipt.pdf" {
		t.Errorf("filename = %q", created.Filename)
	}

	// Download through the signed URL, and check the store serves the original
	// filename rather than the object key.
	url, err := svc.DownloadURL(ctx, subject, created.ID)
	if err != nil {
		t.Fatalf("download url: %v", err)
	}
	getResp, err := http.Get(url)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	got, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("download returned %d: %s", getResp.StatusCode, got)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("downloaded %d bytes, want %d", len(got), len(content))
	}
	if cd := getResp.Header.Get("Content-Disposition"); !strings.Contains(cd, "receipt.pdf") {
		t.Errorf("Content-Disposition = %q; the user's filename should come back", cd)
	}

	if err := svc.Delete(ctx, subject, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := objectStore.Stat(ctx, ticket.ObjectKey); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("the object survived deletion: %v", err)
	}
}

// The signature binds the upload to the declared checksum, so the object store
// - the only party that sees the bytes - is what refuses a mismatch.
func TestStoreRefusesContentThatDoesNotMatchTheSignedChecksum(t *testing.T) {
	o := seedOrg(t, "receipt-checksum")
	claim := seedClaim(t, o, "draft", 100)

	svc := attachmentServiceForTest(t)
	subject := subjectFor(t, o, o.Submitter)
	ctx := context.Background()

	declared := []byte("the file I said I would upload")
	ticket, err := svc.PrepareUpload(ctx, subject, claim, service.UploadRequest{
		Filename: "r.pdf", ContentType: "application/pdf",
		SizeBytes: int64(len(declared)), ChecksumSHA256: checksumOf(declared),
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := upload(t, ticket, []byte("something else entirely"))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Fatal("the store accepted content that did not match the signed checksum")
	}
	if !strings.Contains(string(body), "Checksum") {
		t.Logf("refused with %d: %s", resp.StatusCode, body)
	}
}

// Confirming without uploading must fail. Otherwise the attachment list is a
// set of assertions rather than a set of files.
func TestConfirmRefusesAnObjectThatWasNeverUploaded(t *testing.T) {
	o := seedOrg(t, "receipt-phantom")
	claim := seedClaim(t, o, "draft", 100)

	svc := attachmentServiceForTest(t)
	subject := subjectFor(t, o, o.Submitter)
	ctx := context.Background()

	content := []byte("never uploaded")
	req := service.UploadRequest{
		Filename: "r.pdf", ContentType: "application/pdf",
		SizeBytes: int64(len(content)), ChecksumSHA256: checksumOf(content),
	}

	ticket, err := svc.PrepareUpload(ctx, subject, claim, req)
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.ConfirmUpload(ctx, subject, claim, ticket.ObjectKey, req)
	if !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("got %v, want a validation error naming the missing object", err)
	}
}

// A key naming another tenant's prefix is a caller trying to attach somebody
// else's file - and the object store has no row-level security to stop them
// reading it afterwards.
func TestConfirmRefusesAKeyFromAnotherTenant(t *testing.T) {
	victim := seedOrg(t, "receipt-victim")
	attacker := seedOrg(t, "receipt-attacker")

	victimClaim := seedClaim(t, victim, "draft", 100)
	attackerClaim := seedClaim(t, attacker, "draft", 100)

	svc := attachmentServiceForTest(t)
	ctx := context.Background()

	content := []byte("a confidential receipt")
	req := service.UploadRequest{
		Filename: "r.pdf", ContentType: "application/pdf",
		SizeBytes: int64(len(content)), ChecksumSHA256: checksumOf(content),
	}

	// The victim uploads something real.
	victimTicket, err := svc.PrepareUpload(ctx, subjectFor(t, victim, victim.Submitter), victimClaim, req)
	if err != nil {
		t.Fatal(err)
	}
	resp := upload(t, victimTicket, content)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("victim upload returned %d", resp.StatusCode)
	}

	// The attacker tries to register it against their own claim.
	_, err = svc.ConfirmUpload(ctx, subjectFor(t, attacker, attacker.Submitter),
		attackerClaim, victimTicket.ObjectKey, req)
	if !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("a cross-tenant object key was accepted: %v", err)
	}
}

// A receipt on a submitted claim is evidence.
func TestReceiptsFollowTheClaimState(t *testing.T) {
	o := seedOrg(t, "receipt-state")
	svc := attachmentServiceForTest(t)
	subject := subjectFor(t, o, o.Submitter)
	ctx := context.Background()

	pending := seedClaim(t, o, "pending_approval", 100)
	content := []byte("x")
	req := service.UploadRequest{
		Filename: "r.pdf", ContentType: "application/pdf",
		SizeBytes: 1, ChecksumSHA256: checksumOf(content),
	}

	if _, err := svc.PrepareUpload(ctx, subject, pending, req); !errors.Is(err, shared.ErrConflict) {
		t.Fatalf("attaching to a submitted claim: got %v, want ErrConflict", err)
	}
}

// Only PDFs and images. The store serves these back to a browser, so anything
// that can carry script is a stored cross-site scripting vector against
// whoever opens the receipt - and SVG is the one people forget, because it is
// an image to a human and a scriptable document to a browser.
func TestContentTypeAllowlist(t *testing.T) {
	o := seedOrg(t, "receipt-content-type")
	claim := seedClaim(t, o, "draft", 100)

	svc := attachmentServiceForTest(t)
	subject := subjectFor(t, o, o.Submitter)
	ctx := context.Background()

	content := []byte("x")
	for _, ct := range []string{
		"image/svg+xml", "text/html", "application/javascript",
		"text/plain", "application/zip", "application/octet-stream", "",
	} {
		_, err := svc.PrepareUpload(ctx, subject, claim, service.UploadRequest{
			Filename: "r", ContentType: ct, SizeBytes: 1, ChecksumSHA256: checksumOf(content),
		})
		if !errors.Is(err, shared.ErrValidation) {
			t.Errorf("content type %q was accepted", ct)
		}
	}

	for _, ct := range []string{"application/pdf", "image/jpeg", "image/png", "image/webp"} {
		if _, err := svc.PrepareUpload(ctx, subject, claim, service.UploadRequest{
			Filename: "r", ContentType: ct, SizeBytes: 1, ChecksumSHA256: checksumOf(content),
		}); err != nil {
			t.Errorf("content type %q was refused: %v", ct, err)
		}
	}
}

// Filenames arrive from a file picker and are not paths, whatever the browser
// sent.
func TestHostileFilenamesAreReducedToABaseName(t *testing.T) {
	o := seedOrg(t, "receipt-filename")
	claim := seedClaim(t, o, "draft", 100)

	svc := attachmentServiceForTest(t)
	subject := subjectFor(t, o, o.Submitter)
	ctx := context.Background()

	content := []byte("%PDF-1.7 x")
	for _, name := range []string{
		"../../../etc/passwd",
		`C:\Users\ada\receipt.pdf`,
		"/absolute/receipt.pdf",
		"..",
	} {
		ticket, err := svc.PrepareUpload(ctx, subject, claim, service.UploadRequest{
			Filename: name, ContentType: "application/pdf",
			SizeBytes: int64(len(content)), ChecksumSHA256: checksumOf(content),
		})
		if err != nil {
			// ".." reduces to nothing usable and is refused, which is correct.
			continue
		}
		if strings.Contains(ticket.ObjectKey, "..") || strings.Contains(ticket.ObjectKey, "etc/passwd") {
			t.Errorf("filename %q produced key %q", name, ticket.ObjectKey)
		}
	}
}

// The declared size must match what the store actually holds, or the size
// column is decoration.
func TestConfirmRefusesADeclaredSizeTheStoreDisagreesWith(t *testing.T) {
	o := seedOrg(t, "receipt-size")
	claim := seedClaim(t, o, "draft", 100)

	svc := attachmentServiceForTest(t)
	subject := subjectFor(t, o, o.Submitter)
	ctx := context.Background()

	content := []byte("twenty-nine bytes of receipt!")
	req := service.UploadRequest{
		Filename: "r.pdf", ContentType: "application/pdf",
		SizeBytes: int64(len(content)), ChecksumSHA256: checksumOf(content),
	}

	ticket, err := svc.PrepareUpload(ctx, subject, claim, req)
	if err != nil {
		t.Fatal(err)
	}
	resp := upload(t, ticket, content)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload returned %d", resp.StatusCode)
	}

	lying := req
	lying.SizeBytes = 1
	if _, err := svc.ConfirmUpload(ctx, subject, claim, ticket.ObjectKey, lying); !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("a mismatched size was accepted: %v", err)
	}
}
