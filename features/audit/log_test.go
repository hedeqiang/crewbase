package audit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChainAcrossMonthsAndReopen(t *testing.T) {
	root := t.TempDir()
	l, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	l.sync = false
	now := time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if _, err := l.Append(Entry{Type: "t", Actor: "a", Data: map[string]any{"i": i, "z": "x", "a": []int{1, 2}}}); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(2 * time.Minute) // November
	if _, err := l.Append(Entry{Type: "t", Data: map[string]any{"i": 3}}); err != nil {
		t.Fatal(err)
	}
	l.Close()
	for _, f := range []string{"audit-202610.jsonl", "audit-202611.jsonl"} {
		if _, err := os.Stat(filepath.Join(root, "audit", f)); err != nil {
			t.Fatal(err)
		}
	}
	// reopen continues the chain
	l2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	seq, head := l2.Head()
	if seq != 4 || head == "" {
		t.Fatalf("head %d %q", seq, head)
	}
	l2.now = func() time.Time { return now }
	r, err := l2.Append(Entry{Type: "t2"})
	if err != nil || r.Seq != 5 || r.Prev != head {
		t.Fatalf("%+v %v", r, err)
	}
	l2.Close()
	res, err := Verify(root)
	if err != nil || !res.OK || res.Records != 5 || res.Files != 2 || res.Head != r.Hash {
		t.Fatalf("%+v %v", res, err)
	}
	// query by time
	recs, err := l2.Query(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Time{}, "", 0)
	if err != nil || len(recs) != 2 {
		t.Fatalf("%d %v", len(recs), err)
	}
	recs, _ = l2.Query(time.Time{}, time.Time{}, "t2", 0)
	if len(recs) != 1 {
		t.Fatalf("type filter: %d", len(recs))
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	mk := func(t *testing.T) (string, string) {
		root := t.TempDir()
		l, _ := Open(root)
		l.sync = false
		for i := 0; i < 4; i++ {
			l.Append(Entry{Type: "rule/decision", Data: map[string]any{"verdict": "deny", "i": i}})
		}
		l.Close()
		files, _ := listFiles(filepath.Join(root, "audit"))
		return root, filepath.Join(root, "audit", files[0])
	}
	t.Run("modified", func(t *testing.T) {
		root, f := mk(t)
		b, _ := os.ReadFile(f)
		b = bytes.Replace(b, []byte(`"i":2`), []byte(`"i":9`), 1)
		os.WriteFile(f, b, 0o600)
		res, _ := Verify(root)
		if res.OK || res.BrokenSeq != 3 || !strings.Contains(res.Error, "hash") {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("deleted", func(t *testing.T) {
		root, f := mk(t)
		b, _ := os.ReadFile(f)
		lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
		out := append(append([][]byte{}, lines[:1]...), lines[2:]...)
		os.WriteFile(f, append(bytes.Join(out, []byte("\n")), '\n'), 0o600)
		res, _ := Verify(root)
		if res.OK || res.BrokenSeq != 3 || !strings.Contains(res.Error, "gap") {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("rehashed-but-unlinked", func(t *testing.T) {
		root, f := mk(t)
		// rewrite record 2 consistently (new hash) — the next record's prev breaks
		b, _ := os.ReadFile(f)
		lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
		var r Record
		if err := json.Unmarshal(lines[1], &r); err != nil {
			t.Fatal(err)
		}
		r.Actor = "mallory"
		r.Hash, _ = ComputeHash(r)
		lines[1], _ = json.Marshal(r)
		os.WriteFile(f, append(bytes.Join(lines, []byte("\n")), '\n'), 0o600)
		res, _ := Verify(root)
		if res.OK || res.BrokenSeq != 3 || !strings.Contains(res.Error, "prev") {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("torn tail", func(t *testing.T) {
		root, f := mk(t)
		fh, _ := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0)
		fh.WriteString(`{"seq":5,"ti`)
		fh.Close()
		l, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		if seq, _ := l.Head(); seq != 4 {
			t.Fatalf("head %d", seq)
		}
		l.Close()
		res, _ := Verify(root)
		if res.OK || !strings.Contains(res.Error, "unparsable") {
			t.Fatalf("%+v", res)
		}
	})
}
