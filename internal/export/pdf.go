package export

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

// PDFEncoder writes a paginated table as a PDF, one page at a time.
//
// PDF is the one format here that cannot be written a row at a time, because a
// page is a unit of layout: the encoder has to know a row does not fit before
// it can decide to start a new page. What it does not need is the whole
// report. It buffers exactly one page's content stream - a bounded amount that
// does not grow with the number of rows - writes that page out, and starts the
// next. A ten-thousand-row export costs the same memory as a ten-row one.
//
// The other thing PDF appears to need up front is the cross-reference table,
// which maps object numbers to byte offsets. It goes at the end of the file by
// design - `startxref` at the very bottom points back at it - so a writer that
// counts the bytes it has emitted can build it as it goes. That is what
// countingWriter is for.
//
// Objects are therefore written out of order: pages as they are finished, then
// the page tree and catalog at the end once the list of pages is known. Object
// numbers 3 and 4 are reserved for those two at the start, so pages can
// reference /Parent 3 0 R before object 3 exists. Nothing in PDF requires
// objects to appear in numeric order; only the xref offsets have to be right.
type PDFEncoder struct {
	w   *countingWriter
	rep Report

	// offsets[n] is the byte offset of object n. Index 0 is the free-list
	// head and is never written.
	offsets []int64

	nextObj  int
	pageObjs []int

	// page is the content stream being built. One page, never more.
	page      bytes.Buffer
	pageRows  int
	pageNum   int
	colX      []float64
	colW      []float64
	y         float64
	closed    bool
	openPages bool
}

// A4 landscape, in PostScript points. Landscape because an expense report is
// wide: eight to twelve columns fit legibly across 842pt and do not across
// 595pt.
const (
	pageWidth  = 842.0
	pageHeight = 595.0

	marginX = 28.0
	marginY = 28.0

	titleSize  = 14.0
	metaSize   = 8.0
	headerSize = 8.0
	bodySize   = 8.0

	rowHeight    = 13.0
	headerHeight = 16.0
)

func (e *PDFEncoder) ContentType() string { return "application/pdf" }
func (e *PDFEncoder) Extension() string   { return "pdf" }

func (e *PDFEncoder) Open(w io.Writer, r Report) error {
	e.w = &countingWriter{w: w}
	e.rep = r
	e.offsets = make([]int64, 5) // 0 free, 1 font, 2 bold font, 3 pages, 4 catalog
	e.nextObj = 5

	e.layoutColumns()

	// PDF 1.7. The binary comment on the second line is required by the spec's
	// own recommendation: it makes any tool that transfers the file in text
	// mode corrupt it visibly rather than subtly.
	if err := e.write("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n"); err != nil {
		return err
	}

	if err := e.writeObject(1, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>"); err != nil {
		return err
	}
	if err := e.writeObject(2, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>"); err != nil {
		return err
	}

	e.beginPage()
	return nil
}

// layoutColumns turns the declared widths into x positions.
//
// Column.Width is in character units, which is what the spreadsheet encoder
// wants. Here they are treated as relative weights and scaled to the printable
// width, so a report reads the same on both formats without carrying two sets
// of numbers.
func (e *PDFEncoder) layoutColumns() {
	printable := pageWidth - 2*marginX

	weights := make([]float64, len(e.rep.Columns))
	var total float64
	for i, c := range e.rep.Columns {
		w := c.Width
		if w <= 0 {
			w = defaultWidth(c)
		}
		weights[i] = w
		total += w
	}

	e.colX = make([]float64, len(weights))
	e.colW = make([]float64, len(weights))
	x := marginX
	for i, w := range weights {
		width := printable * (w / total)
		e.colX[i] = x
		e.colW[i] = width
		x += width
	}
}

func (e *PDFEncoder) beginPage() {
	e.page.Reset()
	e.pageRows = 0
	e.pageNum++
	e.openPages = true
	e.y = pageHeight - marginY

	// Title block, on every page. A page of a printed report that has become
	// separated from page one still has to say what it is.
	e.y -= titleSize
	e.text(marginX, e.y, titleSize, true, e.rep.Title)

	if e.rep.Subtitle != "" {
		e.y -= metaSize + 3
		e.text(marginX, e.y, metaSize, false, e.rep.Subtitle)
	}

	e.y -= metaSize + 3
	meta := fmt.Sprintf("%s  ·  generated %s", e.rep.TenantName, e.rep.Generated.UTC().Format("2006-01-02 15:04 MST"))
	e.text(marginX, e.y, metaSize, false, meta)

	e.y -= 10
	e.writeColumnHeaders()
}

