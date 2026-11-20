package library

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestPassagesOf(t *testing.T) {
	text := "# 季度报告\n\n营收增长 12%。\n\n## 风险\n\n供应链延期可能影响交付。\n\n--- 第 2 页 ---\n## 附录\n\n术语表。"
	ps := passagesOf(text)
	r := []rune(text)
	byHead := map[string]chunk{}
	for _, p := range ps {
		byHead[p.heading] = p
		// the offset points at the passage in the text
		if !strings.HasPrefix(string(r[p.start:]), strings.SplitN(p.body, "\n", 2)[0]) {
			t.Fatalf("offset %d of %q is off: %q", p.start, p.heading, string(r[p.start:min(len(r), p.start+20)]))
		}
	}
	if _, ok := byHead["风险"]; !ok {
		t.Fatalf("no 风险 passage: %+v", ps)
	}
	if _, ok := byHead["第 2 页 · 附录"]; !ok {
		t.Fatalf("no page heading: %+v", ps)
	}
	// a long text without headings is cut into passages near passageChars
	long := strings.Repeat("这是一段很长的文字。", 200) // 2000 runes, no breaks
	if n := len(passagesOf(long)); n < 2 {
		t.Fatalf("one passage for %d runes", len([]rune(long)))
	}
}

func TestSearchPassageAndMany(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	doc := "# 用户研究\n\n这一节讲背景。\n\n" + strings.Repeat("填充内容。", 150) + "\n\n## 流失原因\n\n用户离开主要因为价格太高。\n"
	a, _ := s.Add(ctx, AddRequest{Name: "研究.md", Data: []byte(doc), Source: SourceUpload})
	b, _ := s.Add(ctx, AddRequest{Name: "churn.md", Data: []byte("# Churn\n\nWhy customers leave: price.\n"), Source: SourceUpload})
	indexAll(s)

	hits, err := s.Search(ctx, Viewer{Team: "default"}, "价格太高", 5)
	if err != nil || len(hits) != 1 || hits[0].ID != a.ID || hits[0].Passage == nil {
		t.Fatalf("%+v %v", hits, err)
	}
	p := hits[0].Passage
	if p.Heading != "流失原因" || !strings.Contains(hits[0].Snippet, "价格") {
		t.Fatalf("passage %+v snippet %q", p, hits[0].Snippet)
	}
	// from: the reading starts at that passage
	if body := []rune(s.Body(a.ID)); !strings.Contains(string(body[p.Start:]), "价格太高") || strings.Contains(string(body[p.Start:]), "这一节讲背景") {
		t.Fatalf("from %d does not start at the passage", p.Start)
	}
	// several phrasings: both documents, the Chinese one found by two of them first
	hits, err = s.SearchMany(ctx, Viewer{Team: "default"}, []string{"流失", "churn", "用户离开"}, 5)
	if err != nil || len(hits) != 2 || hits[0].ID != a.ID || hits[1].ID != b.ID {
		t.Fatalf("many %+v %v", hits, err)
	}
}

func TestSummariesMakeItFindable(t *testing.T) {
	s := open(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.summarize = func(_ context.Context, name, text string) (string, []string, error) {
		return "一份关于客户为什么不再续费的分析。", []string{"续费率", "retention"}, nil
	}
	go s.summaryWorker(ctx)
	it, _ := s.Add(ctx, AddRequest{Name: "q3.md", Data: []byte("# Q3\n\n很多人没有继续订阅。\n"), Source: SourceUpload})
	indexAll(s)
	if hits, _ := s.Search(ctx, Viewer{Team: "default"}, "retention", 5); len(hits) != 0 {
		t.Fatal("found before summaries are on")
	}
	if err := s.SetSettings(Settings{Summaries: true}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if hits, _ := s.Search(ctx, Viewer{Team: "default"}, "retention", 5); len(hits) == 1 {
			got, _ := s.Get(Viewer{}, it.ID)
			if got.Summary == "" || len(got.Keywords) != 2 {
				t.Fatalf("item %+v", got)
			}
			// the text read is still the document's, not the summary
			if strings.Contains(s.Body(it.ID), "续费") {
				t.Fatal("summary in the body")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("not findable by a keyword of its summary")
}

func TestMigratePassages(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	it, _ := s.Add(ctx, AddRequest{Name: "a.md", Data: []byte("# A\n\n## 第二节\n\n内容在这里。\n"), Source: SourceUpload})
	indexAll(s)
	// a library from before passages: no passages, no marker
	s.mu.Lock()
	s.unindexPassages(it.ID)
	_, _ = s.db.Exec(`DELETE FROM meta`)
	s.mu.Unlock()
	if err := s.migratePassages(); err != nil {
		t.Fatal(err)
	}
	var st string
	_ = s.db.QueryRow(`SELECT text_state FROM items WHERE id = ?`, it.ID).Scan(&st)
	if st != "pending" {
		t.Fatalf("state %q", st)
	}
	indexAll(s)
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM passages WHERE item = ?`, it.ID).Scan(&n); err != nil && err != sql.ErrNoRows || n == 0 {
		t.Fatalf("passages %d %v", n, err)
	}
}

func TestParseSummary(t *testing.T) {
	sum, kw, err := parseSummary("```json\n{\"summary\": \"讲预算。\", \"keywords\": [\"预算\", \"budget\"]}\n```")
	if err != nil || sum != "讲预算。" || len(kw) != 2 {
		t.Fatalf("%q %v %v", sum, kw, err)
	}
	if _, _, err := parseSummary("no json"); err == nil {
		t.Fatal("parsed prose")
	}
}
