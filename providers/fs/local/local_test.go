package local_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/fs"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/fs/local"
)

type env struct {
	h       *kernel.Host
	fs      fs.FS
	ws, out string
	a       fs.Access
}

func setup(t *testing.T, cfg map[string]any) *env {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	if _, err := h.Load(local.Plugin, kernel.LoadOptions{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	e := &env{h: h, fs: kernel.Use[fs.FS](h.Root(), fs.ServiceKey), ws: filepath.Join(base, "ws"), out: filepath.Join(base, "out")}
	os.MkdirAll(e.ws, 0o755)
	os.MkdirAll(e.out, 0o755)
	e.a = fs.Access{Agent: "kai", Roots: []string{e.ws}}
	return e
}

func (e *env) p(rel string) string { return filepath.Join(e.ws, rel) }

func touchLater(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	os.Chtimes(path, later, later)
}

func TestReadWriteEdit(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	r, err := e.fs.Write(ctx, e.a, e.p("sub/a.txt"), []byte("hello world\n"))
	if err != nil || !r.Created || r.Bytes != 12 {
		t.Fatal(r, err)
	}
	f, err := e.fs.Read(ctx, e.a, e.p("sub/a.txt"))
	if err != nil || string(f.Content) != "hello world\n" {
		t.Fatal(f, err)
	}
	if _, err := e.fs.Edit(ctx, e.a, e.p("sub/a.txt"), "world", "there"); err != nil {
		t.Fatal(err)
	}
	// own edits keep the stamp fresh: a second edit needs no re-read
	if _, err := e.fs.Edit(ctx, e.a, e.p("sub/a.txt"), "there", "again"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(e.p("sub/a.txt"))
	if string(b) != "hello again\n" {
		t.Fatal(string(b))
	}
	st, err := e.fs.Stat(ctx, e.a, e.p("sub/a.txt"))
	if err != nil || st.Size != 12 || st.IsDir {
		t.Fatal(st, err)
	}
	ls, err := e.fs.List(ctx, e.a, e.ws)
	if err != nil || len(ls) != 1 || ls[0].Name != "sub" || !ls[0].IsDir {
		t.Fatal(ls, err)
	}
	if _, err := e.fs.Read(ctx, e.a, e.ws); !errors.Is(err, fs.ErrIsDir) {
		t.Fatal(err)
	}
	if _, err := e.fs.Read(ctx, e.a, e.p("nope")); !errors.Is(err, fs.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := e.fs.Read(ctx, e.a, "rel/path"); !errors.Is(err, fs.ErrNotAbs) {
		t.Fatal(err)
	}
}

func TestReadBeforeEdit(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	path := e.p("f.go")
	os.WriteFile(path, []byte("package x\nvar A = 1\nvar B = 1\n"), 0o600)

	_, err := e.fs.Edit(ctx, e.a, path, "A = 1", "A = 2")
	if !errors.Is(err, fs.ErrNotRead) || !strings.Contains(err.Error(), "read it before editing") {
		t.Fatal(err)
	}
	if _, err := e.fs.Write(ctx, e.a, path, []byte("x")); !errors.Is(err, fs.ErrNotRead) {
		t.Fatal("blind overwrite must fail", err)
	}
	e.fs.Read(ctx, e.a, path)
	// another agent's read does not count
	other := e.a
	other.Agent = "nova"
	if _, err := e.fs.Edit(ctx, other, path, "A = 1", "A = 2"); !errors.Is(err, fs.ErrNotRead) {
		t.Fatal(err)
	}
	if _, err := e.fs.Edit(ctx, e.a, path, "= 1", "= 2"); !errors.Is(err, fs.ErrNotUnique) {
		t.Fatal(err)
	}
	if _, err := e.fs.Edit(ctx, e.a, path, "C = 1", "C = 2"); !errors.Is(err, fs.ErrNoMatch) {
		t.Fatal(err)
	}
	if _, err := e.fs.Edit(ctx, e.a, path, "", "x"); err == nil {
		t.Fatal("empty old")
	}
	if _, err := e.fs.Edit(ctx, e.a, path, "A = 1", "A = 1"); err == nil {
		t.Fatal("no-op edit")
	}
	if _, err := e.fs.Edit(ctx, e.a, path, "A = 1", "A = 2"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatal("mode not preserved", st.Mode())
	}
	// changed on disk behind the agent's back → stale
	touchLater(t, path, "package x\nvar A = 9\n")
	_, err = e.fs.Edit(ctx, e.a, path, "A = 9", "A = 3")
	if !errors.Is(err, fs.ErrStale) || !strings.Contains(err.Error(), "read it again") {
		t.Fatal(err)
	}
	if _, err := e.fs.Write(ctx, e.a, path, []byte("x")); !errors.Is(err, fs.ErrStale) {
		t.Fatal(err)
	}
	e.fs.Read(ctx, e.a, path)
	if _, err := e.fs.Edit(ctx, e.a, path, "A = 9", "A = 3"); err != nil {
		t.Fatal(err)
	}
}

func TestBlindOverwriteConfig(t *testing.T) {
	e := setup(t, map[string]any{"allowBlindOverwrite": true})
	os.WriteFile(e.p("x"), []byte("old"), 0o644)
	if _, err := e.fs.Write(context.Background(), e.a, e.p("x"), []byte("new")); err != nil {
		t.Fatal(err)
	}
	// edit still requires a read
	os.WriteFile(e.p("y"), []byte("old"), 0o644)
	if _, err := e.fs.Edit(context.Background(), e.a, e.p("y"), "old", "new"); !errors.Is(err, fs.ErrNotRead) {
		t.Fatal(err)
	}
}

func TestConfinement(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	secret := filepath.Join(e.out, "secret")
	os.WriteFile(secret, []byte("s"), 0o644)

	if _, err := e.fs.Read(ctx, e.a, secret); !errors.Is(err, fs.ErrOutsideRoots) {
		t.Fatal(err)
	}
	if _, err := e.fs.Read(ctx, e.a, e.ws+"/../out/secret"); !errors.Is(err, fs.ErrOutsideRoots) {
		t.Fatal("dot-dot escape", err)
	}
	// symlink to a file outside
	os.Symlink(secret, e.p("link"))
	if _, err := e.fs.Read(ctx, e.a, e.p("link")); !errors.Is(err, fs.ErrOutsideRoots) {
		t.Fatal("symlink escape read", err)
	}
	if _, err := e.fs.Write(ctx, e.a, e.p("link"), []byte("pwn")); !errors.Is(err, fs.ErrOutsideRoots) {
		t.Fatal("symlink escape write", err)
	}
	// symlinked directory outside
	os.Symlink(e.out, e.p("dirlink"))
	if _, err := e.fs.Write(ctx, e.a, e.p("dirlink/new.txt"), []byte("pwn")); !errors.Is(err, fs.ErrOutsideRoots) {
		t.Fatal("dir symlink escape", err)
	}
	if _, err := e.fs.List(ctx, e.a, e.p("dirlink")); !errors.Is(err, fs.ErrOutsideRoots) {
		t.Fatal(err)
	}
	// deleting the link itself is fine (the target survives)
	if err := e.fs.Delete(ctx, e.a, e.p("link"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatal("delete followed the link")
	}
	// dangling link inside: a write replaces the link, never follows it
	ghost := filepath.Join(e.out, "ghost")
	os.Symlink(ghost, e.p("dangling"))
	if _, err := e.fs.Write(ctx, e.a, e.p("dangling"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ghost); err == nil {
		t.Fatal("write followed a dangling link outside")
	}
	if b, s := os.ReadFile(secret); string(b) != "s" || s != nil {
		t.Fatal("secret changed")
	}
	// the workspace root itself
	if err := e.fs.Delete(ctx, e.a, e.ws, true); err == nil {
		t.Fatal("deleting the root must fail")
	}
	// unconfined (host mode) reaches outside
	if _, err := e.fs.Read(ctx, fs.Access{Agent: "kai"}, secret); err != nil {
		t.Fatal(err)
	}
}

func TestHardRoots(t *testing.T) {
	base := t.TempDir()
	allowed := filepath.Join(base, "allowed")
	os.MkdirAll(allowed, 0o755)
	os.WriteFile(filepath.Join(base, "x"), []byte("x"), 0o644)
	e := setup(t, map[string]any{"roots": []string{allowed}})
	if _, err := e.fs.Read(context.Background(), fs.Access{Agent: "kai"}, filepath.Join(base, "x")); !errors.Is(err, fs.ErrOutsideRoots) {
		t.Fatal("hard roots apply even unconfined", err)
	}
	h := kernel.NewHost(nil)
	defer h.Close()
	h.Load(local.Plugin, kernel.LoadOptions{Config: map[string]any{"roots": []string{"relative"}}})
	if p, _ := h.Plugin("@crewbase/fs-local"); p.State != kernel.StateFailed {
		t.Fatal(p)
	}
}

func TestReadOnlyAndDelete(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	os.MkdirAll(e.p("d/e"), 0o755)
	os.WriteFile(e.p("d/e/f"), []byte("x"), 0o644)
	ro := e.a
	ro.ReadOnly = true
	if _, err := e.fs.Write(ctx, ro, e.p("n"), nil); !errors.Is(err, fs.ErrReadOnly) {
		t.Fatal(err)
	}
	if _, err := e.fs.Edit(ctx, ro, e.p("d/e/f"), "x", "y"); !errors.Is(err, fs.ErrReadOnly) {
		t.Fatal(err)
	}
	if err := e.fs.Delete(ctx, ro, e.p("d/e/f"), false); !errors.Is(err, fs.ErrReadOnly) {
		t.Fatal(err)
	}
	if _, err := e.fs.Read(ctx, ro, e.p("d/e/f")); err != nil {
		t.Fatal(err)
	}
	if err := e.fs.Delete(ctx, e.a, e.p("d"), false); err == nil || !strings.Contains(err.Error(), "recursive") {
		t.Fatal(err)
	}
	if err := e.fs.Delete(ctx, e.a, e.p("d"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.p("d")); err == nil {
		t.Fatal("not deleted")
	}
	if err := e.fs.Delete(ctx, e.a, e.p("d"), true); !errors.Is(err, fs.ErrNotFound) {
		t.Fatal(err)
	}
	// re-created file at a deleted path must be read again
	os.WriteFile(e.p("g"), []byte("1"), 0o644)
	e.fs.Read(ctx, e.a, e.p("g"))
	e.fs.Delete(ctx, e.a, e.p("g"), false)
	os.WriteFile(e.p("g"), []byte("1"), 0o644)
	if _, err := e.fs.Edit(ctx, e.a, e.p("g"), "1", "2"); !errors.Is(err, fs.ErrNotRead) {
		t.Fatal(err)
	}
}

func TestWriteIntentVeto(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	var seen []fs.Intent
	e.h.Load(kernel.Spec{Name: "policy", Apply: func(c *kernel.Context) error {
		fs.WriteIntent.Use(c, func(ctx context.Context, in *fs.Intent, next kernel.Next[*fs.Intent, *fs.Intent]) (*fs.Intent, error) {
			seen = append(seen, *in)
			if strings.HasSuffix(in.Path, ".env") {
				in.Deny = "secrets files are protected"
			}
			if strings.HasSuffix(in.Path, ".lock") {
				return in, nil // short-circuit without calling next = veto
			}
			return next(ctx, in)
		})
		return nil
	}}, kernel.LoadOptions{})

	if _, err := e.fs.Write(ctx, e.a, e.p("ok.txt"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.fs.Write(ctx, e.a, e.p(".env"), []byte("K=V")); !errors.Is(err, fs.ErrVetoed) || !strings.Contains(err.Error(), "protected") {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.p(".env")); err == nil {
		t.Fatal("vetoed write happened")
	}
	if _, err := e.fs.Write(ctx, e.a, e.p("go.lock"), []byte("x")); !errors.Is(err, fs.ErrVetoed) {
		t.Fatal(err)
	}
	e.fs.Read(ctx, e.a, e.p("ok.txt"))
	e.fs.Edit(ctx, e.a, e.p("ok.txt"), "x", "y")
	e.fs.Delete(ctx, e.a, e.p("ok.txt"), false)
	ops := []string{}
	for _, s := range seen {
		ops = append(ops, s.Op)
		if s.Agent != "kai" || !filepath.IsAbs(s.Path) {
			t.Fatal(s)
		}
	}
	if strings.Join(ops, ",") != "write,write,write,edit,delete" {
		t.Fatal(ops)
	}
	if seen[0].Exists || !seen[3].Exists {
		t.Fatal(seen)
	}
	// panicking listener vetoes (fail closed)
	e.h.Load(kernel.Spec{Name: "boom", Apply: func(c *kernel.Context) error {
		fs.WriteIntent.Use(c, func(ctx context.Context, in *fs.Intent, next kernel.Next[*fs.Intent, *fs.Intent]) (*fs.Intent, error) {
			panic("bug")
		})
		return nil
	}}, kernel.LoadOptions{})
	if _, err := e.fs.Write(ctx, e.a, e.p("p.txt"), []byte("x")); !errors.Is(err, fs.ErrVetoed) {
		t.Fatal(err)
	}
}

func TestTooLarge(t *testing.T) {
	e := setup(t, map[string]any{"maxReadBytes": 4})
	os.WriteFile(e.p("big"), []byte("12345"), 0o644)
	if _, err := e.fs.Read(context.Background(), e.a, e.p("big")); !errors.Is(err, fs.ErrTooLarge) {
		t.Fatal(err)
	}
}
