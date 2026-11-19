// Package credentials is the `credentials` capability (docs/06 §5): config
// stores references, never values. References are resolved host-side on
// every use, so rotation needs no restart.
//
// Reference schemes:
//
//	vault:<service>/<id>      Crewbase's encrypted vault in the data root
//	                          (vault.go), e.g. vault:crewbase/openai
//	keychain:<service>/<id>   the same vault item (the name configs used
//	                          before the vault; the system keychain is not
//	                          used any more)
//	env:NAME                  environment variable (read-only)
//	mem:<id>                  in-memory (tests, ephemeral tokens)
//
// Describe returns metadata only; no method returns plaintext except
// Resolve, which is for host-side use. An empty value is "not found".
// Every value the service sees is tracked in memory so redaction can scrub
// it from tool results and streams.
package credentials

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the key of the credentials service.
const ServiceKey = "credentials"

var (
	// ErrNotFound means the reference has no (non-empty) value.
	ErrNotFound = errors.New("credentials: not found")
	// ErrReadOnly is returned when writing to a read-only backend (env).
	ErrReadOnly = errors.New("credentials: backend is read-only")
	// ErrUnsupported means no backend is available for the reference.
	ErrUnsupported = errors.New("credentials: unsupported reference")
)

// Info is metadata about a reference. It never contains plaintext.
type Info struct {
	Ref        string `json:"ref"`
	Configured bool   `json:"configured"`
	Source     string `json:"source"`           // backend name
	Masked     string `json:"masked,omitempty"` // e.g. "sk-…a1b2"
}

// Backend stores secrets for one reference scheme. id is the part after
// "<scheme>:". Get returns ErrNotFound (or "", nil) when absent. Error
// messages must never contain the secret.
type Backend interface {
	Name() string
	Get(ctx context.Context, id string) (string, error)
	Set(ctx context.Context, id, value string) error
	Delete(ctx context.Context, id string) error
}

// Service is the `credentials` service.
type Service struct {
	timeout time.Duration
	host    *kernel.Host // for RefChanged; nil outside a host

	vault *Vault // nil without a data root

	mu       sync.Mutex
	backends map[string]Backend
	locks    map[string]*sync.Mutex
	tracked  map[string]struct{}
}

// New creates a service with the mem and env backends; UseVault adds the
// vault (the plugin does, for its data root).
func New() *Service {
	s := &Service{
		timeout:  15 * time.Second,
		backends: map[string]Backend{},
		locks:    map[string]*sync.Mutex{},
		tracked:  map[string]struct{}{},
	}
	s.backends["mem"] = NewMemory()
	s.backends["env"] = Env{}
	return s
}

// Vault is the service's vault (nil without a data root).
func (s *Service) Vault() *Vault { return s.vault }

// UseVault makes v the store of the "vault" and "keychain" schemes.
func (s *Service) UseVault(v *Vault) {
	s.mu.Lock()
	s.backends["vault"], s.backends["keychain"] = v, v
	s.vault = v
	s.mu.Unlock()
}

// SetBackend installs (or replaces) the backend for a scheme.
func (s *Service) SetBackend(scheme string, b Backend) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b == nil {
		delete(s.backends, scheme)
		return
	}
	s.backends[scheme] = b
}

// ParseRef splits "scheme:id".
func ParseRef(ref string) (scheme, id string, err error) {
	scheme, id, ok := strings.Cut(ref, ":")
	if !ok || scheme == "" || id == "" {
		return "", "", fmt.Errorf("%w: want scheme:id", ErrUnsupported)
	}
	return scheme, id, nil
}

func (s *Service) backend(ref string) (Backend, string, error) {
	scheme, id, err := ParseRef(ref)
	if err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	b, ok := s.backends[scheme]
	s.mu.Unlock()
	if !ok {
		return nil, "", fmt.Errorf("%w: no backend for scheme %q", ErrUnsupported, scheme)
	}
	return b, id, nil
}

func (s *Service) lock(ref string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[ref]
	if !ok {
		l = &sync.Mutex{}
		s.locks[ref] = l
	}
	return l
}

func (s *Service) track(v string) {
	if v == "" {
		return
	}
	s.mu.Lock()
	s.tracked[v] = struct{}{}
	s.mu.Unlock()
}

// Secrets returns every secret value seen by this service (resolved or
// written) since start. Used by redaction; never persisted.
func (s *Service) Secrets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.tracked))
	for v := range s.tracked {
		out = append(out, v)
	}
	return out
}

func (s *Service) get(ctx context.Context, ref string) (string, error) {
	b, id, err := s.backend(ref)
	if err != nil {
		return "", err
	}
	v, err := b.Get(ctx, id)
	if errors.Is(err, ErrNotFound) || (err == nil && v == "") {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	s.track(v)
	return v, nil
}

// Resolve returns the plaintext for ref. It is for host-side use only
// (adapters, MCP, plugins); the value must never be placed in model
// context. An empty value returns ErrNotFound.
func (s *Service) Resolve(ctx context.Context, ref string) (string, error) {
	return s.get(ctx, ref)
}

func (s *Service) bg() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), s.timeout)
}