func (e *PDFEncoder) writeColumnHeaders() {
	// Header band.
	fmt.Fprintf(&e.page, "0.122 0.220 0.392 rg\n%.2f %.2f %.2f %.2f re f\n",
		marginX, e.y-headerHeight+4, pageWidth-2*marginX, headerHeight)

	for i, c := range e.rep.Columns {
		e.textColored(e.colX[i]+3, e.y-headerHeight+8, headerSize, true, 1, 1, 1,
			fit(c.Header, e.colW[i]-6, headerSize))
	}
	e.y -= headerHeight + 2
}

func (e *PDFEncoder) Write(row []Cell) error {
	if len(row) != len(e.rep.Columns) {
		return fmt.Errorf("export: row has %d cells, report declares %d columns", len(row), len(e.rep.Columns))
	}

	// Break before drawing, not after: a row half off the bottom of a page is
	// worse than an early break.
	if e.y-rowHeight < marginY+16 {
		if err := e.flushPage(); err != nil {
			return err
		}
		e.beginPage()
	}

	// Zebra striping. On a table this dense it is the difference between
	// following a row across twelve columns and losing it.
	if e.pageRows%2 == 1 {
		fmt.Fprintf(&e.page, "0.949 0.953 0.961 rg\n%.2f %.2f %.2f %.2f re f\n",
			marginX, e.y-rowHeight+3, pageWidth-2*marginX, rowHeight)
	}

	for i, cell := range row {
		text := pdfValue(cell)
		if text == "" {
			continue
		}
		x := e.colX[i] + 3
		avail := e.colW[i] - 6

		// Numbers right-align, which is the only way a column of amounts can
		// be read down.
		if cell.Kind == KindMoney || cell.Kind == KindInt {
			text = fit(text, avail, bodySize)
			x = e.colX[i] + e.colW[i] - 3 - textWidth(text, bodySize)
		} else {
			text = fit(text, avail, bodySize)
		}
		e.text(x, e.y-rowHeight+7, bodySize, false, text)
	}

	e.y -= rowHeight
	e.pageRows++
	return nil
}

// flushPage writes the buffered page as a content stream object and a page
// object, then forgets it.
func (e *PDFEncoder) flushPage() error {
	if !e.openPages {
		return nil
	}
	e.openPages = false

	// Footer, added once the page is otherwise complete so it sits at a fixed
	// position rather than wherever the last row ended.
	footer := fmt.Sprintf("page %d", e.pageNum)
	e.textColored(pageWidth-marginX-textWidth(footer, metaSize), marginY, metaSize, false, 0.4, 0.4, 0.4, footer)

	content := e.page.Bytes()
	contentObj := e.nextObj
	e.nextObj++
	pageObj := e.nextObj
	e.nextObj++

	// /Length must precede the stream data, which is why a page is buffered:
	// the value is not known until the content is complete. It is the only
	// backward dependency in the format and it is bounded by one page.
	if err := e.writeStreamObject(contentObj, content); err != nil {
		return err
	}

	pageDict := fmt.Sprintf(
		"<< /Type /Page /Parent 3 0 R /MediaBox [0 0 %.0f %.0f] "+
			"/Resources << /Font << /F1 1 0 R /F2 2 0 R >> >> /Contents %d 0 R >>",
		pageWidth, pageHeight, contentObj)
	if err := e.writeObject(pageObj, pageDict); err != nil {
		return err
	}

	e.pageObjs = append(e.pageObjs, pageObj)
	e.page.Reset()
	return nil
}

// Close finishes the last page and writes the page tree, catalog, cross
// reference table and trailer.
func (e *PDFEncoder) Close() error {
	if e.w == nil || e.closed {
		return nil
	}
	e.closed = true

	if err := e.flushPage(); err != nil {
		return err
	}

	// An empty report still needs one page, or the file has no /Kids and no
	// viewer will open it.
	if len(e.pageObjs) == 0 {
		e.beginPage()
		e.text(marginX, e.y-20, bodySize, false, "No claims matched this report's filters.")
		if err := e.flushPage(); err != nil {
			return err
		}
	}

	kids := make([]string, len(e.pageObjs))
	for i, obj := range e.pageObjs {
		kids[i] = fmt.Sprintf("%d 0 R", obj)
	}
	pages := fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", len(kids), strings.Join(kids, " "))
	if err := e.writeObject(3, pages); err != nil {
		return err
	}
	if err := e.writeObject(4, "<< /Type /Catalog /Pages 3 0 R >>"); err != nil {
		return err
	}

	return e.writeXref()
}

func (e *PDFEncoder) writeXref() error {
	start := e.w.n
	size := e.nextObj

	var b strings.Builder
	fmt.Fprintf(&b, "xref\n0 %d\n", size)

	// Entry zero is the head of the free list, and every entry is exactly
	// twenty bytes. Readers seek into this table by multiplying the object
	// number by twenty, so a single byte of drift makes the whole file
	// unreadable.
	b.WriteString("0000000000 65535 f \n")
	for i := 1; i < size; i++ {
		var off int64
		if i < len(e.offsets) {
			off = e.offsets[i]
		}
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}

	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 4 0 R >>\nstartxref\n%d\n%%%%EOF\n", size, start)
	return e.write(b.String())
}

