package media_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/media"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/rules"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
	"github.com/hedeqiang/crewbase/providers/llm/openai"
)

var pngBytes, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

// a minimal MP4 header (ftyp box) that content sniffing recognises
var mp4Bytes = append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), make([]byte, 64)...)

// fakeOpenAI serves the Images and Videos APIs.
type fakeOpenAI struct {
	mu        sync.Mutex
	imageBody map[string]any
	editFiles int
	videoBody map[string]any
	polls     int
	content   []byte
}

func (f *fakeOpenAI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		img := base64.StdEncoding.EncodeToString(pngBytes)
		switch {
		case r.URL.Path == "/images/generations":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &f.imageBody)
			n, _ := f.imageBody["n"].(float64)
			var data []string
			for range int(n) {
				data = append(data, fmt.Sprintf(`{"b64_json":%q}`, img))
			}
			fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(data, ","))
		case r.URL.Path == "/images/edits":
			_ = r.ParseMultipartForm(10 << 20)
			f.editFiles = len(r.MultipartForm.File["image[]"])
			fmt.Fprintf(w, `{"data":[{"b64_json":%q}]}`, img)
		case r.Method == "POST" && r.URL.Path == "/videos":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &f.videoBody)
			fmt.Fprint(w, `{"id":"video_abc","status":"queued","progress":0}`)
		case r.URL.Path == "/videos/video_abc":
			f.polls++
			if f.polls < 2 {
				fmt.Fprint(w, `{"id":"video_abc","status":"in_progress","progress":50}`)
				return
			}
			fmt.Fprint(w, `{"id":"video_abc","status":"completed","progress":100}`)
		case r.URL.Path == "/videos/video_abc/content":
			w.Header().Set("Content-Type", "video/mp4")
			w.Write(f.content)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
}

type env struct {
	h     *kernel.Host
	reg   *tools.Registry
	svc   *media.Service
	a     *agent.Agent
	ss    *session.Session
	fake  *fakeOpenAI
	llms  *llm.Registry
	route *router.Router
}

func setup(t *testing.T, models []string, extra ...kernel.Spec) *env {
	t.Helper()
	f := &fakeOpenAI{content: mp4Bytes}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	specs := []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": root + "/sessions"}}, {prompt.Plugin, nil}, {tools.Plugin, nil},
		{approval.Plugin, nil}, {llm.Plugin, nil},
		{mock.Plugin, map[string]any{"ids": []string{"mock"}}},
		{openai.Plugin, map[string]any{"providers": []any{map[string]any{"id": "openai", "baseURL": srv.URL, "apiKey": "k", "models": models}}}},
		{router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil},
	}
	for _, x := range specs {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range extra {
		if _, err := h.Load(s, kernel.LoadOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Load(media.Plugin, kernel.LoadOptions{Config: map[string]any{"syncInterval": "0"}}); err != nil {
		t.Fatal(err)
	}
	llms := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey)
	ad, _ := llms.Get("openai")
	ad.(*openai.Adapter).SetPollInterval(time.Millisecond)
	svc := kernel.Use[*media.Service](h.Root(), media.ServiceKey)
	svc.Sync(context.Background())
	a, err := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey).Create(agent.Spec{Name: "Painter"})
	if err != nil {
		t.Fatal(err)
	}
	ss, _ := a.Session()
	rt, _ := kernel.Use[agentloop.Router](h.Root(), agentloop.RouterKey).(*router.Router)
	return &env{h: h, reg: kernel.Use[*tools.Registry](h.Root(), tools.ServiceKey), svc: svc, a: a, ss: ss, fake: f, llms: llms, route: rt}
}

func (e *env) run(name string, args any) tools.Execution {
	b, _ := json.Marshal(args)
	return e.reg.Run(context.Background(), e.h.Root(), tools.Call{ID: session.NewID("call"), Name: name, Args: b, Agent: e.a.ID(), Session: e.ss, Mode: "sandbox", Workspace: e.a.Workspace()})
}

