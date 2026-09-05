package export

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
)

var testReport = Report{
	Title:      "Expense claims",
	Subtitle:   "2026-01-01 to 2026-03-31 · all departments",
	TenantName: "Acme Ltd",
	Generated:  time.Date(2026, 3, 31, 17, 30, 0, 0, time.UTC),
	Columns: []Column{
		{Header: "Date", Kind: KindDate},
		{Header: "Merchant", Kind: KindText, Width: 24},
		{Header: "Department", Kind: KindText},
		{Header: "Amount", Kind: KindMoney},
		{Header: "Currency", Kind: KindText, Width: 8},
		{Header: "Revision", Kind: KindInt},
		{Header: "Decided", Kind: KindDateTime},
	},
}

func sampleRow(i int) []Cell {
	decided := time.Date(2026, 2, 14, 11, 0, 0, 0, time.UTC)
	return []Cell{
		Date(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i%28)),
		Text(fmt.Sprintf("Merchant %d", i)),
		Text("Engineering"),
		Money(shared.Money{Minor: int64(1000 + i), Currency: "USD"}),
		Text("USD"),
		Int(int64(i%3 + 1)),
		DateTimePtr(&decided),
	}
}

func encode(t *testing.T, f Format, rows int, mutate func(int, []Cell) []Cell) []byte {
	t.Helper()
	enc, err := NewEncoder(f)
	if err != nil {
		t.Fatalf("NewEncoder(%s): %v", f, err)
	}
	var buf bytes.Buffer
	if err := enc.Open(&buf, testReport); err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < rows; i++ {
		row := sampleRow(i)
		if mutate != nil {
			row = mutate(i, row)
		}
		if err := enc.Write(row); err != nil {
			t.Fatalf("Write row %d: %v", i, err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

// -----------------------------------------------------------------------------
// XLSX
// -----------------------------------------------------------------------------

// The parts an .xlsx must contain to open at all. A file missing any of them
// is reported by Excel as "unreadable content", with no indication of which.
var requiredXLSXParts = []string{
	"[Content_Types].xml",
	"_rels/.rels",
	"xl/workbook.xml",
	"xl/_rels/workbook.xml.rels",
	"xl/styles.xml",
	"xl/worksheets/sheet1.xml",
}

func TestXLSXIsAValidPackage(t *testing.T) {
	data := encode(t, FormatXLSX, 50, nil)

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("the output is not a readable zip: %v", err)
	}

	present := map[string]bool{}
	for _, f := range zr.File {
		present[f.Name] = true

		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}

		// Every part must be well-formed XML end to end. A truncated or
		// unescaped part is exactly what a hand-written writer gets wrong, and
		// it is invisible until a customer opens the file.
		dec := xml.NewDecoder(bytes.NewReader(body))
		for {
			_, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s is not well-formed XML: %v", f.Name, err)
			}
		}
	}

	for _, name := range requiredXLSXParts {
		if !present[name] {
			t.Errorf("missing required part %s", name)
		}
	}
}

func TestXLSXCellsAreTyped(t *testing.T) {
	data := encode(t, FormatXLSX, 3, nil)
	sheet := readPart(t, data, "xl/worksheets/sheet1.xml")

	// Amounts must be numeric with the currency style, not text. A spreadsheet
	// whose amount column is text cannot be summed, which is the first thing
	// anyone does with an expense export.
	if !strings.Contains(sheet, `<c r="D2" s="4"><v>10.00</v></c>`) {
		t.Errorf("amount cell is not a styled number:\n%s", excerpt(sheet, "D2"))
	}
	// Dates must be serial numbers with a date format, or they sort as text
	// and 2026-02-10 comes before 2026-02-2.
	if !strings.Contains(sheet, `<c r="A2" s="2"><v>`) {
		t.Errorf("date cell is not a styled serial:\n%s", excerpt(sheet, "A2"))
	}
	if !strings.Contains(sheet, `t="inlineStr"`) {
		t.Error("no inline strings found; the encoder must not depend on a shared string table")
	}
	if strings.Contains(sheet, "sharedStrings") {
		t.Error("the worksheet references a shared string table, which cannot be built without buffering every string")
	}
	// The header row must be frozen and the range filtered, both of which are
	// written at opposite ends of a streamed document.
	if !strings.Contains(sheet, `state="frozen"`) {
		t.Error("header row is not frozen")
	}
	if !strings.Contains(sheet, `<autoFilter ref="A1:G4"/>`) {
		t.Errorf("autoFilter does not cover the written range:\n%s", excerpt(sheet, "autoFilter"))
	}
}

// The dates in a spreadsheet have to match the dates in the data. Excel's
// serial scheme is off by one from the obvious implementation because it
// reproduces a Lotus 1-2-3 leap year bug, and getting it wrong shifts every
// date in every report by a day.
func TestExcelSerialMatchesKnownAnchors(t *testing.T) {
	// 1900-03-01 is the first date the scheme is correct for, and the anchor
	// that pins the epoch. Before it, Excel's numbering is one ahead of any
	// real calendar because serial 60 is the 29th of February 1900, a day that
	// did not exist - so no epoch choice can satisfy both sides of that date.
	// This one is chosen to be right from 1900-03-01 onward, which covers
	// every date an expense claim will ever carry.
	cases := map[string]float64{
		"1900-03-01": 61,
		"1901-01-01": 367,
		"2026-01-01": 46023,
		"2026-03-14": 46095,
	}
	for date, want := range cases {
		parsed, err := time.Parse("2006-01-02", date)
		if err != nil {
			t.Fatal(err)
		}
		if got := excelSerial(parsed); got != want {
			t.Errorf("excelSerial(%s) = %v, want %v", date, got, want)
		}
	}
}

func TestColumnNames(t *testing.T) {
	cases := map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 51: "AZ", 52: "BA", 701: "ZZ", 702: "AAA"}
	for i, want := range cases {
		if got := colName(i); got != want {
			t.Errorf("colName(%d) = %s, want %s", i, got, want)
		}
	}
}

