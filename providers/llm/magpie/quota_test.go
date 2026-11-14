package magpie_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/magpie"
)

// quotasJSON follows the shape of magpie's GET /v1/magpie/quotas.
const quotasJSON = `{"object":"list","data":[
 {"provider":"cursor","name":"Cursor","kind":"subscription","plan":"Team","windows":[
   {"name":"Cursor Models","used":0,"remaining":100,"resetsAt":"2026-12-10T07:15:34Z"},
   {"name":"On-demand","used":0,"remaining":100,"display":"$0.00","text":true}]},
 {"provider":"codex","name":"Codex","kind":"subscription","plan":"prolite","windows":[
   {"name":"7 days","used":14,"remaining":86,"resetsAt":"2026-11-18T16:36:44+08:00","holds":{"tokens":97700}},
   {"name":"Image generation","used":0,"remaining":100,"display":"Unknown","text":true,"lane":"images"}]},
 {"provider":"codex","name":"Codex","kind":"subscription","plan":"pro","windows":[
   {"name":"7 days","used":100,"remaining":0,"resetsAt":"2026-11-18T16:30:47+08:00"}]},
 {"provider":"claude","name":"Claude","kind":"subscription","windows":[
   {"name":"5h","used":75,"remaining":25,"resetsAt":"2026-11-14T20:00:00.5Z"},
   {"name":"week","used":40,"remaining":60}]},
 {"provider":"deepseek","name":"DeepSeek","kind":"balance","windows":[],"balance":"¥121.86"}]}`

func TestQuotas(t *testing.T) {
	var hits atomic.Int32
	var fail atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"name":"magpie","version":"1"}`) })
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"codex/gpt-5","owned_by":"codex"},{"id":"claude/opus"},{"id":"deepseek/chat","owned_by":"deepseek"},{"id":"auto","owned_by":"group"}]}`)
	})
	mux.HandleFunc("/v1/magpie/quotas", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+magpie.AppKey || fail.Load() {
			http.Error(w, "no", 500)
			return
		}
		fmt.Fprint(w, quotasJSON)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	h := kernel.NewHost(nil)
	defer h.Close()
	h.Load(llm.Plugin, kernel.LoadOptions{})
	if _, err := h.Load(magpie.Plugin, kernel.LoadOptions{Config: map[string]any{"baseURL": srv.URL + "/v1", "rescanInterval": "0"}}); err != nil {
		t.Fatal(err)
	}
	ad, ok := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey).Get("magpie")
	if !ok {
		t.Fatal("not registered")
	}
	qr, ok := ad.(llm.QuotaReporter)
	if !ok {
		t.Fatal("magpie adapter is not a QuotaReporter")
	}
	q := qr.Quota(context.Background())
	var labels []string
	for _, w := range q.Windows {
		labels = append(labels, fmt.Sprintf("%s=%.2f", w.Label, w.Remaining))
	}
	// cursor has no model in the catalog; text-only and image lanes are skipped
	want := "Claude · 5h=0.25,Claude · week=0.60,Codex (prolite) · 7 days=0.86,Codex (pro) · 7 days=0.00"
	if got := strings.Join(labels, ","); got != want {
		t.Fatalf("windows:\n got %s\nwant %s", got, want)
	}
	// headline: the best subscription's tightest window
	if !q.Known || q.Remaining != 0.86 || q.ResetsAt.IsZero() {
		t.Fatalf("%+v", q)
	}
	if !q.Windows[0].ResetsAt.Equal(time.Date(2026, 11, 14, 20, 0, 0, 5e8, time.UTC)) {
		t.Fatal(q.Windows[0].ResetsAt)
	}

	// per model
	mq := ad.(*magpie.Adapter).QuotaFor(context.Background(), "claude/opus")
	if !mq.Known || len(mq.Windows) != 2 || mq.Remaining != 0.25 {
		t.Fatalf("%+v", mq)
	}
	if mq := ad.(*magpie.Adapter).QuotaFor(context.Background(), "auto"); mq.Known {
		t.Fatalf("routing group: %+v", mq)
	}
	if mq := ad.(*magpie.Adapter).QuotaFor(context.Background(), "deepseek/chat"); mq.Known {
		t.Fatalf("balance account: %+v", mq)
	}

	// cached: no further reads; a failing gateway keeps the last data
	fail.Store(true)
	if q2 := qr.Quota(context.Background()); len(q2.Windows) != 4 || hits.Load() != 1 {
		t.Fatalf("hits=%d %+v", hits.Load(), q2)
	}
}

func TestQuotasUnavailable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"name":"magpie","version":"1"}`) })
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"codex/gpt-5"}]}`) })
	srv := httptest.NewServer(mux) // older magpie: no quotas endpoint
	defer srv.Close()
	h := kernel.NewHost(nil)
	defer h.Close()
	h.Load(llm.Plugin, kernel.LoadOptions{})
	h.Load(magpie.Plugin, kernel.LoadOptions{Config: map[string]any{"baseURL": srv.URL, "rescanInterval": "0"}})
	ad, ok := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey).Get("magpie")
	if !ok {
		t.Fatal("not registered")
	}
	if q := ad.(llm.QuotaReporter).Quota(context.Background()); q.Known {
		t.Fatalf("%+v", q)
	}
}
