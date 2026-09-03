// Package export streams tabular reports straight to an io.Writer.
//
// The constraint that shapes every encoder here is that no report is ever
// assembled in memory. A tenant with four years of history and a hundred
// thousand claims produces a spreadsheet of roughly 12 MB; buffering it means
// 12 MB per concurrent export, and an endpoint whose memory cost is set by the
// customer's data volume is an endpoint that falls over on the day someone
// exports everything.
//
// So each encoder writes as it goes:
//
//   - CSV is trivially streamable and is the fallback for very large ranges.
//   - XLSX is a ZIP of XML parts. archive/zip writes entries to the response
//     as they are produced, and the worksheet XML is emitted row by row, so
//     the peak footprint is the deflate window rather than the sheet.
//   - PDF needs page geometry, so it buffers exactly one page - a bounded
//     amount that does not grow with the report - and writes each page out
//     as it is finished.
//
// What none of them do is call a library that takes a [][]string.
package export

import (
	"fmt"
	"io"
	"time"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
)

// Format is the requested output. It is parsed from the URL rather than from
// an Accept header: these are downloads, and a user pasting a link into a
// browser has no way to set headers.
type Format string

const (
	FormatCSV  Format = "csv"
	FormatXLSX Format = "xlsx"
	FormatPDF  Format = "pdf"
)

func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatCSV:
		return FormatCSV, nil
	case FormatXLSX:
		return FormatXLSX, nil
	case FormatPDF:
		return FormatPDF, nil
	default:
		return "", shared.FieldError{Field: "format", Detail: "must be one of csv, xlsx or pdf"}
	}
}

// CellKind tells an encoder how to render a value.
//
// It exists because "1234.50" is a string in CSV, a number with a currency
// format in a spreadsheet, and a right-aligned column in a PDF. Passing
// pre-formatted strings would produce a spreadsheet whose amounts cannot be
// summed and whose dates cannot be sorted - the two things people export to a
// spreadsheet in order to do.
type CellKind uint8

const (
	KindText CellKind = iota
	KindMoney
	KindDate
	KindDateTime
	KindInt
)

// Cell is one value. It is a tagged union rather than an `any`, so an encoder
// switching on Kind is exhaustive and a new kind breaks compilation at every
// encoder rather than falling into a default branch.
type Cell struct {
	Kind  CellKind
	Text  string
	Money shared.Money
	Time  time.Time
	Int   int64
}

func Text(s string) Cell { return Cell{Kind: KindText, Text: s} }

// TextPtr renders a NULL column as an empty cell rather than the string
// "<nil>", which is what fmt would produce and what a careless %v in a report
// generator ships to a customer.
func TextPtr(s *string) Cell {
	if s == nil {
		return Cell{Kind: KindText}
	}
	return Cell{Kind: KindText, Text: *s}
}

func Money(m shared.Money) Cell { return Cell{Kind: KindMoney, Money: m} }
func Int(v int64) Cell          { return Cell{Kind: KindInt, Int: v} }

func Date(t time.Time) Cell { return Cell{Kind: KindDate, Time: t} }

func DatePtr(t *time.Time) Cell {
	if t == nil {
		return Cell{Kind: KindText}
	}
	return Cell{Kind: KindDate, Time: *t}
}

func DateTimePtr(t *time.Time) Cell {
	if t == nil {
		return Cell{Kind: KindText}
	}
	return Cell{Kind: KindDateTime, Time: *t}
}

// Column describes one column of the report.
type Column struct {
	Header string
	Kind   CellKind

	// Width is in character units, the unit both Excel's column width and the
	// PDF layout below use. Zero falls back to a width derived from the
	// header, which is right often enough that most columns leave it unset.
	Width float64
}

// Report is the metadata an encoder needs before the first row.
type Report struct {
	// Title appears in the PDF header and as the worksheet name.
	Title string

	// Subtitle carries the filter the report was run with, so a printed copy
	// says what it contains. A report whose scope is not on its face gets
	// misread.
	Subtitle string

	Columns    []Column
	Generated  time.Time
	TenantName string
}

// Encoder writes one report to one writer.
//
// The lifecycle is Open, then Write per row, then Close - and Close must be
// called even on the error path, because for XLSX it writes the ZIP central
// directory without which the file will not open at all.
type Encoder interface {
	Open(w io.Writer, r Report) error
	Write(row []Cell) error
	Close() error

	ContentType() string
	Extension() string
}

func NewEncoder(f Format) (Encoder, error) {
	switch f {
	case FormatCSV:
		return &CSVEncoder{}, nil
	case FormatXLSX:
		return &XLSXEncoder{}, nil
	case FormatPDF:
		return &PDFEncoder{}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported export format %q", shared.ErrValidation, f)
	}
}

// excelEpoch is 1899-12-30, not 1899-12-31.
//
// Excel's serial date scheme deliberately reproduces a Lotus 1-2-3 bug that
// treated 1900 as a leap year. Anchoring two days before 1900-01-01 makes
// every date from 1900-03-01 onward come out right, which is every date any
// expense report will ever contain.
var excelEpoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

func excelSerial(t time.Time) float64 {
	utc := t.UTC()
	days := utc.Sub(excelEpoch).Hours() / 24
	return days
}