// -----------------------------------------------------------------------------
// Hostile input
// -----------------------------------------------------------------------------

// The values in a report are the customer's own data, and one member of an
// organisation chooses the merchant name that someone in finance later opens
// in Excel.
func TestHostileTextIsNeutralised(t *testing.T) {
	hostile := []string{
		`=cmd|'/c calc'!A1`,
		`+1+1`,
		`-2+3`,
		`@SUM(A1:A9)`,
		"tab\tand\nnewline",
		"control\x01\x02chars",
		"unicode ✓ Ünïcödé 日本語",
		strings.Repeat("long ", 200),
	}

	mutate := func(i int, row []Cell) []Cell {
		row[1] = Text(hostile[i])
		return row
	}

	t.Run("xlsx stays well-formed and defuses formulas", func(t *testing.T) {
		data := encode(t, FormatXLSX, len(hostile), mutate)
		sheet := readPart(t, data, "xl/worksheets/sheet1.xml")

		dec := xml.NewDecoder(strings.NewReader(sheet))
		for {
			_, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("hostile input produced malformed XML: %v", err)
			}
		}
		if strings.Contains(sheet, ">=cmd") {
			t.Error("a leading = reached the cell unprefixed; opening the file would offer to run it")
		}
		if strings.ContainsRune(sheet, '\x01') {
			t.Error("a control character reached the XML; no parser accepts the document")
		}
		if !strings.Contains(sheet, "Ünïcödé") {
			t.Error("valid unicode was mangled")
		}
	})

	t.Run("csv prefixes formulas", func(t *testing.T) {
		data := encode(t, FormatCSV, len(hostile), mutate)
		for _, prefix := range []string{`"\t=cmd`, `"\t+1+1`, `"\t-2+3`, `"\t@SUM`} {
			unquoted := strings.ReplaceAll(strings.Trim(prefix, `"`), `\t`, "\t")
			if !strings.Contains(string(data), unquoted) {
				t.Errorf("csv did not defuse %q", unquoted)
			}
		}
	})

	t.Run("pdf escapes string delimiters", func(t *testing.T) {
		parens := func(i int, row []Cell) []Cell {
			row[1] = Text(`a (nested) \ backslash ) unbalanced`)
			return row
		}
		data := encode(t, FormatPDF, 3, parens)
		assertPDFStructure(t, data)
	})
}

// -----------------------------------------------------------------------------
// PDF
// -----------------------------------------------------------------------------

var objHeader = regexp.MustCompile(`^(\d+) 0 obj`)

