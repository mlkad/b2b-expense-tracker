package export

import (
	"archive/zip"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// XLSXEncoder writes a real .xlsx straight to the response.
//
// An .xlsx is a ZIP of XML parts, and both halves of that are streamable:
// archive/zip emits each entry's local header and deflated bytes as they are
// produced, and a worksheet is a flat sequence of <row> elements. So the
// bytes of row one are on the wire before row two has been read from the
// database, and the encoder's memory does not grow with the report.
//
// Two decisions make that possible and are worth stating, because the usual
// spreadsheet libraries make the opposite ones:
//
// Inline strings, not a shared string table. The conventional XLSX layout
// interns every string in xl/sharedStrings.xml and has cells reference it by
// index. That is smaller for repetitive data - and it requires holding every
// distinct string in memory until the last row is known, which is precisely
// the buffering being avoided. Writing t="inlineStr" puts the text in the cell
// and costs some bytes, which deflate largely takes back.
//
// No <dimension> element. It declares the used range, and the used range is
// only known at the end. It is optional; Excel computes the range itself.
//
// The result opens in Excel, Numbers, LibreOffice and Google Sheets, with
// typed cells: amounts sum, dates sort, and the header row filters.
type XLSXEncoder struct {
	zw    *zip.Writer
	sheet io.Writer

	cols     []Column
	rowIndex int
	closed   bool
}

func (e *XLSXEncoder) ContentType() string {
	return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
}

func (e *XLSXEncoder) Extension() string { return "xlsx" }

func (e *XLSXEncoder) Open(w io.Writer, r Report) error {
	e.zw = zip.NewWriter(w)
	e.cols = r.Columns

	sheetName := sheetTitle(r.Title)

	// The fixed parts go in first. All of them are small and their content is
	// known before the first row, so they are written and finished; the
	// worksheet entry is left open and streamed into.
	parts := []struct {
		name string
		body string
	}{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", rootRelsXML},
		{"xl/workbook.xml", fmt.Sprintf(workbookXML, xmlAttr(sheetName))},
		{"xl/_rels/workbook.xml.rels", workbookRelsXML},
		{"xl/styles.xml", stylesXML},
	}
	for _, p := range parts {
		if err := e.writePart(p.name, p.body); err != nil {
			return err
		}
	}

	sheet, err := e.zw.CreateHeader(&zip.FileHeader{
		Name:     "xl/worksheets/sheet1.xml",
		Method:   zip.Deflate,
		Modified: r.Generated,
	})
	if err != nil {
		return fmt.Errorf("create worksheet entry: %w", err)
	}
	e.sheet = sheet

	if err := e.writeSheetPrologue(); err != nil {
		return err
	}
	return e.writeHeaderRow()
}

func (e *XLSXEncoder) writePart(name, body string) error {
	f, err := e.zw.Create(name)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	if _, err := io.WriteString(f, body); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

func (e *XLSXEncoder) writeSheetPrologue() error {
	var b strings.Builder
	b.WriteString(xmlDecl)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)

	// Freeze the header row. On a report of any length this is the difference
	// between a usable spreadsheet and one where the reader scrolls back up to
	// remember which column is which.
	b.WriteString(`<sheetViews><sheetView workbookViewId="0">` +
		`<pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>` +
		`</sheetView></sheetViews>`)

	b.WriteString(`<sheetFormatPr defaultRowHeight="15"/>`)

	b.WriteString(`<cols>`)
	for i, c := range e.cols {
		width := c.Width
		if width <= 0 {
			width = defaultWidth(c)
		}
		fmt.Fprintf(&b, `<col min="%d" max="%d" width="%.2f" customWidth="1"/>`, i+1, i+1, width)
	}
	b.WriteString(`</cols>`)

	b.WriteString(`<sheetData>`)
	_, err := io.WriteString(e.sheet, b.String())
	return err
}

func (e *XLSXEncoder) writeHeaderRow() error {
	e.rowIndex = 1

	var b strings.Builder
	b.WriteString(`<row r="1" s="1" customFormat="1">`)
	for i, c := range e.cols {
		writeInlineString(&b, colName(i), 1, styleHeader, c.Header)
	}
	b.WriteString(`</row>`)

	_, err := io.WriteString(e.sheet, b.String())
	return err
}

func (e *XLSXEncoder) Write(row []Cell) error {
	if len(row) != len(e.cols) {
		return fmt.Errorf("export: row has %d cells, report declares %d columns", len(row), len(e.cols))
	}
	e.rowIndex++

	// One builder per row, not one per report. It is reset by going out of
	// scope, and the alternative - a shared buffer - would need resetting
	// anyway and would make the encoder unsafe to reuse.
	var b strings.Builder
	b.Grow(len(row) * 48)

	fmt.Fprintf(&b, `<row r="%d">`, e.rowIndex)
	for i, cell := range row {
		ref := colName(i)
		switch cell.Kind {
		case KindText:
			if cell.Text == "" {
				continue // an omitted cell is an empty cell, and is smaller
			}
			writeInlineString(&b, ref, e.rowIndex, styleDefault, cell.Text)

		case KindMoney:
			// Written as a number in major units so the column sums. The
			// division is done in decimal string space by Money.String and
			// re-parsed here rather than as minor/100.0 in float, so a value
			// like 1234567890123 does not lose its last digits on the way
			// through a float64.
			writeNumber(&b, ref, e.rowIndex, styleMoney, cell.Money.String())

		case KindInt:
			writeNumber(&b, ref, e.rowIndex, styleInt, strconv.FormatInt(cell.Int, 10))

		case KindDate:
			if cell.Time.IsZero() {
				continue
			}
			writeNumber(&b, ref, e.rowIndex, styleDate,
				strconv.FormatFloat(excelSerial(cell.Time), 'f', -1, 64))

		case KindDateTime:
			if cell.Time.IsZero() {
				continue
			}
			writeNumber(&b, ref, e.rowIndex, styleDateTime,
				strconv.FormatFloat(excelSerial(cell.Time), 'f', -1, 64))
		}
	}
	b.WriteString(`</row>`)

	_, err := io.WriteString(e.sheet, b.String())
	return err
}

// Close finishes the worksheet and writes the ZIP central directory.
//
// It must be called even when the report failed part way through: without the
// central directory the response is not a ZIP at all, and the browser saves a
// file that no application can open. A truncated-but-valid spreadsheet is a
// better failure than a corrupt one, and the handler pairs this with an
// explicit abort so the client also sees the transfer did not complete.
func (e *XLSXEncoder) Close() error {
	if e.zw == nil || e.closed {
		return nil
	}
	e.closed = true

	var b strings.Builder
	b.WriteString(`</sheetData>`)

	// autoFilter comes after sheetData in the schema, which is convenient: by
	// the time it is written the row count is known, so the filter covers the
	// real range rather than just the header.
	if e.rowIndex >= 1 && len(e.cols) > 0 {
		fmt.Fprintf(&b, `<autoFilter ref="A1:%s%d"/>`, colName(len(e.cols)-1), e.rowIndex)
	}
	b.WriteString(`</worksheet>`)

	if _, err := io.WriteString(e.sheet, b.String()); err != nil {
		return err
	}
	return e.zw.Close()
}

// -----------------------------------------------------------------------------
// Cell writers
// -----------------------------------------------------------------------------

// Style indices into the cellXfs list in stylesXML. They are constants rather
// than magic numbers because the order in that XML is the contract.
const (
	styleDefault  = 0
	styleHeader   = 1
	styleDate     = 2
	styleDateTime = 3
	styleMoney    = 4
	styleInt      = 5
)

func writeInlineString(b *strings.Builder, col string, row, style int, text string) {
	b.WriteString(`<c r="`)
	b.WriteString(col)
	b.WriteString(strconv.Itoa(row))
	b.WriteString(`" t="inlineStr"`)
	if style != styleDefault {
		fmt.Fprintf(b, ` s="%d"`, style)
	}
	// xml:space="preserve" keeps leading and trailing spaces, which matters
	// because sanitizeFormula prefixes a tab to defuse formula injection and
	// a normalising parser would strip it straight back off.
	b.WriteString(`><is><t xml:space="preserve">`)
	xmlEscape(b, sanitizeFormula(text))
	b.WriteString(`</t></is></c>`)
}

func writeNumber(b *strings.Builder, col string, row, style int, value string) {
	b.WriteString(`<c r="`)
	b.WriteString(col)
	b.WriteString(strconv.Itoa(row))
	b.WriteString(`"`)
	if style != styleDefault {
		fmt.Fprintf(b, ` s="%d"`, style)
	}
	b.WriteString(`><v>`)
	b.WriteString(value)
	b.WriteString(`</v></c>`)
}

// colName converts a zero-based index to a spreadsheet column label. It is
// bijective base-26, not ordinary base-26: after Z comes AA, and there is no
// digit zero, which is why the decrement is inside the loop.
func colName(i int) string {
	name := make([]byte, 0, 3)
	for i >= 0 {
		name = append(name, byte('A'+i%26))
		i = i/26 - 1
	}
	// Digits were produced least significant first.
	for l, r := 0, len(name)-1; l < r; l, r = l+1, r-1 {
		name[l], name[r] = name[r], name[l]
	}
	return string(name)
}

func defaultWidth(c Column) float64 {
	base := float64(utf8.RuneCountInString(c.Header)) + 4
	switch c.Kind {
	case KindMoney:
		base = maxFloat(base, 14)
	case KindDate:
		base = maxFloat(base, 12)
	case KindDateTime:
		base = maxFloat(base, 20)
	case KindText:
		base = maxFloat(base, 18)
	}
	return minFloat(base, 60)
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// xmlEscape writes text as XML character data.
//
// encoding/xml's EscapeText is not used because it also escapes newlines and
// tabs as character references, and because it cannot be told to drop the
// characters that are simply not representable. XML 1.0 has no way to encode
// most control characters - not even as a numeric reference - so a merchant
// name containing a stray 0x01 produces a file every parser rejects. They are
// dropped rather than escaped, which is the only option that yields a valid
// document.
func xmlEscape(b *strings.Builder, s string) {
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		case '\t', '\n', '\r':
			b.WriteRune(r)
		default:
			if r < 0x20 || (r >= 0x7F && r <= 0x9F) || r == 0xFFFE || r == 0xFFFF {
				continue
			}
			if r == utf8.RuneError {
				// Invalid UTF-8 in the source. Emitting it would produce a
				// document no parser accepts.
				continue
			}
			b.WriteRune(r)
		}
	}
}

