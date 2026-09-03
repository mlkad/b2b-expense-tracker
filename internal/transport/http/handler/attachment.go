package handler

import (
	"net/http"

	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/service"
	"github.com/mlkad/b2b-expense-tracker/internal/transport/http/middleware"
)

type AttachmentHandler struct {
	attachments *service.AttachmentService
}

func NewAttachmentHandler(attachments *service.AttachmentService) *AttachmentHandler {
	return &AttachmentHandler{attachments: attachments}
}

type uploadRequest struct {
	Filename       string `json:"filename"`
	ContentType    string `json:"content_type"`
	SizeBytes      int64  `json:"size_bytes"`
	ChecksumSHA256 string `json:"checksum_sha256"`
}

func (r uploadRequest) toService() service.UploadRequest {
	return service.UploadRequest{
		Filename:       r.Filename,
		ContentType:    r.ContentType,
		SizeBytes:      r.SizeBytes,
		ChecksumSHA256: r.ChecksumSHA256,
	}
}

// PrepareUpload returns a signed URL the client uploads to directly.
//
// Two steps rather than one multipart POST, because a receipt is up to 25 MiB
// and an API that proxied it would hold that much per concurrent upload and
// spend its request deadline on the client's connection speed. The client
// uploads to the object store and then confirms.
func (h *AttachmentHandler) PrepareUpload(w http.ResponseWriter, r *http.Request) {
	expenseID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req uploadRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	ticket, err := h.attachments.PrepareUpload(r.Context(), middleware.MustSubject(r), expenseID, req.toService())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ticket)
}

type confirmRequest struct {
	uploadRequest
	ObjectKey string `json:"object_key"`
}

// ConfirmUpload records the receipt after the client has uploaded it. The
// service stat-s the object first, so this cannot register a file that was
// never uploaded.
func (h *AttachmentHandler) ConfirmUpload(w http.ResponseWriter, r *http.Request) {
	expenseID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req confirmRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	created, err := h.attachments.ConfirmUpload(r.Context(), middleware.MustSubject(r),
		expenseID, req.ObjectKey, req.uploadRequest.toService())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, attachmentBody(created))
}

func (h *AttachmentHandler) List(w http.ResponseWriter, r *http.Request) {
	expenseID, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}

	list, err := h.attachments.List(r.Context(), middleware.MustSubject(r), expenseID)
	if err != nil {
		writeError(w, r, err)
		return
	}

	items := make([]map[string]any, len(list))
	for i, a := range list {
		items[i] = attachmentBody(a)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// Download redirects to a short-lived signed URL rather than proxying the
// bytes.
//
// 302 with Cache-Control: no-store. The redirect target is a credential with a
// few minutes of life, and a cached 302 would hand it to the next person to
// open the same link after the permission check that produced it no longer
// holds.
func (h *AttachmentHandler) Download(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}

	url, err := h.attachments.DownloadURL(r.Context(), middleware.MustSubject(r), id)
	if err != nil {
		writeError(w, r, err)
		return
	}

	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, url, http.StatusFound)
}

func (h *AttachmentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.attachments.Delete(r.Context(), middleware.MustSubject(r), id); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// attachmentBody renders a receipt for a client.
//
// The object key is deliberately not included. It is an internal address, and
// publishing it invites a client to construct its own URLs against the bucket
// - which is exactly what the presign flow exists to keep it from doing. The
// checksum is hex rather than base64 so it matches what sha256sum prints.
func attachmentBody(a *repo.Attachment) map[string]any {
	return map[string]any{
		"id":           a.ID,
		"expense_id":   a.ExpenseID,
		"filename":     a.Filename,
		"content_type": a.ContentType,
		"size_bytes":   a.SizeBytes,
		"checksum":     service.HexChecksum(a.Checksum),
		"uploaded_by":  a.UploadedBy,
		"created_at":   a.CreatedAt,
	}
}
