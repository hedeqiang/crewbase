package attachments

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// MaxTextRunes caps the extracted text a model sees inline; the whole file
// stays in the workspace for the agent's own tools.
const MaxTextRunes = 120_000

// maxXMLBytes caps one decompressed XML part of an Office file (zip bombs).
const maxXMLBytes = 64 << 20

// Extract returns the readable text of a file, or ok=false when the type
// has no text form here (images, legacy binary Office files).
func Extract(name, mime string, data []byte) (text string, ok bool, err error) {
	defer func() {
		// third-party parsers may panic on malformed input
		if r := recover(); r != nil {
			text, ok, err = "", false, fmt.Errorf("could not read %s: %v", name, r)
		}
	}()
	switch mime {
	case "application/pdf":
		t, err := pdfText(data)
		return t, err == nil, err
	case mimeDOCX:
		t, err := officeText(data, docxParts, "p", nil)
		return t, err == nil, err
	case mimePPTX:
		t, err := officeText(data, pptxParts, "p", func(i int) string { return fmt.Sprintf("## 幻灯片 %d\n", i+1) })
		return t, err == nil, err
	case mimeXLSX:
		t, err := xlsxText(data)
		return t, err == nil, err
	}
	if isText(mime) && utf8.Valid(data) {
		return string(data), true, nil
	}
	return "", false, nil
}

func isText(mime string) bool {
	return strings.HasPrefix(mime, "text/") || mime == "application/json" || mime == "application/xml" ||
		mime == "application/yaml" || mime == "application/toml" || mime == "application/x-sh"
}

// clipText caps text at MaxTextRunes.
func clipText(s string) (string, bool) {
	if utf8.RuneCountInString(s) <= MaxTextRunes {
		return s, false
	}
	r := []rune(s)
	return string(r[:MaxTextRunes]), true
}

func pdfText(data []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		rows, err := p.GetTextByRow()
		if err != nil {
			continue
		}
		if r.NumPage() > 1 {
			fmt.Fprintf(&b, "\n--- 第 %d 页 ---\n", i)
		}
		for _, row := range rows {
			for j, w := range row.Content {
				if j > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(w.S)
			}
			b.WriteByte('\n')
		}
		if b.Len() > MaxTextRunes*4 {
			break
		}
	}
	return strings.TrimSpace(b.String()), nil
}

var (
	slideRe = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)
	sheetRe = regexp.MustCompile(`^xl/worksheets/sheet(\d+)\.xml$`)
)

func docxParts(zr *zip.Reader) []*zip.File {
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			return []*zip.File{f}
		}
	}
	return nil
}

func pptxParts(zr *zip.Reader) []*zip.File {
	return numbered(zr, slideRe)
}

func numbered(zr *zip.Reader, re *regexp.Regexp) []*zip.File {
	type nf struct {
		n int
		f *zip.File
	}
	var xs []nf
	for _, f := range zr.File {
		if m := re.FindStringSubmatch(f.Name); m != nil {
			n, _ := strconv.Atoi(m[1])
			xs = append(xs, nf{n, f})
		}
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i].n < xs[j].n })
	out := make([]*zip.File, len(xs))
	for i, x := range xs {
		out[i] = x.f
	}
	return out
}

func readPart(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > maxXMLBytes {
		return nil, fmt.Errorf("%s is too large", f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxXMLBytes))
}

// officeText walks Office XML parts, writing <t> text and breaking lines
// at paragraph elements (local name para). Tabs and breaks are kept.
func officeText(data []byte, parts func(*zip.Reader) []*zip.File, para string, header func(int) string) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("not an Office file: %w", err)
	}
	fs := parts(zr)
	if len(fs) == 0 {
		return "", fmt.Errorf("no document body found")
	}
	var b strings.Builder
	for i, f := range fs {
		x, err := readPart(f)
		if err != nil {
			return "", err
		}
		if header != nil {
			if i > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(header(i))
		}
		d := xml.NewDecoder(bytes.NewReader(x))
		inText := false
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "t":
					inText = true
				case "tab":
					b.WriteByte('\t')
				case "br", "cr":
					b.WriteByte('\n')
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "t":
					inText = false
				case para:
					b.WriteByte('\n')
				}
			case xml.CharData:
				if inText {
					b.Write(t)
				}
			}
		}
		if b.Len() > MaxTextRunes*4 {
			break
		}
	}
	return strings.TrimSpace(collapseBlank(b.String())), nil
}