func xmlAttr(s string) string {
	var b strings.Builder
	xmlEscape(&b, s)
	return b.String()
}

// sheetTitle trims a report title to something Excel accepts as a worksheet
// name: at most 31 characters, and none of : \ / ? * [ ].
func sheetTitle(title string) string {
	if title == "" {
		title = "Report"
	}
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case ':', '\\', '/', '?', '*', '[', ']':
			return '-'
		}
		return r
	}, title)

	runes := []rune(cleaned)
	if len(runes) > 31 {
		runes = runes[:31]
	}
	return strings.TrimSpace(string(runes))
}

// -----------------------------------------------------------------------------
// Static parts
// -----------------------------------------------------------------------------

const xmlDecl = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`

const contentTypesXML = xmlDecl + `
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
  <Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
  <Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>`

const rootRelsXML = xmlDecl + `
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`

const workbookXML = xmlDecl + `
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"
          xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets><sheet name="%s" sheetId="1" r:id="rId1"/></sheets>
</workbook>`

const workbookRelsXML = xmlDecl + `
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`

// stylesXML declares the six cell formats the encoder references by index.
//
// The empty <fill> at index 0 and the gray125 at index 1 are not decoration:
// the spec reserves those two slots, and Excel silently renders every fill in
// the file with the wrong colour if they are missing.
const stylesXML = xmlDecl + `
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <numFmts count="3">
    <numFmt numFmtId="164" formatCode="yyyy\-mm\-dd"/>
    <numFmt numFmtId="165" formatCode="yyyy\-mm\-dd\ hh:mm"/>
    <numFmt numFmtId="166" formatCode="#,##0.00"/>
  </numFmts>
  <fonts count="2">
    <font><sz val="11"/><name val="Calibri"/></font>
    <font><b/><sz val="11"/><color rgb="FFFFFFFF"/><name val="Calibri"/></font>
  </fonts>
  <fills count="3">
    <fill><patternFill patternType="none"/></fill>
    <fill><patternFill patternType="gray125"/></fill>
    <fill><patternFill patternType="solid"><fgColor rgb="FF1F3864"/><bgColor indexed="64"/></patternFill></fill>
  </fills>
  <borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>
  <cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
  <cellXfs count="6">
    <xf numFmtId="0"   fontId="0" fillId="0" borderId="0" xfId="0"/>
    <xf numFmtId="0"   fontId="1" fillId="2" borderId="0" xfId="0" applyFont="1" applyFill="1"/>
    <xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>
    <xf numFmtId="165" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>
    <xf numFmtId="166" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>
    <xf numFmtId="1"   fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>
  </cellXfs>
  <!-- The "Normal" named style. Excel tolerates its absence; other readers
       warn that the workbook has no default style and substitute their own,
       which changes the font of every unstyled cell. -->
  <cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>
</styleSheet>`