func (e *env) events(typ string) []session.Event {
	var out []session.Event
	for _, ev := range e.ss.Events() {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func TestToolsFollowAvailableModels(t *testing.T) {
	e := setup(t, []string{"gpt-6-luna"})
	for _, n := range []string{"image_generate", "image_edit", "video_generate"} {
		if _, ok := e.reg.Get(e.a.ID(), n); ok {
			t.Fatalf("%s registered without a model for it", n)
		}
	}
	e2 := setup(t, []string{"gpt-6-luna", "dall-e-3"})
	if _, ok := e2.reg.Get(e2.a.ID(), "image_generate"); !ok {
		t.Fatal("image_generate missing")
	}
	if _, ok := e2.reg.Get(e2.a.ID(), "image_edit"); ok {
		t.Fatal("dall-e-3 cannot edit: image_edit must stay hidden")
	}
	if _, ok := e2.reg.Get(e2.a.ID(), "video_generate"); ok {
		t.Fatal("video_generate without a video model")
	}
}

func TestImageGenerateSavesFilesAndRecords(t *testing.T) {
	e := setup(t, []string{"gpt-6-luna", "gpt-image-1-mini"})
	tool, ok := e.reg.Get(e.a.ID(), "image_generate")
	if !ok {
		t.Fatal("image_generate missing")
	}
	if !strings.Contains(tool.Description+fmt.Sprint(tool.Params), "openai/gpt-image-1-mini") {
		t.Fatal("the model list should name the available model")
	}
	ex := e.run("image_generate", map[string]any{"prompt": "A red fox, watercolor", "size": "1024x1024", "quality": "low", "n": 2})
	if ex.Result.IsError || ex.Category != tools.CategoryWrite {
		t.Fatalf("%s %+v", ex.Category, ex.Result)
	}
	r := ex.Result.Content
	if !strings.Contains(r, "generated/") || strings.Contains(r, base64.StdEncoding.EncodeToString(pngBytes)[:20]) {
		t.Fatalf("result must carry paths, never the bytes: %s", r)
	}
	evs := e.events(media.TypeGenerated)
	if len(evs) != 2 {
		t.Fatalf("%d media events", len(evs))
	}
	var g media.Generated
	_ = evs[1].Decode(&g)
	if g.Kind != "image" || g.Model != "openai/gpt-image-1-mini" || g.Mime != "image/png" || !strings.HasPrefix(g.Path, "generated/") || !strings.Contains(g.Path, "a-red-fox-watercolor") {
		t.Fatalf("%+v", g)
	}
	b, err := os.ReadFile(filepath.Join(e.a.Workspace(), filepath.FromSlash(g.Path)))
	if err != nil || string(b) != string(pngBytes) {
		t.Fatalf("saved file: %v", err)
	}
	e.fake.mu.Lock()
	body := e.fake.imageBody
	e.fake.mu.Unlock()
	if body["model"] != "gpt-image-1-mini" || body["quality"] != "low" || body["size"] != "1024x1024" {
		t.Fatalf("request %v", body)
	}
	// summary for the step / approval line
	_, sum := tool.Classify(tools.Call{Args: json.RawMessage(`{"prompt":"一只猫在窗台上晒太阳","n":2,"size":"1536x1024"}`)})
	if sum != "生成图片：一只猫在窗台上晒太阳（2 张，1536x1024）" {
		t.Fatal(sum)
	}
}

func TestImageEditReadsWorkspaceImagesOnly(t *testing.T) {
	e := setup(t, []string{"gpt-image-2"})
	ws := e.a.Workspace()
	os.MkdirAll(filepath.Join(ws, "uploads"), 0o755)
	os.WriteFile(filepath.Join(ws, "uploads", "cat.png"), pngBytes, 0o644)
	os.WriteFile(filepath.Join(ws, "notes.html"), []byte("<html><script>x</script></html>"), 0o644)
	ex := e.run("image_edit", map[string]any{"images": []string{"uploads/cat.png"}, "prompt": "add a hat"})
	if ex.Result.IsError {
		t.Fatal(ex.Result.Content)
	}
	e.fake.mu.Lock()
	n := e.fake.editFiles
	e.fake.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d files sent", n)
	}
	outside := filepath.Join(t.TempDir(), "secret.png")
	os.WriteFile(outside, pngBytes, 0o644)
	os.Symlink(outside, filepath.Join(ws, "link.png"))
	for _, bad := range []string{"../secret.png", outside, "link.png", "notes.html", "missing.png"} {
		ex := e.run("image_edit", map[string]any{"images": []string{bad}, "prompt": "x"})
		if !ex.Result.IsError {
			t.Errorf("%s was accepted", bad)
		}
	}
}

func TestVideoNeedsApprovalAndRunsInBackground(t *testing.T) {
	// with the rules engine, video generation is Ask: nobody answers here,
	// so it is not run.
	e := setup(t, []string{"gpt-6-luna", "sora-2"}, rules.Plugin)
	ex := e.run("video_generate", map[string]any{"prompt": "a cat", "seconds": 4})
	if ex.Category != tools.CategorySpend || !ex.Result.IsError || ex.Outcome == approval.OutcomeAllowedOnce {
		t.Fatalf("%s %s %+v", ex.Category, ex.Outcome, ex.Result)
	}
	if asked := e.events(session.TypeApprovalAsked); len(asked) != 1 || !strings.Contains(string(asked[0].Data), "生成视频：a cat（4 秒）") {
		t.Fatalf("approval card: %v", asked)
	}
	e.fake.mu.Lock()
	created := e.fake.videoBody
	e.fake.mu.Unlock()
	if created != nil {
		t.Fatal("the provider was called without approval")
	}

	// approved (tool body run directly): returns at once, reports back
	e = setup(t, []string{"gpt-6-luna", "sora-2"})
	e.a.Pause() // keep the report in the queue
	tool, _ := e.reg.Get(e.a.ID(), "video_generate")
	res, _ := tool.Execute(context.Background(), tools.Call{ID: "c1", Name: "video_generate", Agent: e.a.ID(), Session: e.ss, Workspace: e.a.Workspace(),
		Args: json.RawMessage(`{"prompt":"a cat on a skateboard","seconds":4,"aspect":"9:16","resolution":"720p"}`)})
	if res.IsError || !strings.Contains(res.Content, "Started vid1") {
		t.Fatal(res.Content)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(e.a.Queue()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	q := e.a.Queue()
	if len(q) != 1 || q[0].Tag != "job:vid1" || q[0].Session != e.ss.ID() || !strings.Contains(q[0].Text, "generated/") || !strings.Contains(q[0].Text, ".mp4") {
		t.Fatalf("report %+v", q)
	}
	gens := e.events(media.TypeGenerated)
	jobs := e.events(media.TypeVideoJob)
	if len(gens) != 1 || len(jobs) != 2 {
		t.Fatalf("events: %d generated, %d job", len(gens), len(jobs))
	}
	var g media.Generated
	_ = gens[0].Decode(&g)
	if g.Kind != "video" || g.Mime != "video/mp4" || g.Job != "vid1" {
		t.Fatalf("%+v", g)
	}
	e.fake.mu.Lock()
	created = e.fake.videoBody
	e.fake.mu.Unlock()
	if created["size"] != "720x1280" || created["seconds"] != "4" {
		t.Fatalf("create %v", created)
	}
	if st := e.run("video_status", map[string]any{}).Result.Content; !strings.Contains(st, "vid1  done") {
		t.Fatal(st)
	}
}

func TestVideoThatIsNotAVideoFails(t *testing.T) {
	e := setup(t, []string{"sora-2"})
	e.fake.content = []byte("<!doctype html><script>alert(1)</script>")
	e.a.Pause()
	tool, _ := e.reg.Get(e.a.ID(), "video_generate")
	res, _ := tool.Execute(context.Background(), tools.Call{ID: "c1", Agent: e.a.ID(), Session: e.ss, Workspace: e.a.Workspace(), Args: json.RawMessage(`{"prompt":"x"}`)})
	if res.IsError {
		t.Fatal(res.Content)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(e.a.Queue()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if q := e.a.Queue(); len(q) != 1 || !strings.Contains(q[0].Text, "失败") || !strings.Contains(q[0].Text, "text/html") {
		t.Fatalf("%+v", q)
	}
	entries, _ := os.ReadDir(filepath.Join(e.a.Workspace(), "generated"))
	if len(entries) != 0 {
		t.Fatalf("left files: %v", entries)
	}
}

func TestPickModel(t *testing.T) {
	e := setup(t, []string{"gpt-6-luna", "gpt-image-1-mini", "gpt-image-2"})
	ctx := context.Background()
	if ref, err := e.svc.Pick(ctx, e.a, "image", "", false); err != nil || ref.Model != "gpt-image-1-mini" {
		t.Fatalf("first available: %v %v", ref, err)
	}
	e.route.SetRoutes([]router.Route{{Task: router.TaskImage, Model: "openai/gpt-image-2"}})
	if ref, err := e.svc.Pick(ctx, e.a, "image", "", false); err != nil || ref.Model != "gpt-image-2" {
		t.Fatalf("routed: %v %v", ref, err)
	}
	if _, err := e.svc.Pick(ctx, e.a, "image", "openai/gpt-6-luna", false); err == nil || !strings.Contains(err.Error(), "not an available image model") {
		t.Fatalf("a chat model must not draw: %v", err)
	}
	if _, err := e.svc.Pick(ctx, e.a, "video", "", false); err == nil || !strings.Contains(err.Error(), "no video model") {
		t.Fatalf("no video model: %v", err)
	}
	// a local-only agent is never sent to a cloud image model
	mk, _ := e.llms.Get("mock")
	mk.(*mock.Adapter).SetKind("local")
	local, _ := kernel.Use[*agent.Registry](e.h.Root(), agent.ServiceKey).Create(agent.Spec{Name: "Private", Models: []llm.ModelRef{{Provider: "mock", Model: "default"}}})
	if _, err := e.svc.Pick(ctx, local, "image", "", false); err == nil {
		t.Fatal("local-only agent routed to a cloud model")
	}
}

func TestSave(t *testing.T) {
	ws := t.TempDir()
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.Local)
	a, err := media.Save(ws, "image", "A cat / on ../../etc", strings.NewReader(string(pngBytes)), media.MaxImageBytes, now)
	if err != nil || a.Path != "generated/20261116-100000-a-cat-on-etc.png" || a.Mime != "image/png" {
		t.Fatalf("%+v %v", a, err)
	}
	b, _ := media.Save(ws, "image", "A cat / on ../../etc", strings.NewReader(string(pngBytes)), media.MaxImageBytes, now)
	if b.Path != "generated/20261116-100000-a-cat-on-etc-2.png" {
		t.Fatalf("no overwrite: %+v", b)
	}
	if c, _ := media.Save(ws, "image", "一只猫", strings.NewReader(string(pngBytes)), media.MaxImageBytes, now); c.Path != "generated/20261116-100000-image.png" {
		t.Fatalf("CJK prompt: %+v", c)
	}
	for _, bad := range []string{"<svg xmlns='http://www.w3.org/2000/svg'></svg>", "<html><body>x</body></html>", ""} {
		if _, err := media.Save(ws, "image", "x", strings.NewReader(bad), media.MaxImageBytes, now); err == nil {
			t.Errorf("saved %q as an image", bad)
		}
	}
	if _, err := media.Save(ws, "video", "x", strings.NewReader(string(pngBytes)), media.MaxVideoBytes, now); err == nil {
		t.Error("a PNG saved as a video")
	}
	if _, err := media.Save(ws, "image", "big", strings.NewReader(string(pngBytes)+strings.Repeat("x", 100)), 80, now); err == nil {
		t.Error("size cap ignored")
	}
	entries, _ := os.ReadDir(filepath.Join(ws, "generated"))
	if len(entries) != 3 {
		t.Fatalf("temp files left behind: %v", entries)
	}
	// generated/ pointing outside the workspace is refused
	ws2 := t.TempDir()
	os.Symlink(t.TempDir(), filepath.Join(ws2, "generated"))
	if _, err := media.Save(ws2, "image", "x", strings.NewReader(string(pngBytes)), media.MaxImageBytes, now); err == nil {
		t.Error("followed a symlink out of the workspace")
	}
}
