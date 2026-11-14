// Package scope implements scoped registration: entries are either global or
// belong to one agent. Agent entries shadow global entries with the same
// name; restrictions filter what an agent can see (multiple restrictions
// intersect). Filtered entries are indistinguishable from absent ones.
package scope

import (
	"sort"
	"sync"

	"github.com/hedeqiang/crewbase/kernel"
)

// Filter returns true to keep an entry visible.
type Filter func(name string) bool

type entry[T any] struct {
	value T
	order uint64
}

type restriction struct {
	scope  string
	filter Filter
}

// Registry is a scoped, shadowing registry of named values.
type Registry[T any] struct {
	mu           sync.RWMutex
	global       map[string]*entry[T]
	agents       map[string]map[string]*entry[T]
	restrictions []*restriction
	seq          uint64
	onChange     func()
}

// New creates a registry. onChange (optional) runs after every mutation.
func New[T any](onChange func()) *Registry[T] {
	return &Registry[T]{global: map[string]*entry[T]{}, agents: map[string]map[string]*entry[T]{}, onChange: onChange}
}

func (r *Registry[T]) changed() {
	if r.onChange != nil {
		r.onChange()
	}
}

// Register adds name in ctx's scope. A duplicate name in the same scope
// replaces the earlier one until the later registration is disposed.
func (r *Registry[T]) Register(c *kernel.Context, name string, v T) {
	scope := c.Scope()
	r.mu.Lock()
	r.seq++
	e := &entry[T]{value: v, order: r.seq}
	m := r.global
	if scope != "" {
		if r.agents[scope] == nil {
			r.agents[scope] = map[string]*entry[T]{}
		}
		m = r.agents[scope]
	}
	prev := m[name]
	m[name] = e
	r.mu.Unlock()
	r.changed()
	c.Defer(func() {
		r.mu.Lock()
		if m[name] == e {
			if prev != nil {
				m[name] = prev
			} else {
				delete(m, name)
			}
		}
		r.mu.Unlock()
		r.changed()
	})
}

// Restrict adds a filter for ctx's scope ("" applies to every agent).
func (r *Registry[T]) Restrict(c *kernel.Context, f Filter) {
	x := &restriction{scope: c.Scope(), filter: f}
	r.mu.Lock()
	r.restrictions = append(r.restrictions, x)
	r.mu.Unlock()
	r.changed()
	c.Defer(func() {
		r.mu.Lock()
		for i, y := range r.restrictions {
			if y == x {
				r.restrictions = append(r.restrictions[:i:i], r.restrictions[i+1:]...)
				break
			}
		}
		r.mu.Unlock()
		r.changed()
	})
}

func (r *Registry[T]) allowed(agent, name string) bool {
	for _, x := range r.restrictions {
		if (x.scope == "" || x.scope == agent) && !x.filter(name) {
			return false
		}
	}
	return true
}

// Get resolves name for agent ("" = global view).
func (r *Registry[T]) Get(agent, name string) (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var zero T
	if !r.allowed(agent, name) {
		return zero, false
	}
	if agent != "" {
		if e, ok := r.agents[agent][name]; ok {
			return e.value, true
		}
	}
	if e, ok := r.global[name]; ok {
		return e.value, true
	}
	return zero, false
}

// Named is a resolved entry.
type Named[T any] struct {
	Name  string
	Value T
}

// Visible lists what agent sees, in registration order.
func (r *Registry[T]) Visible(agent string) []Named[T] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	merged := map[string]*entry[T]{}
	for k, e := range r.global {
		merged[k] = e
	}
	if agent != "" {
		for k, e := range r.agents[agent] {
			merged[k] = e
		}
	}
	out := make([]Named[T], 0, len(merged))
	orders := map[string]uint64{}
	for k, e := range merged {
		if r.allowed(agent, k) {
			out = append(out, Named[T]{Name: k, Value: e.value})
			orders[k] = e.order
		}
	}
	sort.Slice(out, func(i, j int) bool { return orders[out[i].Name] < orders[out[j].Name] })
	return out
}
