// Package wasm runs WebAssembly plugins in the host (docs/05 §7): light,
// high-frequency pure hooks — stream-chunk transforms, tool-result
// redaction, rule checks — that are too chatty for a subprocess.
//
// Isolation: modules run in wazero (pure Go, no cgo) with their own linear
// memory capped at MemoryPages. They get WASI so ordinary Go / Rust /
// TinyGo builds work, but with no file system, no network, no
// environment and no arguments; stdout / stderr go to the plugin log.
// Every call has a deadline; a call that errors, traps or times out
// changes nothing (the hook passes its input through), and the `rule`
// hook can only tighten a decision (ask / deny), never allow.
//
// ABI (JSON in, JSON out):
//
//	export alloc(size u32) -> ptr u32        // buffer for the input
//	export hook_<name>(ptr u32, len u32) -> u64  // (out_ptr << 32) | out_len
//
// Hooks:
//
//	stream_chunk  in {"type","text"}                         out {"text"}
//	tool_result   in {"tool","category","content","isError"} out {"content"}
//	rule          in {"tool","category","summary","args","mode"} out {"verdict":"ask"|"deny"|"", "reason"}
package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Limits.
const (
	MemoryPages   = 256 // 16 MiB
	MaxModule     = 32 << 20
	MaxIO         = 4 << 20
	DefaultBudget = 50 * time.Millisecond
)

// Module is one plugin module. Calls are serialised (a module's linear
// memory is not shared between calls). A call that runs past its budget
// closes the instance; the next call starts a fresh one.
type Module struct {
	name    string
	bin     []byte
	hooks   []string
	budget  time.Duration
	log     *slog.Logger
	out     *logWriter
	cache   wazero.CompilationCache // reinstantiation after a timeout is cheap
	mu      sync.Mutex
	inst    *instance
	failing int // consecutive failures (logged once per streak)
}

type instance struct {
	rt    wazero.Runtime
	mod   api.Module
	alloc api.Function
	hooks map[string]api.Function
}

// logWriter forwards module output to the log, bounded.
type logWriter struct {
	log  *slog.Logger
	name string
	mu   sync.Mutex
	n    int
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.n < 64<<10 {
		w.n += len(p)
		w.log.Info("wasm plugin output", "plugin", w.name, "text", strings.TrimSpace(string(p)))
	}
	return len(p), nil
}

// Load compiles and instantiates path. hooks are the declared hooks; each
// must be exported as hook_<name>.
func Load(ctx context.Context, name, path string, hooks []string, budget time.Duration, log *slog.Logger) (*Module, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > MaxModule {
		return nil, fmt.Errorf("wasm module is larger than %d MB", MaxModule>>20)
	}
	bin, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if budget <= 0 {
		budget = DefaultBudget
	}
	if log == nil {
		log = slog.Default()
	}
	m := &Module{name: name, bin: bin, hooks: hooks, budget: budget, log: log, out: &logWriter{log: log, name: name}, cache: wazero.NewCompilationCache()}
	inst, err := m.instantiate(ctx)
	if err != nil {
		return nil, err
	}
	m.inst = inst
	return m, nil
}