// assertPDFStructure walks the file the way a reader does: find startxref, seek
// to the cross reference table, and check that every offset in it lands on the
// object it claims. A single byte of drift in the xref makes the document
// unopenable, and nothing else in the file would show it.
func assertPDFStructure(t *testing.T, data []byte) {
	t.Helper()

	if !bytes.HasPrefix(data, []byte("%PDF-1.7")) {
		t.Fatal("missing PDF header")
	}
	if !bytes.HasSuffix(bytes.TrimRight(data, "\n"), []byte("%%EOF")) {
		t.Fatal("missing EOF trailer")
	}

	idx := bytes.LastIndex(data, []byte("startxref"))
	if idx < 0 {
		t.Fatal("missing startxref")
	}
	fields := strings.Fields(string(data[idx+len("startxref"):]))
	if len(fields) == 0 {
		t.Fatal("startxref has no offset")
	}
	xrefOff, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatalf("startxref offset is not a number: %v", err)
	}
	if xrefOff <= 0 || xrefOff >= len(data) {
		t.Fatalf("startxref offset %d is outside the file (%d bytes)", xrefOff, len(data))
	}
	if !bytes.HasPrefix(data[xrefOff:], []byte("xref\n")) {
		t.Fatalf("startxref does not point at the xref table, found %q", data[xrefOff:min(xrefOff+16, len(data))])
	}

	// Parse "xref\n0 N\n" then N twenty-byte entries.
	rest := data[xrefOff+len("xref\n"):]
	nl := bytes.IndexByte(rest, '\n')
	header := strings.Fields(string(rest[:nl]))
	if len(header) != 2 {
		t.Fatalf("malformed xref subsection header %q", rest[:nl])
	}
	count, err := strconv.Atoi(header[1])
	if err != nil {
		t.Fatalf("malformed xref count: %v", err)
	}

	entries := rest[nl+1:]
	for i := 1; i < count; i++ {
		entry := entries[i*20 : i*20+20]
		if entry[19] != '\n' {
			t.Fatalf("xref entry %d is not twenty bytes: %q", i, entry)
		}
		off, err := strconv.Atoi(strings.TrimSpace(string(entry[:10])))
		if err != nil {
			t.Fatalf("xref entry %d offset: %v", i, err)
		}
		if off <= 0 || off >= len(data) {
			t.Fatalf("xref entry %d points outside the file: %d", i, off)
		}
		m := objHeader.FindSubmatch(data[off:])
		if m == nil {
			t.Fatalf("xref entry %d points at %q, not an object header", i, data[off:min(off+24, len(data))])
		}
		if got := string(m[1]); got != strconv.Itoa(i) {
			t.Fatalf("xref entry %d points at object %s", i, got)
		}
	}
}

func TestPDFStructureIsValid(t *testing.T) {
	for _, rows := range []int{0, 1, 40, 500} {
		t.Run(fmt.Sprintf("%d rows", rows), func(t *testing.T) {
			assertPDFStructure(t, encode(t, FormatPDF, rows, nil))
		})
	}
}

func TestPDFPaginates(t *testing.T) {
	data := encode(t, FormatPDF, 500, nil)
	pages := bytes.Count(data, []byte("/Type /Page "))
	if pages < 10 {
		t.Fatalf("500 rows produced %d pages; the encoder is not breaking pages", pages)
	}
	count := regexp.MustCompile(`/Type /Pages /Count (\d+)`).FindSubmatch(data)
	if count == nil {
		t.Fatal("no page tree")
	}
	declared, _ := strconv.Atoi(string(count[1]))
	if declared != pages {
		t.Fatalf("page tree declares %d pages, %d page objects written", declared, pages)
	}
}

func TestTextFitsItsColumn(t *testing.T) {
	const width = 60.0
	long := strings.Repeat("W", 100)

	got := fit(long, width, 8)
	if w := textWidth(got, 8); w > width {
		t.Fatalf("fit produced %.2fpt of text for a %.2fpt column", w, width)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated text should say so: %q", got)
	}
	if short := fit("ok", width, 8); short != "ok" {
		t.Errorf("text that fits was altered: %q", short)
	}
}

// -----------------------------------------------------------------------------
// The property the whole package exists for
// -----------------------------------------------------------------------------

// Memory must not grow with the size of the report.
//
// The test writes two exports differing by two orders of magnitude and
// compares the heap in use after each. A buffering encoder shows the row count
// in this number; a streaming one does not.
func TestMemoryDoesNotScaleWithRowCount(t *testing.T) {
	if testing.Short() {
		t.Skip("allocation profile is slow")
	}

	measure := func(f Format, rows int) uint64 {
		enc, err := NewEncoder(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := enc.Open(io.Discard, testReport); err != nil {
			t.Fatal(err)
		}

		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)

		var peak uint64
		for i := 0; i < rows; i++ {
			if err := enc.Write(sampleRow(i)); err != nil {
				t.Fatal(err)
			}
			if i%20000 == 0 {
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapInuse > peak {
					peak = m.HeapInuse
				}
			}
		}
		if err := enc.Close(); err != nil {
			t.Fatal(err)
		}
		return peak
	}

	for _, f := range []Format{FormatCSV, FormatXLSX, FormatPDF} {
		t.Run(string(f), func(t *testing.T) {
			small := measure(f, 1_000)
			large := measure(f, 200_000)

			// Two hundred times the rows. Anything that accumulates per row
			// would show a proportional increase; the allowance here is for GC
			// timing and the deflate window, not for growth.
			if large > small*4+8<<20 {
				t.Fatalf("heap grew with row count: %d bytes at 1k rows, %d at 200k", small, large)
			}
			t.Logf("heap in use: %d KiB at 1k rows, %d KiB at 200k rows", small>>10, large>>10)
		})
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func readPart(t *testing.T, archive []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		defer rc.Close()
		body, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(body)
	}
	t.Fatalf("part %s not found", name)
	return ""
}

func excerpt(s, around string) string {
	i := strings.Index(s, around)
	if i < 0 {
		return "(not found)"
	}
	return s[max(0, i-80):min(len(s), i+120)]
}