var blankRe = regexp.MustCompile(`\n{3,}`)

func collapseBlank(s string) string { return blankRe.ReplaceAllString(s, "\n\n") }

// xlsxText renders each sheet as tab-separated rows.
func xlsxText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("not an Office file: %w", err)
	}
	var shared []string
	names := map[int]string{}
	for _, f := range zr.File {
		switch f.Name {
		case "xl/sharedStrings.xml":
			x, err := readPart(f)
			if err != nil {
				return "", err
			}
			shared = sharedStrings(x)
		case "xl/workbook.xml":
			x, err := readPart(f)
			if err == nil {
				var wb struct {
					Sheets []struct {
						Name string `xml:"name,attr"`
					} `xml:"sheets>sheet"`
				}
				if xml.Unmarshal(x, &wb) == nil {
					for i, s := range wb.Sheets {
						names[i+1] = s.Name
					}
				}
			}
		}
	}
	sheets := numbered(zr, sheetRe)
	if len(sheets) == 0 {
		return "", fmt.Errorf("no sheets found")
	}
	var b strings.Builder
	for i, f := range sheets {
		x, err := readPart(f)
		if err != nil {
			return "", err
		}
		n, _ := strconv.Atoi(sheetRe.FindStringSubmatch(f.Name)[1])
		title := names[n]
		if title == "" {
			title = path.Base(f.Name)
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "## %s\n", title)
		var ws struct {
			Rows []struct {
				Cells []struct {
					Ref    string `xml:"r,attr"`
					Type   string `xml:"t,attr"`
					Value  string `xml:"v"`
					Inline string `xml:"is>t"`
				} `xml:"c"`
			} `xml:"sheetData>row"`
		}
		if err := xml.Unmarshal(x, &ws); err != nil {
			return "", err
		}
		for _, r := range ws.Rows {
			col := 0
			var cells []string
			for _, c := range r.Cells {
				if k := colIndex(c.Ref); k > col {
					for ; col < k; col++ {
						cells = append(cells, "")
					}
				}
				v := c.Value
				switch c.Type {
				case "s":
					if k, err := strconv.Atoi(v); err == nil && k >= 0 && k < len(shared) {
						v = shared[k]
					}
				case "inlineStr":
					v = c.Inline
				}
				cells = append(cells, strings.ReplaceAll(v, "\t", " "))
				col++
			}
			b.WriteString(strings.TrimRight(strings.Join(cells, "\t"), "\t"))
			b.WriteByte('\n')
			if b.Len() > MaxTextRunes*4 {
				break
			}
		}
	}
	return strings.TrimSpace(b.String()), nil
}

func sharedStrings(x []byte) []string {
	var out []string
	d := xml.NewDecoder(bytes.NewReader(x))
	var cur strings.Builder
	in, inT := false, false
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "si" {
				in = true
				cur.Reset()
			} else if t.Name.Local == "t" && in {
				inT = true
			}
		case xml.EndElement:
			if t.Name.Local == "si" {
				in = false
				out = append(out, cur.String())
			} else if t.Name.Local == "t" {
				inT = false
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		}
	}
	return out
}

// colIndex turns a cell ref like "C7" into a 0-based column.
func colIndex(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A'+1)
	}
	return n - 1
}

// Garbled reports whether extracted text is mostly junk: control
// characters, private-use glyphs and stray Latin-1 characters, which is
// what PDF fonts without a usable character map come out as (subset CJK
// fonts often). Such text is better replaced by OCR than shown or indexed.
// Real text has no control characters, so accented Latin alone (French,
// German) never counts.
func Garbled(s string) bool {
	total, bad, odd := 0, 0, 0
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || r == ' ':
			continue
		case r == utf8.RuneError, unicode.IsControl(r), r >= 0xE000 && r <= 0xF8FF:
			bad++
		case r >= 0x80 && r <= 0xFF:
			odd++
		}
		total++
	}
	if total == 0 || bad == 0 {
		return false
	}
	return bad*10 > total || (bad+odd)*5 > total
}
