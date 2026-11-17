package acp

// Managing subscription CLIs from the UI (settings › 模型与订阅): rename a
// CLI, change the command that starts it in ACP mode and its environment,
// turn it off and on, add a custom CLI that speaks ACP on stdio, remove it
// again, and learn its models now.
//
// What the person changes is kept in <stateDir>/subscriptions.json and
// merged over the built-in table and the plugin config (agents in a patch
// file keep working; the file wins where both set a field). Environment
// values may be secrets: with the credentials service they are stored in
// the system keychain (keychain:crewbase/subscription-<id>, one JSON object
// per CLI) and only the variable names are in the file; the API shows
// masked values only.
//
// Changes apply at once: each adapter is registered under its own kernel
// context, so a CLI is re-registered (or unregistered) without touching the
// others. A disabled CLI stays disabled: Rescan only registers runnable
// CLIs, and a disabled one is not runnable.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/llm"
)

// overridesFile keeps the UI's changes, in the state dir.
const overridesFile = "subscriptions.json"

// Override is what the person changed about one CLI.
type Override struct {
	ID string `json:"id"`
	// Custom: added from the UI (removed for good when removed).
	Custom bool   `json:"custom,omitempty"`
	Name   string `json:"name,omitempty"`
	// Command replaces the ACP entrypoint (empty = the default one).
	Command []string `json:"command,omitempty"`
	// EnvKeys are the variable names; the values are in the keychain
	// under EnvRef.
	EnvKeys []string `json:"envKeys,omitempty"`
	EnvRef  string   `json:"envRef,omitempty"`
	// Env holds the values only when no credentials service is available.
	Env      map[string]string `json:"env,omitempty"`
	Disabled *bool             `json:"disabled,omitempty"`
	Created  time.Time         `json:"created,omitzero"`
}

type overrides struct {
	path string
	mu   sync.Mutex
	list []Override
	env  map[string]map[string]string // resolved values, by id
}

