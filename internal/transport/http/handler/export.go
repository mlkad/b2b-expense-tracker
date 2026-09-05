package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
	"github.com/mlkad/b2b-expense-tracker/internal/export"
	"github.com/mlkad/b2b-expense-tracker/internal/logger"
	"github.com/mlkad/b2b-expense-tracker/internal/service"
	"github.com/mlkad/b2b-expense-tracker/internal/transport/http/middleware"
)

type ExportHandler struct {
	reports   *service.ReportService
	downloads *auth.DownloadTokens

	// deadline bounds one export. It is much longer than the API timeout
	// because a large report legitimately takes minutes, and it is applied
	// here rather than by the route group so the two cannot be confused.
	deadline time.Duration
}

func NewExportHandler(reports *service.ReportService, downloads *auth.DownloadTokens, deadline time.Duration) *ExportHandler {
	if deadline <= 0 {
		deadline = 10 * time.Minute
	}
	return &ExportHandler{reports: reports, downloads: downloads, deadline: deadline}
}

// flushInterval is how often buffered bytes are pushed to the client.
//
// Not per row: a flush is a syscall and a TCP segment, and flushing a
// forty-byte CSV row produces a packet per row. Not never, either - a client
// that sees nothing for two minutes assumes the download has hung, and so do
// most proxies. A second is short enough that the browser's progress
// indicator moves and long enough that the writes coalesce.
const flushInterval = time.Second

// HandleExport streams a report.
//
// The hard constraint of a streaming download is that HTTP commits the status
// code with the first byte of the body. After that there is no way to send a
// 500: whatever has already been written is what the client saves. So the
// order here is deliberate:
//
//  1. Parse and validate everything that can be validated before touching the
//     database. A bad format or a malformed date is a 400 with a body.
//  2. Open the transaction, check the entitlement, resolve the tenant name -
//     all before a byte is written. Every error up to this point is still
//     reportable as JSON with a status code.
//  3. Write the headers, from the prepare callback, at the last possible
//     moment.
//  4. Stream.
//  5. A failure after step 3 cannot be a status code, so it becomes a
//     deliberately aborted connection. The client sees a truncated transfer,
//     which every HTTP client reports as a failed download - rather than a
//     complete-looking file that is quietly missing half the year.
func (h *ExportHandler) HandleExport(w http.ResponseWriter, r *http.Request) {
	subject := middleware.MustSubject(r)
	log := logger.FromContext(r.Context())

	q := newQueryReader(r)
	format, err := export.ParseFormat(q.raw("format"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	filter := q.filter()
	if err := q.err(); err != nil {
		writeError(w, r, err)
		return
	}

	// The request context is used as it arrives. The export routes are
	// mounted under their own long Timeout middleware, so the deadline here is
	// already the right one - and, crucially, the context still carries the
	// server's client-disconnect cancellation. Replacing it with
	// context.WithoutCancel to escape a short route timeout would drop that
	// too, and an abandoned download would keep scanning the database and
	// writing into a closed socket until it finished.
	ctx := r.Context()

	// The default write deadline is measured in seconds and would cut a large
	// report off mid-stream. NewResponseController reaches through the
	// middleware's wrapper because statusRecorder implements Unwrap.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(h.deadline)); err != nil {
		// Not fatal. Some ResponseWriter implementations - notably in tests -
		// do not support deadlines, and the export is still correct without
		// one.
		log.DebugContext(ctx, "write deadline not supported", slog.String("error", err.Error()))
	}

	flusher := &periodicFlusher{w: w, rc: rc, every: flushInterval, last: time.Now()}
	headersWritten := false

	rows, err := h.reports.StreamExport(ctx, subject,
		service.ExportRequest{Format: format, Filter: filter},
		func(meta export.Report) error {
			// The last point at which an error can still be a status code.
			setDownloadHeaders(w, format, meta)
			headersWritten = true
			return nil
		},
		flusher)

	if err != nil {
		if !headersWritten {
			writeError(w, r, err)
			return
		}

		// Past the point of no return. Log it with the row count so the size
		// of the truncation is known, then drop the connection.
		level := slog.LevelError
		if errors.Is(err, service.ErrExportTooLarge) || isClientGone(ctx.Err()) {
			level = slog.LevelInfo
		}
		log.LogAttrs(ctx, level, "export failed after the response began",
			slog.String("error", err.Error()),
			slog.Int("rows_written", rows),
			slog.String("format", string(format)))

		// http.ErrAbortHandler is the documented way to abandon a response
		// without a stack trace in the log. The Recoverer re-panics it
		// untouched so the server closes the connection.
		panic(http.ErrAbortHandler)
	}

	if err := flusher.Flush(); err != nil {
		log.DebugContext(ctx, "final flush failed", slog.String("error", err.Error()))
	}

	log.InfoContext(ctx, "export complete",
		slog.Int("rows", rows),
		slog.String("format", string(format)))
}

// setDownloadHeaders names the file and tells caches and proxies how to treat
// it.
func setDownloadHeaders(w http.ResponseWriter, format export.Format, meta export.Report) {
	encoder, _ := export.NewEncoder(format)
	filename := fmt.Sprintf("expenses-%s.%s", meta.Generated.Format("2006-01-02"), encoder.Extension())

	h := w.Header()
	h.Set("Content-Type", encoder.ContentType())
	h.Set("Content-Disposition", contentDisposition(filename))

	// The length is genuinely unknown - that is the point of streaming - so
	// the response is chunked. Saying so explicitly stops a proxy from
	// buffering the whole thing to compute a length, which would reintroduce
	// exactly the memory cost this design avoids, one hop away.
	h.Set("Transfer-Encoding", "chunked")
	h.Set("X-Accel-Buffering", "no") // nginx: do not buffer this response

	// A report is a point-in-time snapshot of private financial data. It must
	// not sit in a shared cache, and it must not be revalidated from one.
	h.Set("Cache-Control", "no-store, private")
	h.Set("X-Content-Type-Options", "nosniff")
}

// contentDisposition emits both the plain and the RFC 5987 encoded filename.
//
// The unencoded form is ASCII-only for old clients; the filename* form carries
// the real name for everything since about 2012. Quoting matters: a filename
// containing a quote or a semicolon would otherwise let a caller inject header
// parameters, and the report name derives from tenant-controlled data.
func contentDisposition(filename string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7E || r == '"' || r == '\\' || r == ';' {
			return '_'
		}
		return r
	}, filename)

	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		ascii, url.PathEscape(filename))
}

