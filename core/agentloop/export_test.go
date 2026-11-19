package agentloop

import "time"

// SetClock moves the time requests are planned at; it returns a restore func.
func SetClock(f func() time.Time) func() {
	old := clock
	clock = f
	return func() { clock = old }
}

// CacheTTL is cacheTTL, for tests.
const CacheTTL = cacheTTL
