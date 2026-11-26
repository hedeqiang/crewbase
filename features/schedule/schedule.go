// Package schedule provides the `schedule` capability: timed (cron, every,
// daily, weekly, at) and webhook triggers that deliver firings to owner
// handlers (routines, workflows…). Management and delivery are serialized;
// timed jobs fire at most once a minute; every firing carries an idempotency
// key that is persisted before delivery (at-most-once — a crash between the
// two loses that firing rather than repeating it).
package schedule

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "schedule"

// Job is a persisted trigger.
type Job struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Owner   string          `json:"owner"` // handler kind, e.g. "routine"
	Spec    string          `json:"spec"`  // trigger spec, or "webhook"
	Payload json.RawMessage `json:"payload,omitempty"`
	Enabled bool            `json:"enabled"`
	Created time.Time       `json:"created"`
	// Webhook jobs: secret required on POST /api/hooks/{id}.
	Secret   string    `json:"secret,omitempty"`
	LastFire time.Time `json:"lastFire,omitzero"`
	LastKey  string    `json:"lastKey,omitempty"`
	NextFire time.Time `json:"nextFire,omitzero"`
	Fires    int       `json:"fires"`
	LastErr  string    `json:"lastError,omitempty"`
}

// IsWebhook reports whether the job is triggered by HTTP.
func (j Job) IsWebhook() bool { return j.Spec == "webhook" }

// Firing is one delivery.
type Firing struct {
	Job       Job
	Key       string    // idempotency key
	Scheduled time.Time // planned time (or receive time for webhooks)
	Body      json.RawMessage
}

// Handler receives firings for one owner kind.
type Handler func(ctx context.Context, f Firing) error

// Changed notifies that jobs changed (no delta).
var Changed = kernel.NewEmit[struct{}]("schedule/changed")

// Service is the `schedule` service.
type Service struct {
	c    *kernel.Context
	path string
	now  func() time.Time

	mu       sync.Mutex // serializes management and delivery
	jobs     map[string]*Job
	handlers map[string]Handler
	seen     map[string]time.Time // recent webhook idempotency keys
}

// New creates a service persisting to dir ("" = memory).
func New(c *kernel.Context, dir string, now func() time.Time) (*Service, error) {
	if now == nil {
		now = time.Now
	}
	s := &Service{c: c, now: now, jobs: map[string]*Job{}, handlers: map[string]Handler{}, seen: map[string]time.Time{}}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		s.path = filepath.Join(dir, "schedule.json")
		b, err := os.ReadFile(s.path)
		if err == nil {
			var jobs []*Job
			if err := json.Unmarshal(b, &jobs); err != nil {
				return nil, fmt.Errorf("schedule.json: %w", err)
			}
			for _, j := range jobs {
				s.jobs[j.ID] = j
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	// Missed firings while the daemon was off are skipped, not replayed.
	t := s.now()
	for _, j := range s.jobs {
		s.plan(j, t)
	}
	return s, nil
}

func (s *Service) plan(j *Job, after time.Time) {
	j.NextFire = time.Time{}
	if !j.Enabled || j.IsWebhook() {
		return
	}
	sp, err := Parse(j.Spec)
	if err != nil {
		j.LastErr = err.Error()
		return
	}
	next := sp.Next(after)
	if earliest := j.LastFire.Add(MinInterval); !j.LastFire.IsZero() && !next.IsZero() && next.Before(earliest) {
		next = sp.Next(earliest.Add(-time.Second))
	}
	j.NextFire = next
}

func (s *Service) saveLocked() error {
	if s.path == "" {
		return nil
	}
	list := s.listLocked()
	b, _ := json.MarshalIndent(list, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Service) listLocked() []Job {
	out := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Created.Before(out[k].Created) })
	return out
}

func randID(prefix string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// Add creates a job. Webhook jobs get a random secret.
func (s *Service) Add(j Job) (Job, error) {
	if j.Owner == "" {
		return j, errors.New("job owner is required")
	}
	if j.IsWebhook() {
		j.Secret = randID("", 16)
	} else if _, err := Parse(j.Spec); err != nil {
		return j, err
	}
	if j.ID == "" {
		j.ID = randID("job_", 6)
	}
	j.Created = s.now().UTC()
	j.Enabled = true
	s.mu.Lock()
	if _, ok := s.jobs[j.ID]; ok {
		s.mu.Unlock()
		return j, fmt.Errorf("job %s exists", j.ID)
	}
	jj := j
	s.plan(&jj, s.now())
	s.jobs[j.ID] = &jj
	err := s.saveLocked()
	out := jj
	s.mu.Unlock()
	Changed.Emit(s.c, struct{}{})
	return out, err
}

// Update changes spec/name/payload/enabled of a job.
func (s *Service) Update(id string, fn func(*Job)) (Job, error) {
	s.mu.Lock()
	j, ok := s.jobs[id]
	if !ok {
		s.mu.Unlock()
		return Job{}, fmt.Errorf("job %s not found", id)
	}
	cp := *j
	fn(&cp)
	cp.ID, cp.Owner, cp.Created, cp.Secret = j.ID, j.Owner, j.Created, j.Secret
	if !cp.IsWebhook() {
		if _, err := Parse(cp.Spec); err != nil {
			s.mu.Unlock()
			return *j, err
		}
	}
	s.plan(&cp, s.now())
	*j = cp
	err := s.saveLocked()
	s.mu.Unlock()
	Changed.Emit(s.c, struct{}{})
	return cp, err
}

// Remove deletes a job.
func (s *Service) Remove(id string) error {
	s.mu.Lock()
	if _, ok := s.jobs[id]; !ok {
		s.mu.Unlock()
		return fmt.Errorf("job %s not found", id)
	}
	delete(s.jobs, id)
	err := s.saveLocked()
	s.mu.Unlock()
	Changed.Emit(s.c, struct{}{})
	return err
}

// Get returns a job.
func (s *Service) Get(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// List returns jobs, optionally filtered by owner.
func (s *Service) List(owner string) []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Job
	for _, j := range s.listLocked() {
		if owner == "" || j.Owner == owner {
			out = append(out, j)
		}
	}
	return out
}

// Handle registers the handler for an owner kind (bound to ctx).
func (s *Service) Handle(c *kernel.Context, owner string, h Handler) {
	s.mu.Lock()
	s.handlers[owner] = h
	s.mu.Unlock()
	c.Defer(func() {
		s.mu.Lock()
		delete(s.handlers, owner)
		s.mu.Unlock()
	})
}

// Tick fires every due job (called each second by the plugin; tests call
// it directly with a fake clock).
func (s *Service) Tick(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	var due []*Job
	for _, j := range s.jobs {
		if j.Enabled && !j.NextFire.IsZero() && !now.Before(j.NextFire) {
			due = append(due, j)
		}
	}
	sort.Slice(due, func(i, k int) bool { return due[i].NextFire.Before(due[k].NextFire) })
	for _, j := range due {
		f := Firing{Scheduled: j.NextFire, Key: fmt.Sprintf("%s@%d", j.ID, j.NextFire.Unix())}
		s.deliverLocked(ctx, j, f, now)
		s.plan(j, now)
	}
	if len(due) > 0 {
		_ = s.saveLocked()
	}
	s.mu.Unlock()
	if len(due) > 0 {
		Changed.Emit(s.c, struct{}{})
	}
}

// deliverLocked records the key durably, then calls the handler.
func (s *Service) deliverLocked(ctx context.Context, j *Job, f Firing, now time.Time) {
	if j.LastKey == f.Key {
		return // already delivered
	}
	j.LastKey, j.LastFire = f.Key, now
	j.Fires++
	if err := s.saveLocked(); err != nil {
		j.LastErr = "persist: " + err.Error()
		return
	}
	h := s.handlers[j.Owner]
	if h == nil {
		j.LastErr = "no handler for " + j.Owner
		return
	}
	f.Job = *j
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("handler panic: %v", r)
			}
		}()
		return h(ctx, f)
	}()
	if err != nil {
		j.LastErr = err.Error()
	} else {
		j.LastErr = ""
	}
}