// Describe returns metadata about ref without plaintext.
func (s *Service) Describe(ref string) Info {
	info := Info{Ref: ref}
	b, _, err := s.backend(ref)
	if err != nil {
		return info
	}
	info.Source = b.Name()
	ctx, cancel := s.bg()
	defer cancel()
	v, err := s.get(ctx, ref)
	if err != nil {
		return info
	}
	info.Configured, info.Masked = true, Mask(v)
	return info
}

// Set writes value to ref (write-only). An empty value deletes it.
func (s *Service) Set(ref, value string) error {
	l := s.lock(ref)
	l.Lock()
	defer l.Unlock()
	return s.setLocked(ref, value)
}

func (s *Service) setLocked(ref, value string) error {
	if value == "" {
		return s.deleteLocked(ref)
	}
	b, id, err := s.backend(ref)
	if err != nil {
		return err
	}
	s.track(value)
	ctx, cancel := s.bg()
	defer cancel()
	if err := b.Set(ctx, id, value); err != nil {
		return err
	}
	s.changed(ref, "set")
	return nil
}

// RefChange reports that the value behind a reference was written or
// removed. It never carries the value.
type RefChange struct {
	Ref    string `json:"ref"`
	Action string `json:"action"` // set | delete
}

// RefChanged fires after a successful Set or Delete (audited).
var RefChanged = kernel.NewEmit[RefChange]("credentials/ref-changed")

func (s *Service) changed(ref, action string) {
	// test-only in-memory refs (mem:) are not worth a record
	if s.host == nil || strings.HasPrefix(ref, "mem:") {
		return
	}
	RefChanged.Emit(s.host, RefChange{Ref: ref, Action: action})
}

// Delete removes ref. Deleting a missing ref is not an error.
func (s *Service) Delete(ref string) error {
	l := s.lock(ref)
	l.Lock()
	defer l.Unlock()
	return s.deleteLocked(ref)
}

func (s *Service) deleteLocked(ref string) error {
	b, id, err := s.backend(ref)
	if err != nil {
		return err
	}
	ctx, cancel := s.bg()
	defer cancel()
	err = b.Delete(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err == nil {
		s.changed(ref, "delete")
	}
	return err
}

// RefreshLocked serializes read → decide → replace for ref (e.g. OAuth
// refresh): fn receives the current value ("" if absent) and returns the
// new one. Returning the same value (or "") leaves it unchanged; an error
// aborts without writing. fn must not call Set/Delete/RefreshLocked on the
// same ref (it would deadlock); Resolve is fine.
func (s *Service) RefreshLocked(ref string, fn func(old string) (string, error)) error {
	l := s.lock(ref)
	l.Lock()
	defer l.Unlock()
	ctx, cancel := s.bg()
	old, err := s.get(ctx, ref)
	cancel()
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	nv, err := fn(old)
	if err != nil {
		return err
	}
	if nv == "" || nv == old {
		return nil
	}
	return s.setLocked(ref, nv)
}

// Mask renders a short preview such as "sk-…a1b2". Short values reveal
// nothing.
func Mask(v string) string {
	n := utf8.RuneCountInString(v)
	if n < 12 {
		return "…"
	}
	r := []rune(v)
	prefix := 3
	if n < 16 {
		prefix = 2
	}
	return string(r[:prefix]) + "…" + string(r[n-4:])
}

// ---------------------------------------------------------------- memory

// Memory is an in-memory backend.
type Memory struct {
	mu sync.Mutex
	m  map[string]string
}

func NewMemory() *Memory { return &Memory{m: map[string]string{}} }

func (*Memory) Name() string { return "memory" }

func (b *Memory) Get(_ context.Context, id string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.m[id]
	if !ok || v == "" {
		return "", ErrNotFound
	}
	return v, nil
}

func (b *Memory) Set(_ context.Context, id, v string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m[id] = v
	return nil
}

func (b *Memory) Delete(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.m, id)
	return nil
}

// ---------------------------------------------------------------- env

// Env reads environment variables; it is read-only.
type Env struct{}

func (Env) Name() string { return "env" }

func (Env) Get(_ context.Context, name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", ErrNotFound
	}
	return v, nil
}

func (Env) Set(context.Context, string, string) error { return ErrReadOnly }
func (Env) Delete(context.Context, string) error      { return ErrReadOnly }

// Config of the plugin.
type Config struct {
	// Root is the data root; the vault lives in <root>/vault.
	Root string `json:"root"`
}

// Plugin provides the `credentials` service.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/credentials",
	Description: "Credential references: the encrypted vault, env, memory (credentials service).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		s := New()
		s.host = c.Host()
		if cfg.Root != "" {
			v, err := OpenVault(filepath.Join(cfg.Root, VaultDir))
			if err != nil {
				return err
			}
			s.UseVault(v)
		}
		return c.Provide(ServiceKey, s)
	},
})
