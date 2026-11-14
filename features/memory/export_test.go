package memory

// BlockIndexing holds the index lock (stalling the worker) until the
// returned func is called.
func (s *Store) BlockIndexing() func() {
	s.mu.Lock()
	return s.mu.Unlock
}

// Dirty reports whether a catch-up pass is pending.
func (s *Store) Dirty() bool { return s.dirty.Load() }