func (m *Module) instantiate(ctx context.Context) (*instance, error) {
	cfg := wazero.NewRuntimeConfig().WithMemoryLimitPages(MemoryPages).WithCloseOnContextDone(true).WithCompilationCache(m.cache)
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	fail := func(err error) (*instance, error) {
		rt.Close(context.Background())
		return nil, err
	}
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return fail(err)
	}
	compiled, err := rt.CompileModule(ctx, m.bin)
	if err != nil {
		return fail(fmt.Errorf("compile: %w", err))
	}
	for _, imp := range compiled.ImportedFunctions() {
		mod, _, _ := imp.Import()
		if mod != wasi_snapshot_preview1.ModuleName {
			return fail(fmt.Errorf("the module imports %q, which the host does not provide (only WASI)", mod))
		}
	}
	// no args, no env, no file system: WASI only for clocks, random and logs
	mcfg := wazero.NewModuleConfig().WithName(m.name).WithStdout(m.out).WithStderr(m.out).WithStdin(eofReader{}).
		WithSysWalltime().WithSysNanotime().WithRandSource(randReader{}).
		WithStartFunctions() // reactor: _initialize below, never _start
	mod, err := rt.InstantiateModule(ctx, compiled, mcfg)
	if err != nil {
		return fail(fmt.Errorf("instantiate: %w", err))
	}
	if init := mod.ExportedFunction("_initialize"); init != nil {
		ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := init.Call(ictx)
		cancel()
		if err != nil {
			return fail(fmt.Errorf("_initialize: %w", err))
		}
	}
	in := &instance{rt: rt, mod: mod, alloc: mod.ExportedFunction("alloc"), hooks: map[string]api.Function{}}
	if in.alloc == nil {
		return fail(errors.New("the module does not export alloc(size) -> ptr"))
	}
	for _, h := range m.hooks {
		f := mod.ExportedFunction("hook_" + h)
		if f == nil {
			return fail(fmt.Errorf("the module declares hook %q but does not export hook_%s", h, h))
		}
		in.hooks[h] = f
	}
	return in, nil
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

type randReader struct{}

func (randReader) Read(p []byte) (int, error) {
	f, err := os.Open("/dev/urandom")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.ReadFull(f, p)
}

// Close releases the module.
func (m *Module) Close(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error
	if m.inst != nil {
		err = m.inst.rt.Close(ctx)
		m.inst = nil
	}
	_ = m.cache.Close(ctx)
	return err
}

// Has reports whether the module declares hook.
func (m *Module) Has(hook string) bool {
	for _, h := range m.hooks {
		if h == hook {
			return true
		}
	}
	return false
}

// ErrBudget is returned when a call exceeds its time budget.
var ErrBudget = errors.New("wasm hook exceeded its time budget")

// Call runs hook with in (JSON-encoded) and decodes the output into out.
// On any error out is left untouched.
func (m *Module) Call(ctx context.Context, hook string, in, out any) error {
	if !m.Has(hook) {
		return fmt.Errorf("no hook %q", hook)
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if len(b) > MaxIO {
		return fmt.Errorf("hook input larger than %d MB", MaxIO>>20)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inst == nil {
		inst, err := m.instantiate(ctx)
		if err != nil {
			return m.failed(hook, err)
		}
		m.inst = inst
	}
	cctx, cancel := context.WithTimeout(ctx, m.budget)
	defer cancel()
	err = m.call(cctx, m.inst, m.inst.hooks[hook], b, out)
	if err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			err = ErrBudget
		}
		// the instance may be closed or in a bad state: start fresh next time
		m.inst.rt.Close(context.Background())
		m.inst = nil
		return m.failed(hook, err)
	}
	m.failing = 0
	return nil
}

func (m *Module) failed(hook string, err error) error {
	if m.failing == 0 {
		m.log.Warn("wasm hook failed; passing through", "plugin", m.name, "hook", hook, "err", err)
	}
	m.failing++
	return err
}

func (m *Module) call(ctx context.Context, in *instance, f api.Function, data []byte, out any) error {
	r, err := in.alloc.Call(ctx, uint64(len(data)))
	if err != nil {
		return fmt.Errorf("alloc: %w", err)
	}
	ptr := uint32(r[0])
	if !in.mod.Memory().Write(ptr, data) {
		return errors.New("alloc returned memory outside the module")
	}
	res, err := f.Call(ctx, uint64(ptr), uint64(len(data)))
	if err != nil {
		return err
	}
	optr, olen := uint32(res[0]>>32), uint32(res[0])
	if olen == 0 {
		return nil // nothing to change
	}
	if olen > MaxIO {
		return fmt.Errorf("hook output larger than %d MB", MaxIO>>20)
	}
	b, ok := in.mod.Memory().Read(optr, olen)
	if !ok {
		return errors.New("hook output is outside the module's memory")
	}
	return json.Unmarshal(append([]byte(nil), b...), out)
}