// Trigger fires a job now (manual "run now" or webhook). key may be empty.
func (s *Service) Trigger(ctx context.Context, id, key string, body json.RawMessage) error {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("job %s not found", id)
	}
	if !j.Enabled {
		return errors.New("job is disabled")
	}
	if key == "" {
		key = randID("", 8)
	}
	key = j.ID + "#" + key
	for k, t := range s.seen {
		if now.Sub(t) > 24*time.Hour {
			delete(s.seen, k)
		}
	}
	if _, dup := s.seen[key]; dup {
		return nil // idempotent replay
	}
	s.seen[key] = now
	s.deliverLocked(ctx, j, Firing{Key: key, Scheduled: now, Body: body}, now)
	_ = s.saveLocked()
	go Changed.Emit(s.c, struct{}{})
	if j.LastErr != "" {
		return errors.New(j.LastErr)
	}
	return nil
}

// CheckSecret validates a webhook secret in constant time.
func (s *Service) CheckSecret(id, secret string) bool {
	j, ok := s.Get(id)
	if !ok || !j.IsWebhook() || j.Secret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(j.Secret), []byte(secret)) == 1
}

// Plugin provides the schedule service. Config: {"dir": "${root}"}.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/schedule",
	Description: "Cron / interval / webhook triggers with idempotent delivery.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[struct {
			Dir string `json:"dir"`
		}](c)
		if err != nil {
			return err
		}
		s, err := New(c, cfg.Dir, nil)
		if err != nil {
			return err
		}
		c.Go(func(ctx context.Context) {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					s.Tick(ctx)
				}
			}
		})
		return c.Provide(ServiceKey, s)
	},
})

// WebhookPlugin exposes POST /api/hooks/{id} (authenticated by the hook's
// own secret via the X-Crewbase-Secret header, never the desktop token).
var WebhookPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/webhook",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Webhook triggers on the gateway.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/hooks/{id}", Public: true,
			Validate: func(r *gateway.Request) error {
				if !s.CheckSecret(r.Params["id"], r.HTTP.Header.Get("X-Crewbase-Secret")) {
					return &gateway.Error{Status: 401, Code: "unauthorized", Message: "missing or invalid token"}
				}
				return nil
			},
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				// Detach from the HTTP request: the handler only enqueues.
				err := s.Trigger(context.WithoutCancel(ctx), r.Params["id"], r.HTTP.Header.Get("Idempotency-Key"), r.Body)
				if err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return map[string]bool{"accepted": true}, nil
			}})
		return nil
	},
})