func (e *PDFEncoder) writeObject(num int, body string) error {
	e.recordOffset(num)
	return e.write(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", num, body))
}

func (e *PDFEncoder) writeStreamObject(num int, data []byte) error {
	e.recordOffset(num)
	if err := e.write(fmt.Sprintf("%d 0 obj\n<< /Length %d >>\nstream\n", num, len(data))); err != nil {
		return err
	}
	if _, err := e.w.Write(data); err != nil {
		return err
	}
	return e.write("\nendstream\nendobj\n")
}

func (e *PDFEncoder) recordOffset(num int) {
	for len(e.offsets) <= num {
		e.offsets = append(e.offsets, 0)
	}
	e.offsets[num] = e.w.n
}

func (e *PDFEncoder) write(s string) error {
	_, err := io.WriteString(e.w, s)
	return err
}

// text draws a string in black.
func (e *PDFEncoder) text(x, y, size float64, bold bool, s string) {
	e.textColored(x, y, size, bold, 0, 0, 0, s)
}

func (e *PDFEncoder) textColored(x, y, size float64, bold bool, r, g, b float64, s string) {
	if s == "" {
		return
	}
	font := "F1"
	if bold {
		font = "F2"
	}
	fmt.Fprintf(&e.page, "%.3f %.3f %.3f rg\nBT /%s %.1f Tf %.2f %.2f Td (%s) Tj ET\n",
		r, g, b, font, size, x, y, pdfString(s))
}

func pdfValue(c Cell) string {
	switch c.Kind {
	case KindText:
		return c.Text
	case KindMoney:
		return c.Money.String() + " " + string(c.Money.Currency)
	case KindInt:
		return fmt.Sprintf("%d", c.Int)
	case KindDate:
		if c.Time.IsZero() {
			return ""
		}
		return c.Time.UTC().Format("2006-01-02")
	case KindDateTime:
		if c.Time.IsZero() {
			return ""
		}
		return c.Time.UTC().Format("2006-01-02 15:04")
	default:
		return ""
	}
}

// pdfString escapes a literal string for the ( ) syntax and folds it into
// WinAnsi.
//
// The base-14 fonts are single-byte with WinAnsiEncoding, so anything outside
// Latin-1 has no glyph. Emitting the UTF-8 bytes raw would render as mojibake,
// which looks like data corruption in a document a customer files with their
// accounts. Substituting '?' is visibly lossy, which is the honest failure.
// Full Unicode would mean embedding a font subset, and that is a larger change
// than this report format justifies.
func pdfString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '(':
			b.WriteString(`\(`)
		case r == ')':
			b.WriteString(`\)`)
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20:
			// Control characters have no glyph and would end the string
			// literal in some readers.
		case r < 0x7F:
			b.WriteRune(r)
		case r >= 0xA0 && r <= 0xFF:
			b.WriteByte(byte(r))
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

// fit truncates text to a width, appending an ellipsis when it has to.
//
// A cell that overflows its column in a PDF does not wrap or clip - it draws
// straight over the next column, and the result is unreadable rather than
// merely truncated.
func fit(s string, width, size float64) string {
	if width <= 0 {
		return ""
	}
	if textWidth(s, size) <= width {
		return s
	}

	ellipsis := textWidth("...", size)
	runes := []rune(s)
	var acc float64
	for i, r := range runes {
		w := runeWidth(r) * size / 1000
		if acc+w+ellipsis > width {
			if i == 0 {
				return ""
			}
			return string(runes[:i]) + "..."
		}
		acc += w
	}
	return s
}

func textWidth(s string, size float64) float64 {
	var total float64
	for _, r := range s {
		total += runeWidth(r)
	}
	return total * size / 1000
}

// helveticaWidths holds the advance widths for ASCII 32..126 from Adobe's
// Helvetica AFM, in units of 1/1000 em.
//
// A fixed estimate would be simpler and wrong in a way that shows: 'i' and 'W'
// differ by more than four to one, so a column of names estimated at an
// average width either overflows or wastes half the page. These are the real
// numbers, and they are what makes fit() truncate at the right character.
var helveticaWidths = [95]float64{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
}

func runeWidth(r rune) float64 {
	if r >= 32 && r <= 126 {
		return helveticaWidths[r-32]
	}
	// Latin-1 accented letters are close enough to their unaccented forms, and
	// everything else is rendered as '?'.
	if r >= 0xA0 && r <= 0xFF {
		return 556
	}
	return helveticaWidths['?'-32]
}

// countingWriter tracks how many bytes have been written, which is what makes
// the cross reference table constructible without buffering the document.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