// periodicFlusher pushes buffered bytes at a bounded rate.
//
// It sits between the encoder and the ResponseWriter, so the encoders stay
// unaware of HTTP - which is what lets them be tested against a bytes.Buffer.
type periodicFlusher struct {
	w     http.ResponseWriter
	rc    *http.ResponseController
	every time.Duration
	last  time.Time
}

func (f *periodicFlusher) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err != nil {
		return n, err
	}
	if time.Since(f.last) >= f.every {
		f.last = time.Now()
		// A flush failure means the client is gone. The subsequent Write
		// returns the same condition, and the stream unwinds through the
		// normal error path, so it is not worth failing on here.
		_ = f.rc.Flush()
	}
	return n, nil
}

func (f *periodicFlusher) Flush() error { return f.rc.Flush() }

func isClientGone(err error) bool {
	return err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

// Deadline reports the timeout the export routes must be mounted with. The
// router reads it rather than repeating the number, so the write deadline set
// on the socket above and the context deadline cannot disagree.
func (h *ExportHandler) Deadline() time.Duration { return h.deadline }

// downloadTicket is a signed URL for one report.
type downloadTicket struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// IssueDownloadToken mints a link the browser can navigate to.
//
// Two steps rather than one, for the same reason receipts take two: a browser
// navigation cannot carry an Authorization header, and fetching the report with
// one instead would mean holding a report that may be tens of megabytes in the
// tab's memory - which is the cost the streaming export was built to avoid.
//
// The token is bound to the exact query it was minted for, so the filters
// cannot be widened afterwards, and it lives for a minute because a query
// string is written down by every access log it passes through.
func (h *ExportHandler) IssueDownloadToken(w http.ResponseWriter, r *http.Request) {
	subject := middleware.MustSubject(r)

	q := newQueryReader(r)
	if _, err := export.ParseFormat(q.raw("format")); err != nil {
		writeError(w, r, err)
		return
	}
	// Parsed and discarded: the point is to reject a malformed filter now,
	// while an error can still be a JSON response, rather than after the
	// browser has navigated to a URL that will fail mid-stream.
	_ = q.filter()
	if err := q.err(); err != nil {
		writeError(w, r, err)
		return
	}

	query := r.URL.Query()
	query.Del(middleware.DownloadQueryParam)
	canonical := query.Encode()

	token, expiresAt, err := h.downloads.Issue(subject, canonical)
	if err != nil {
		writeError(w, r, err)
		return
	}

	signed := query
	signed.Set(middleware.DownloadQueryParam, token)

	writeJSON(w, http.StatusOK, downloadTicket{
		URL:       "/api/v1/reports/expenses/export?" + signed.Encode(),
		ExpiresAt: expiresAt,
	})
}
