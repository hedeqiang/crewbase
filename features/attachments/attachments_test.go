package attachments

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// minimalPDF builds a one-page PDF with the given text and a correct xref.
func minimalPDF(text string) []byte {
	stream := fmt.Sprintf("BT /F1 12 Tf 72 712 Td (%s) Tj ET", text)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, x)
	return b.Bytes()
}

func TestExtractOffice(t *testing.T) {
	docx := zipOf(t, map[string]string{"word/document.xml": `<w:document xmlns:w="w"><w:body>
		<w:p><w:r><w:t>季度报告</w:t></w:r></w:p><w:p><w:r><w:t>收入</w:t><w:tab/><w:t>120</w:t></w:r></w:p></w:body></w:document>`})
	got, ok, err := Extract("r.docx", mimeDOCX, docx)
	if err != nil || !ok || got != "季度报告\n收入\t120" {
		t.Fatalf("docx = %q %v %v", got, ok, err)
	}
	pptx := zipOf(t, map[string]string{
		"ppt/slides/slide2.xml":  `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:r><a:t>第二页</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/slide10.xml": `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:r><a:t>第十页</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/slide1.xml":  `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:r><a:t>标题</a:t></a:r></a:p></p:sld>`,
	})
	got, _, _ = Extract("d.pptx", mimePPTX, pptx)
	if !strings.Contains(got, "## 幻灯片 1\n标题") || strings.Index(got, "第二页") > strings.Index(got, "第十页") {
		t.Fatalf("pptx order = %q", got)
	}
	xlsx := zipOf(t, map[string]string{
		"xl/workbook.xml":          `<workbook><sheets><sheet name="销售"/></sheets></workbook>`,
		"xl/sharedStrings.xml":     `<sst><si><t>地区</t></si><si><r><t>华</t></r><r><t>东</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row><c r="A1" t="s"><v>0</v></c><c r="C1"><v>42</v></c></row><row><c r="A2" t="s"><v>1</v></c><c r="B2" t="inlineStr"><is><t>x</t></is></c></row></sheetData></worksheet>`,
	})
	got, _, _ = Extract("s.xlsx", mimeXLSX, xlsx)
	if got != "## 销售\n地区\t\t42\n华东\tx" {
		t.Fatalf("xlsx = %q", got)
	}
	if _, ok, _ := Extract("bad.docx", mimeDOCX, []byte("not a zip")); ok {
		t.Fatal("broken docx must not extract")
	}
}

func TestExtractPDF(t *testing.T) {
	got, ok, err := Extract("a.pdf", "application/pdf", minimalPDF("Hello Crewbase"))
	if err != nil || !ok || !strings.Contains(got, "Hello Crewbase") {
		t.Fatalf("pdf = %q %v %v", got, ok, err)
	}
	if _, ok, _ := Extract("b.pdf", "application/pdf", []byte("%PDF-1.4 garbage")); ok {
		t.Fatal("garbage pdf must not extract")
	}
}

