//go:build live

package live

// DeepSeek tool APIs against the real service (a few requests, well under
// ¥0.1). Runs only with DEEPSEEK_API_KEY set:
//
//	DEEPSEEK_API_KEY=… go test -tags live ./test/live -run DeepSeek -v

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/features/deepseek"
	"github.com/hedeqiang/crewbase/providers/llm/openai"
)

func dsClient(t *testing.T) *deepseek.Client {
	key := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY not set")
	}
	return &deepseek.Client{Key: func(context.Context) (string, error) { return key, nil }}
}

func TestDeepSeekSearchFetch(t *testing.T) {
	c := dsClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := c.Search(ctx, deepseek.SearchRequest{Query: "Go 编程语言 site:go.dev", Count: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("no results")
	}
	for _, r := range res {
		t.Logf("search: %.2f %s %s", r.RerankScore, r.URL, r.Title)
	}
	p, err := c.Fetch(ctx, "https://go.dev/")
	if err != nil {
		t.Fatal(err)
	}
	if p.Content == "" {
		t.Fatal("empty page")
	}
	t.Logf("fetch: %q, %d chars", p.Title, len([]rune(p.Content)))
}

// TTS → ASR round trip, and OCR of a rendered image.
func TestDeepSeekSpeechAndOCR(t *testing.T) {
	c := dsClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const said = "今天下午三点开会。"
	wav, err := c.Speak(ctx, said, "stella")
	if err != nil {
		t.Fatal(err)
	}
	if len(wav) < 1000 || string(wav[:4]) != "RIFF" {
		t.Fatalf("tts: %d bytes", len(wav))
	}
	t.Logf("tts: %d bytes", len(wav))
	tr, err := c.Transcribe(ctx, "speech.wav", wav)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("asr: %q (%d ms)", tr.Text, tr.DurationMS)
	if !strings.Contains(tr.Text, "开会") {
		t.Fatalf("round trip lost the words: %q", tr.Text)
	}

	r, err := c.OCR(ctx, "digits.png", digitsPNG(), deepseek.PurposeText)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ocr: %d pages, %q", r.PageCount, r.Text())
	if r.PageCount != 1 {
		t.Fatalf("pages: %d", r.PageCount)
	}
}

// digitsPNG draws "1 7" as thick bars (no font needed): a block "1" and a
// "7" — enough for OCR to return something non-empty or empty without error.
func digitsPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.White)
		}
	}
	bar := func(x0, y0, x1, y1 int) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				img.Set(x, y, color.Black)
			}
		}
	}
	bar(40, 15, 52, 85)   // 1
	bar(110, 15, 160, 27) // 7 top
	for i := 0; i < 58; i++ {
		bar(148-i/2, 27+i, 160-i/2, 28+i)
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// Direct DeepSeek API: a PDF goes up the Files API once and the model
// answers from it (deepseek-flash, one short reply).
func TestDeepSeekFilesAPI(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY not set")
	}
	a, err := openai.New(nil, openai.ProviderConfig{ID: "deepseek", Preset: "deepseek", APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	pdf := livePDF("Invoice 4471. Total due: 812 EUR.")
	at := llm.Attachment{Path: "uploads/d/invoice.pdf", Name: "invoice.pdf", Mime: "application/pdf", SHA256: "live-" + time.Now().Format("150405"), Size: int64(len(pdf)), Data: pdf,
		Text: "[附件 invoice.pdf]", Alt: "[附件 invoice.pdf，无法读取]"}
	req := llm.Request{Model: "deepseek-flash", Workspace: ws, MaxTokens: 200, Messages: []llm.Message{{Role: llm.RoleUser, Parts: []llm.Part{
		{Type: "text", Text: "What is the total due in the attached invoice? Answer with the number and currency only."},
		{Type: llm.PartAttachment, Attachment: &at},
	}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out strings.Builder
	if _, err := a.Stream(ctx, req, func(ch llm.Chunk) error {
		if ch.Type == "text" {
			out.WriteString(ch.Text)
		}
		return nil
	}); err != nil {
		t.Fatal(strings.ReplaceAll(err.Error(), key, "***"))
	}
	t.Logf("reply: %q", out.String())
	cache, _ := os.ReadFile(ws + "/uploads/.remote/deepseek.json")
	if !strings.Contains(string(cache), "file") {
		t.Fatalf("no file id cached (upload not used?): %s", cache)
	}
	if !strings.Contains(out.String(), "812") {
		t.Fatalf("answer does not come from the file: %q", out.String())
	}
}

func livePDF(text string) []byte {
	stream := fmt.Sprintf("BT /F1 18 Tf 72 700 Td (%s) Tj ET", text)
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