func loadOverrides(dir string) (*overrides, error) {
	o := &overrides{env: map[string]map[string]string{}}
	if dir == "" {
		return o, nil
	}
	o.path = filepath.Join(dir, overridesFile)
	b, err := os.ReadFile(o.path)
	if errors.Is(err, os.ErrNotExist) {
		return o, nil
	}
	if err != nil {
		return o, err
	}
	var doc struct {
		Agents []Override `json:"agents"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return o, fmt.Errorf("%s: %w", overridesFile, err)
	}
	for _, x := range doc.Agents {
		if x.ID != "" {
			o.list = append(o.list, x)
		}
	}
	return o, nil
}

func (o *overrides) get(id string) (Override, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, x := range o.list {
		if x.ID == id {
			return x, true
		}
	}
	return Override{}, false
}

// put stores x (replacing the entry with its id) and writes the file.
func (o *overrides) put(x Override) error {
	o.mu.Lock()
	i := slices.IndexFunc(o.list, func(y Override) bool { return y.ID == x.ID })
	if i >= 0 {
		o.list[i] = x
	} else {
		o.list = append(o.list, x)
	}
	o.mu.Unlock()
	return o.save()
}

func (o *overrides) drop(id string) error {
	o.mu.Lock()
	o.list = slices.DeleteFunc(o.list, func(y Override) bool { return y.ID == id })
	delete(o.env, id)
	o.mu.Unlock()
	return o.save()
}

func (o *overrides) save() error {
	if o.path == "" {
		return nil
	}
	o.mu.Lock()
	b, err := json.MarshalIndent(map[string]any{"agents": o.list}, "", "  ")
	o.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.path), 0o700); err != nil {
		return err
	}
	tmp := o.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, o.path)
}

// secretStore keeps environment values (the credentials service).
type secretStore interface {
	Set(ref, value string) error
	Resolve(ctx context.Context, ref string) (string, error)
	Delete(ref string) error
}

func envRef(id string) string { return "keychain:crewbase/subscription-" + id }

// loadEnv reads the stored environment of every CLI that has one.
func (s *Service) loadEnv(ctx context.Context) {
	st := s.store()
	s.ov.mu.Lock()
	list := slices.Clone(s.ov.list)
	s.ov.mu.Unlock()
	for _, x := range list {
		env := map[string]string{}
		switch {
		case x.EnvRef != "" && st != nil:
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			v, err := st.Resolve(rctx, x.EnvRef)
			cancel()
			if err == nil {
				_ = json.Unmarshal([]byte(v), &env)
			} else if !errors.Is(err, credentials.ErrNotFound) {
				s.c.Log().Warn("acp: environment of a CLI not read", "agent", x.ID, "err", err)
			}
		case len(x.Env) > 0:
			env = x.Env
		default:
			continue
		}
		s.ov.mu.Lock()
		s.ov.env[x.ID] = env
		s.ov.mu.Unlock()
	}
}

func (s *Service) store() secretStore {
	if s.secrets == nil {
		return nil
	}
	return s.secrets()
}

// config is the plugin config with the overrides applied.
func (s *Service) config() Config {
	cfg := s.cfg
	if s.ov == nil {
		return cfg
	}
	agents := slices.Clone(cfg.Agents)
	s.ov.mu.Lock()
	defer s.ov.mu.Unlock()
	for _, x := range s.ov.list {
		i := slices.IndexFunc(agents, func(a AgentConfig) bool { return a.ID == x.ID })
		if i < 0 {
			_, isBuiltin := findBuiltin(x.ID)
			if !x.Custom && (!isBuiltin || (cfg.Builtins != nil && !*cfg.Builtins)) {
				continue // an override of something no longer there
			}
			agents = append(agents, AgentConfig{ID: x.ID})
			i = len(agents) - 1
		}
		a := &agents[i]
		if x.Name != "" {
			a.Name = x.Name
		}
		if len(x.Command) > 0 {
			a.Command = slices.Clone(x.Command)
		}
		if env := s.ov.env[x.ID]; len(env) > 0 {
			m := make(map[string]string, len(a.Env)+len(env))
			for k, v := range a.Env {
				m[k] = v
			}
			for k, v := range env {
				m[k] = v
			}
			a.Env = m
		}
		if x.Disabled != nil {
			a.Disabled = *x.Disabled
		}
	}
	cfg.Agents = agents
	return cfg
}

func (s *Service) agentConfig(id string) (AgentConfig, bool) {
	for _, a := range agentList(s.config()) {
		if a.ID == id {
			return a, true
		}
	}
	return AgentConfig{}, false
}

func (s *Service) setErr(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.errs == nil {
		s.errs = map[string]string{}
	}
	if err == nil {
		delete(s.errs, id)
	} else {
		s.errs[id] = err.Error()
	}
}

// unmount unregisters the CLI's adapter (its processes stop).
func (s *Service) unmount(id string) {
	s.mu.Lock()
	ctx := s.live[id]
	s.mu.Unlock()
	if ctx != nil {
		ctx.Dispose()
	}
}

// remount re-registers the CLI with its current config (or leaves it
// unregistered when it is disabled or cannot run).
func (s *Service) remount(id string) error {
	s.unmount(id)
	ac, ok := s.agentConfig(id)
	if !ok {
		return nil
	}
	r := resolve(ac)
	if !r.runnable() || s.add == nil {
		return nil
	}
	err := s.add(r)
	s.setErr(id, err)
	return err
}

// ErrNotFound: no CLI with that id.
var ErrNotFound = errors.New("subscription CLI not found")

// EnvVar is an environment variable as the UI sees it.
type EnvVar struct {
	Key    string `json:"key"`
	Masked string `json:"masked"`
}

// Subscription is one CLI as the settings page sees it (no secrets).
type Subscription struct {
	CLIInfo
	// Kind: builtin | custom.
	Kind string `json:"kind"`
	// Removable: added from the UI (removing a built-in disables it).
	Removable bool `json:"removable"`
	Enabled   bool `json:"enabled"`
	// Active: registered as a model provider now.
	Active bool `json:"active"`
	// CLI is the binary checked for "installed".
	CLI string `json:"cli"`
	// Command is the ACP command the person set (empty = automatic).
	Command []string `json:"command"`
	Env     []EnvVar `json:"env"`
	Models  int      `json:"models"`
	// Reason says why an enabled CLI is not active: not_installed |
	// no_entrypoint.
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

// List describes every known CLI, built-in and custom (disabled ones too).
func (s *Service) List(ctx context.Context) []Subscription {
	s.Rescan()
	cfg := s.config()
	infos := Detect(ctx, cfg)
	acs := agentList(cfg)
	out := make([]Subscription, 0, len(infos))
	for i, info := range infos {
		ac := acs[i]
		r := resolve(ac)
		v := Subscription{CLIInfo: info, Kind: "builtin", Enabled: !ac.Disabled, CLI: r.CLI, Command: []string{}, Env: []EnvVar{}}
		if _, ok := findBuiltin(ac.ID); !ok {
			v.Kind = "custom"
		}
		if x, ok := s.ov.get(ac.ID); ok {
			v.Removable = x.Custom
			if len(x.Command) > 0 {
				v.Command = slices.Clone(x.Command)
			}
			s.ov.mu.Lock()
			env := s.ov.env[ac.ID]
			for _, k := range sortedKeys(env) {
				v.Env = append(v.Env, EnvVar{Key: k, Masked: credentials.Mask(env[k])})
			}
			s.ov.mu.Unlock()
		}
		if a, ok := s.Adapter(ac.ID); ok {
			v.Active, v.Running = true, a.Running()
			if ms, err := a.Models(ctx); err == nil {
				v.Models = len(ms)
			}
		}
		if v.Enabled && !v.Active {
			switch {
			case r.cliPath == "":
				v.Reason = "not_installed"
			case len(r.Command) == 0:
				v.Reason = "no_entrypoint"
			}
		}
		s.mu.Lock()
		v.Error = s.errs[ac.ID]
		s.mu.Unlock()
		out = append(out, v)
	}
	return out
}

// Get describes one CLI.
func (s *Service) Get(ctx context.Context, id string) (Subscription, bool) {
	for _, v := range s.List(ctx) {
		if v.ID == id {
			return v, true
		}
	}
	return Subscription{}, false
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

var (
	subIDRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

// checkCommand validates an ACP command and that its program exists.
func checkCommand(cmd []string) ([]string, error) {
	if len(cmd) > 64 {
		return nil, errors.New("command has too many arguments")
	}
	out := make([]string, 0, len(cmd))
	for _, a := range cmd {
		if len(a) > 4096 || strings.ContainsRune(a, 0) {
			return nil, errors.New("command argument too long or invalid")
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return out, nil
	}
	out[0] = strings.TrimSpace(out[0])
	if out[0] == "" {
		return nil, errors.New("the command needs a program")
	}
	if _, err := lookPath(out[0]); err != nil {
		return nil, fmt.Errorf("program %q not found (on PATH or as a path)", out[0])
	}
	return out, nil
}

func checkEnv(env map[string]string) error {
	if len(env) > 50 {
		return errors.New("too many environment variables")
	}
	for k, v := range env {
		if !envKeyRe.MatchString(k) {
			return fmt.Errorf("invalid environment variable name %q", k)
		}
		if len(v) > 8192 || strings.ContainsRune(v, 0) {
			return fmt.Errorf("value of %s too long or invalid", k)
		}
	}
	return nil
}

// storeEnv keeps env for x (keychain when available) and caches it.
func (s *Service) storeEnv(x *Override, env map[string]string) error {
	if err := checkEnv(env); err != nil {
		return err
	}
	x.EnvKeys, x.Env = sortedKeys(env), nil
	st := s.store()
	switch {
	case len(env) == 0:
		if x.EnvRef != "" && st != nil {
			_ = st.Delete(x.EnvRef)
		}
		x.EnvRef, x.EnvKeys = "", nil
	case st != nil:
		b, _ := json.Marshal(env)
		x.EnvRef = envRef(x.ID)
		if err := st.Set(x.EnvRef, string(b)); err != nil {
			return fmt.Errorf("store environment: %w", err)
		}
	default:
		x.Env, x.EnvRef = env, ""
	}
	s.ov.mu.Lock()
	if len(env) == 0 {
		delete(s.ov.env, x.ID)
	} else {
		s.ov.env[x.ID] = env
	}
	s.ov.mu.Unlock()
	return nil
}

// NewCLI is a custom CLI to add.
type NewCLI struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Command []string          `json:"command"`
	Env     map[string]string `json:"env"`
}

// Add registers a custom CLI that speaks ACP on stdio and learns its
// models. A failed probe does not undo the add; it is reported in Error.
func (s *Service) Add(ctx context.Context, in NewCLI) (Subscription, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	id := strings.ToLower(strings.TrimSpace(in.ID))
	if !subIDRe.MatchString(id) {
		return Subscription{}, errors.New("id must be lower-case letters, digits and dashes (max 32)")
	}
	if _, ok := findBuiltin(id); ok {
		return Subscription{}, fmt.Errorf("%q is a built-in CLI", id)
	}
	if _, ok := s.agentConfig(id); ok {
		return Subscription{}, fmt.Errorf("a subscription CLI %q already exists", id)
	}
	if s.reg != nil {
		if _, taken := s.reg.Get(providerID(id)); taken {
			return Subscription{}, fmt.Errorf("a provider %q already exists", providerID(id))
		}
	}
	name := strings.TrimSpace(in.Name)
	if len(name) > 80 {
		return Subscription{}, errors.New("name too long")
	}
	if len(in.Command) == 0 {
		return Subscription{}, errors.New("a command is required")
	}
	cmd, err := checkCommand(in.Command)
	if err != nil {
		return Subscription{}, err
	}
	x := Override{ID: id, Custom: true, Name: name, Command: cmd, Created: time.Now().UTC()}
	if err := s.storeEnv(&x, in.Env); err != nil {
		return Subscription{}, err
	}
	if err := s.ov.put(x); err != nil {
		return Subscription{}, err
	}
	if err := s.remount(id); err != nil {
		v, _ := s.Get(ctx, id)
		return v, nil
	}
	s.probe(ctx, id)
	v, _ := s.Get(ctx, id)
	return v, nil
}

// Patch edits a CLI; nil fields stay as they are.
type Patch struct {
	Name *string `json:"name"`
	// Command replaces the ACP command; [] returns a built-in to its
	// default entrypoint.
	Command *[]string `json:"command"`
	// Env replaces the environment: a null value keeps the stored value
	// of that variable, a missing variable is removed.
	Env     *map[string]*string `json:"env"`
	Enabled *bool               `json:"enabled"`
}

// Update edits a CLI and re-registers it at once.
func (s *Service) Update(ctx context.Context, id string, pt Patch) (Subscription, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if _, ok := s.agentConfig(id); !ok {
		return Subscription{}, ErrNotFound
	}
	x, ok := s.ov.get(id)
	if !ok {
		x = Override{ID: id, Created: time.Now().UTC()}
	}
	_, isBuiltin := findBuiltin(id)
	cmdChanged := false
	if pt.Name != nil {
		n := strings.TrimSpace(*pt.Name)
		if len(n) > 80 {
			return Subscription{}, errors.New("name too long")
		}
		x.Name = n
	}
	if pt.Command != nil {
		cmd, err := checkCommand(*pt.Command)
		if err != nil {
			return Subscription{}, err
		}
		if len(cmd) == 0 && !isBuiltin && (x.Custom || !slices.ContainsFunc(s.cfg.Agents, func(a AgentConfig) bool { return a.ID == id && len(a.Command) > 0 })) {
			return Subscription{}, errors.New("a command is required")
		}
		cmdChanged = !slices.Equal(cmd, x.Command)
		x.Command = cmd
		if len(cmd) == 0 {
			x.Command = nil
		}
	}
	if pt.Env != nil {
		s.ov.mu.Lock()
		old := s.ov.env[id]
		s.ov.mu.Unlock()
		env := map[string]string{}
		for k, v := range *pt.Env {
			k = strings.TrimSpace(k)
			switch {
			case v != nil:
				env[k] = *v
			case old != nil:
				if ov, ok := old[k]; ok {
					env[k] = ov
				}
			}
		}
		if err := s.storeEnv(&x, env); err != nil {
			return Subscription{}, err
		}
		cmdChanged = true
	}
	if pt.Enabled != nil {
		off := !*pt.Enabled
		x.Disabled = &off
	}
	if err := s.ov.put(x); err != nil {
		return Subscription{}, err
	}
	err := s.remount(id)
	if err == nil && cmdChanged && s.c != nil {
		// another command may offer other models
		s.c.Go(func(ctx context.Context) {
			pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			s.probe(pctx, id)
		})
	}
	v, _ := s.Get(ctx, id)
	return v, nil
}

// Remove deletes a custom CLI added from the UI; any other is disabled
// (Update with enabled: true brings it back). It reports whether the CLI
// was deleted.
func (s *Service) Remove(ctx context.Context, id string) (deleted bool, err error) {
	x, ok := s.ov.get(id)
	if !ok || !x.Custom {
		off := false
		if _, err := s.Update(ctx, id, Patch{Enabled: &off}); err != nil {
			return false, err
		}
		return false, nil
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.unmount(id)
	if x.EnvRef != "" {
		if st := s.store(); st != nil {
			_ = st.Delete(x.EnvRef)
		}
	}
	s.setErr(id, nil)
	return true, s.ov.drop(id)
}

// Refresh detects the CLI again (a new version or entrypoint re-registers
// it) and learns its models now, whatever the cache says. The models
// registry has the new list when it returns.
func (s *Service) Refresh(ctx context.Context, id string) (Subscription, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	ac, ok := s.agentConfig(id)
	if !ok {
		return Subscription{}, ErrNotFound
	}
	r := resolve(ac)
	switch {
	case ac.Disabled:
		return Subscription{}, errors.New("this CLI is disabled; enable it first")
	case r.cliPath == "":
		s.unmount(id)
		return Subscription{}, fmt.Errorf("%s is not installed (%s not found on PATH)", r.Name, r.CLI)
	case len(r.Command) == 0:
		s.unmount(id)
		return Subscription{}, fmt.Errorf("no ACP entrypoint found for %s", r.Name)
	}
	a, have := s.Adapter(id)
	if !have || !sameRun(a.cfg, r) || !sameBridge(a.bridge, bridgeOf(r)) {
		if err := s.remount(id); err != nil {
			return Subscription{}, err
		}
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	err := s.probe(pctx, id)
	v, _ := s.Get(ctx, id)
	return v, err
}

// probe learns the registered CLI's models now and tells the registry.
func (s *Service) probe(ctx context.Context, id string) error {
	a, ok := s.Adapter(id)
	if !ok {
		return nil
	}
	a.SetVersions(versionKey(versionOf(ctx, a.cfg.cliPath), a.Bridge()))
	err := a.ProbeModels(ctx)
	if err != nil {
		err = fmt.Errorf("model list not learned: %w", err)
	}
	s.setErr(id, err)
	if s.c != nil {
		llm.AdaptersChanged.Emit(s.c.Host(), struct{}{})
	}
	return err
}

// sameRun: the same process would be started.
func sameRun(a, b resolved) bool {
	if a.Name != b.Name || a.cliPath != b.cliPath || !slices.Equal(a.Command, b.Command) || len(a.Env) != len(b.Env) {
		return false
	}
	for k, v := range a.Env {
		if b.Env[k] != v {
			return false
		}
	}
	return true
}

func sameBridge(a, b *llm.Bridge) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Package == b.Package && a.Version == b.Version
}
