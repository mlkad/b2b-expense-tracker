// Package storage holds receipt objects.
//
// The bytes never pass through this service. A receipt is up to 25 MiB, and an
// API that proxied uploads would hold that much per concurrent upload, spend
// its request deadline on the client's connection speed, and put a file the
// user chose through a process that also holds database credentials.
//
// Instead the API signs a URL and the client talks to the object store
// directly: PUT to upload, GET to download. The API's involvement is one HMAC
// and one row.
package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrNotFound means the object is not in the store. The confirm path
	// treats it as "the client never uploaded what it says it did".
	ErrNotFound = errors.New("object not found in storage")

	ErrUnavailable = errors.New("object storage is unavailable")
)

// MaxObjectBytes matches expense_attachments_size_chk. It is checked before
// signing and again after upload, because a presigned PUT cannot carry a size
// limit - only the POST policy form can, and that cannot be used for a plain
// PUT. Signing for a declared size and verifying the stored size is the honest
// version of the guarantee.
const MaxObjectBytes = 25 << 20

// ObjectInfo is what the store reports about a stored object.
type ObjectInfo struct {
	Key         string
	SizeBytes   int64
	ContentType string

	// ChecksumSHA256 is the base64 digest the store computed, when it computed
	// one. Empty when the backend does not report one - which is not the same
	// as unverified: the digest is enforced at upload time by the signed
	// x-amz-checksum-sha256 header, and a mismatch is refused there with
	// XAmzContentChecksumMismatch. This field is a second, weaker confirmation
	// for the confirm path, not the mechanism.
	ChecksumSHA256 string
}

// PresignedRequest is everything a client needs to perform the upload.
type PresignedRequest struct {
	URL    string `json:"url"`
	Method string `json:"method"`
	// Headers the client must send verbatim. They are part of the signature,
	// so omitting or altering one makes the store reject the request - which
	// is what enforces the declared content type and checksum.
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// PutConstraints are the properties the signature binds the upload to.
type PutConstraints struct {
	ContentType string

	// ChecksumSHA256 is the base64-encoded SHA-256 the client claims. When
	// set, it is signed as x-amz-checksum-sha256, so the object store computes
	// the digest itself and refuses the upload if it disagrees. That moves
	// verification to the only party that sees the bytes.
	ChecksumSHA256 string
}

// Store is the object store this service needs. Four operations, all of which
// either sign a URL or make one small request; none of them moves object bytes
// through this process.
type Store interface {
	PresignPut(ctx context.Context, key string, ttl time.Duration, c PutConstraints) (*PresignedRequest, error)

	// PresignGet returns a download URL. downloadName sets the
	// Content-Disposition the store will serve, so a receipt saved from the
	// dashboard keeps the name the user uploaded rather than the object key.
	PresignGet(ctx context.Context, key string, ttl time.Duration, downloadName string) (string, error)

	Stat(ctx context.Context, key string) (ObjectInfo, error)
	Delete(ctx context.Context, key string) error
}

// ObjectKey builds the path an attachment is stored at.
//
// The user's filename is deliberately not part of it. A key derived from
// client input is a path traversal waiting to happen, and object stores accept
// keys containing "../" quite happily. The filename is a column instead, and
// it reaches the client again only through Content-Disposition on a presigned
// download.
//
// The tenant prefix is not a security boundary - object stores have no
// row-level security, and possession of a presigned URL is the whole
// authorisation - but it makes a bucket lifecycle rule or a per-tenant
// deletion expressible, which matters when a customer leaves.
func ObjectKey(tenantID, expenseID uuid.UUID, filename string) string {
	ext := strings.ToLower(path.Ext(filename))
	if len(ext) > 10 || !safeExtension(ext) {
		ext = ""
	}
	return fmt.Sprintf("tenants/%s/expenses/%s/%s%s", tenantID, expenseID, uuid.NewString(), ext)
}

// safeExtension keeps the key free of anything that could be interpreted by a
// path parser or a web server. The extension is cosmetic - it helps a human
// browsing the bucket - so an unrecognised one is dropped rather than rejected.
func safeExtension(ext string) bool {
	if ext == "" {
		return false
	}
	for _, r := range ext[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// AllowedContentTypes is what a receipt may be.
//
// An allowlist, not a denylist. The store serves these back to a browser, so
// anything that can carry script - text/html, image/svg+xml - would be a stored
// cross-site scripting vector against whoever opens the receipt. SVG is the one
// people forget: it is an image to a human and a document with <script> to a
// browser.
var AllowedContentTypes = map[string]struct{}{
	"application/pdf": {},
	"image/jpeg":      {},
	"image/png":       {},
	"image/webp":      {},
	"image/heic":      {},
	"image/tiff":      {},
}

func ContentTypeAllowed(ct string) bool {
	_, ok := AllowedContentTypes[strings.ToLower(strings.TrimSpace(ct))]
	return ok
}

// ChecksumOf is a helper for tests and for the filesystem store.
func ChecksumOf(b []byte) [32]byte { return sha256.Sum256(b) }

// httpClient is shared by the backends. The timeouts are short because every
// request these make is a HEAD or a DELETE against a store in the same region;
// an upload never comes through here.
func httpClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        8,
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     90 * time.Second,
			ForceAttemptHTTP2:   true,
		},
	}
}