func TestMimeOf(t *testing.T) {
	p := pngBytes(t)
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"a.png", p, "image/png"},
		{"a.jpg", p, "image/png"},                                  // the bytes win for images
		{"evil.png", []byte("<html>"), "application/octet-stream"}, // not an image
		{"x.pdf", []byte("hello"), "application/octet-stream"},
		{"x.pdf", minimalPDF("x"), "application/pdf"},
		{"notes", []byte("plain words"), "text/plain"},
		{"a.svg", []byte("<svg/>"), "image/svg+xml"},
		{"r.DOCX", []byte("PK"), mimeDOCX},
	}
	for _, c := range cases {
		if got := MimeOf(c.name, c.data); got != c.want {
			t.Errorf("MimeOf(%s) = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestSaveAndResolve(t *testing.T) {
	ws := t.TempDir()
	now := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	a, err := Save(ws, "../../报告.md", []byte("# hi"), now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Path != "uploads/2026-11-15/报告.md" || a.Mime != "text/markdown" || a.Size != 4 || len(a.SHA256) != 64 {
		t.Fatalf("saved %+v", a)
	}
	same, _ := Save(ws, "报告.md", []byte("# hi"), now)
	if same.Path != a.Path {
		t.Fatalf("identical file should be reused: %s", same.Path)
	}
	other, _ := Save(ws, "报告.md", []byte("# other"), now)
	if other.Path != "uploads/2026-11-15/报告 (1).md" {
		t.Fatalf("name clash: %s", other.Path)
	}
	if _, err := Save(ws, "big.bin", make([]byte, MaxFileBytes+1), now); err == nil {
		t.Fatal("oversized upload accepted")
	}

	got, err := Resolve(ws, a.Path, "报告.md")
	if err != nil || got.SHA256 != a.SHA256 || got.Mime != "text/markdown" {
		t.Fatalf("resolve %+v %v", got, err)
	}
	os.WriteFile(filepath.Join(ws, "secret.txt"), []byte("x"), 0o644)
	for _, bad := range []string{"secret.txt", "uploads/../secret.txt", "../x", "/etc/passwd", "uploads/2026-11-15/missing"} {
		if _, err := Resolve(ws, bad, ""); err == nil {
			t.Errorf("Resolve(%q) accepted", bad)
		}
	}
	// a symlink out of the workspace is refused
	outside := filepath.Join(t.TempDir(), "o.txt")
	os.WriteFile(outside, []byte("outside"), 0o644)
	os.Symlink(outside, filepath.Join(ws, "uploads", "2026-11-15", "link.txt"))
	if _, err := Resolve(ws, "uploads/2026-11-15/link.txt", ""); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

type fakeAdapter struct {
	llm.Adapter
	accept map[string]bool
}

func (f fakeAdapter) AcceptsFile(_, mime string) bool { return f.accept[mime] }

func TestHydrate(t *testing.T) {
	ws := t.TempDir()
	now := time.Now()
	img, _ := Save(ws, "pic.png", pngBytes(t), now)
	doc, _ := Save(ws, "notes.txt", []byte("line one\nline two"), now)
	pdfA, _ := Save(ws, "a.pdf", minimalPDF("Native PDF"), now)
	gone := llm.Attachment{Path: "uploads/x/gone.txt", Name: "gone.txt", Mime: "text/plain"}
	orig := []llm.Part{{Type: "text", Text: "看看"}}
	for _, at := range []llm.Attachment{img, doc, pdfA, gone} {
		at := at
		orig = append(orig, llm.Part{Type: llm.PartAttachment, Attachment: &at})
	}
	mk := func() llm.Request {
		parts := append([]llm.Part(nil), orig...)
		return llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Parts: parts}}}
	}
	s := NewService()

	// a text-only model: image becomes a note, documents become text
	req := mk()
	s.Hydrate(context.Background(), ws, fakeAdapter{}, "m", &req)
	ps := req.Messages[0].Parts
	if ps[1].Attachment.Data != nil || !strings.Contains(ps[1].Attachment.Text, "不能查看图片") {
		t.Fatalf("image for text model: %+v", ps[1].Attachment)
	}
	if !strings.Contains(ps[2].Attachment.Text, "line two") || !strings.Contains(ps[2].Attachment.Text, doc.Path) {
		t.Fatalf("text doc: %q", ps[2].Attachment.Text)
	}
	if !strings.Contains(ps[3].Attachment.Text, "Native PDF") {
		t.Fatalf("pdf text: %q", ps[3].Attachment.Text)
	}
	if !strings.Contains(ps[4].Attachment.Text, "已不在工作区") {
		t.Fatalf("missing file: %q", ps[4].Attachment.Text)
	}
	if orig[1].Attachment.Text != "" {
		t.Fatal("hydrate wrote into the shared attachment")
	}

	// a vision + PDF model: bytes, short labels, Alt kept for fallback
	req = mk()
	s.Hydrate(context.Background(), ws, fakeAdapter{accept: map[string]bool{"image/png": true, "application/pdf": true}}, "m", &req)
	ps = req.Messages[0].Parts
	if len(ps[1].Attachment.Data) == 0 || len(ps[3].Attachment.Data) == 0 {
		t.Fatal("native files not loaded")
	}
	if strings.Contains(ps[3].Attachment.Text, "Native PDF") || !strings.Contains(ps[3].Attachment.Alt, "Native PDF") {
		t.Fatalf("native pdf label %q alt %q", ps[3].Attachment.Text, ps[3].Attachment.Alt)
	}
	if ps[2].Attachment.Data != nil {
		t.Fatal("text file should not be sent as bytes")
	}
	est := llm.EstimateRequest(req)
	if est < 1600 {
		t.Fatalf("estimate ignores attachments: %d", est)
	}
}

func TestClipLongText(t *testing.T) {
	ws := t.TempDir()
	long := strings.Repeat("字", MaxTextRunes+10)
	at, _ := Save(ws, "long.txt", []byte(long), time.Now())
	req := llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartAttachment, Attachment: &at}}}}}
	NewService().Hydrate(context.Background(), ws, fakeAdapter{}, "m", &req)
	txt := req.Messages[0].Parts[0].Attachment.Text
	if !strings.Contains(txt, "内容过长") || strings.Count(txt, "字") > MaxTextRunes+20 {
		t.Fatal("long text not clipped")
	}
}
