package export

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// CSVEncoder streams RFC 4180 rows.
//
// It exists for the ranges where a spreadsheet is the wrong tool: a
// four-year export is a file no version of Excel opens comfortably, and CSV
// hands it to whatever the customer actually processes it with.
type CSVEncoder struct {
	w      *csv.Writer
	cols   int
	record []string
}

func (e *CSVEncoder) ContentType() string { return "text/csv; charset=utf-8" }
func (e *CSVEncoder) Extension() string   { return "csv" }

func (e *CSVEncoder) Open(w io.Writer, r Report) error {
	e.w = csv.NewWriter(w)
	e.cols = len(r.Columns)
	// One record buffer, reused. The encoder is used once per request and
	// never concurrently, so reusing it costs nothing and saves an allocation
	// per row - which at a hundred thousand rows is worth having.
	e.record = make([]string, e.cols)

	// A UTF-8 BOM. Excel on Windows opens a BOM-less UTF-8 CSV as the system
	// code page, which turns every non-ASCII merchant name into mojibake. Tools
	// that read CSV properly skip the BOM. It is three bytes to make the common
	// case work.
	if _, err := io.WriteString(w, "\xef\xbb\xbf"); err != nil {
		return fmt.Errorf("write bom: %w", err)
	}

	headers := make([]string, e.cols)
	for i, c := range r.Columns {
		headers[i] = c.Header
	}
	return e.w.Write(headers)
}

func (e *CSVEncoder) Write(row []Cell) error {
	if len(row) != e.cols {
		return fmt.Errorf("export: row has %d cells, report declares %d columns", len(row), e.cols)
	}
	for i, cell := range row {
		e.record[i] = csvValue(cell)
	}
	if err := e.w.Write(e.record); err != nil {
		return err
	}
	// csv.Writer buffers; flushing per row would syscall per row. The
	// underlying writer in the handler flushes on its own schedule, so the
	// bytes reach the client without this doing it here.
	return nil
}

func (e *CSVEncoder) Close() error {
	if e.w == nil {
		return nil
	}
	e.w.Flush()
	return e.w.Error()
}

// csvValue renders a cell as text.
//
// Amounts are written as a plain decimal with no currency symbol and no
// thousands separator: a spreadsheet reading "1,234.50" in a locale that uses
// the comma as a decimal separator gets 1.234. The currency travels in its own
// column instead.
func csvValue(c Cell) string {
	switch c.Kind {
	case KindText:
		return sanitizeFormula(c.Text)
	case KindMoney:
		return c.Money.String()
	case KindInt:
		return strconv.FormatInt(c.Int, 10)
	case KindDate:
		if c.Time.IsZero() {
			return ""
		}
		return c.Time.UTC().Format("2006-01-02")
	case KindDateTime:
		if c.Time.IsZero() {
			return ""
		}
		return c.Time.UTC().Format(time.RFC3339)
	default:
		return ""
	}
}

// sanitizeFormula defuses CSV injection.
//
// A merchant name of `=cmd|'/c calc'!A1` is stored faithfully and rendered
// faithfully in the API, but a spreadsheet opening the CSV treats a leading
// =, +, - or @ as a formula and, with one click through the warning, executes
// it on the machine of whoever opened the finance export. The value is the
// customer's own data, which is exactly why it can arrive hostile: one member
// of an organisation can name a merchant, and someone else in finance opens
// the export.
//
// Prefixing with a tab is the OWASP recommendation and is what Excel, Sheets
// and LibreOffice all treat as "this is text". It is visible in the cell,
// which is the price of not executing it.
func sanitizeFormula(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "\t" + s
	}
	return s
}

// ErrShortWrite is returned when the client goes away mid-download. The
// handler treats it as an abort rather than an error worth logging at warn:
// users cancel downloads.
var ErrShortWrite = errors.New("export: writer closed before the report finished")

func isClosedPipe(err error) bool {
	return errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, io.ErrShortWrite) ||
		strings.Contains(err.Error(), "broken pipe") ||
		strings.Contains(err.Error(), "connection reset by peer")
}
