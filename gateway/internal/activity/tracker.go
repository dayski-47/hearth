// Package activity tracks the most recent gateway-visible traffic for each
// workspace, so the idle auto-stop sweep (see package idle) can tell a quiet
// workspace from a busy one without a new signal from hearth-workspace.
package activity

import (
	"sync"
	"time"
)

// Tracker holds each workspace's last-touched time in process memory. It is
// deliberately not persisted: a gateway restart resets every workspace's idle
// clock to zero rather than reading a stale timestamp. That is what makes the
// idle sweep safe to run right after a restart (see package idle) — nothing
// is stopped in the first pass just because the tracker has no record yet.
type Tracker struct {
	mu   sync.Mutex
	last map[string]time.Time
	now  func() time.Time
}

// NewTracker builds a Tracker using the wall clock.
func NewTracker() *Tracker {
	return NewTrackerWithClock(time.Now)
}

// NewTrackerWithClock is NewTracker with an injectable clock, for tests that
// need to control what "now" a touch records.
func NewTrackerWithClock(now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}
	return &Tracker{last: map[string]time.Time{}, now: now}
}

// Touch records activity for id at the tracker's current time. Safe to call
// on a nil *Tracker (a no-op) so callers that build a Deps without wiring a
// tracker (most existing gateway tests) do not have to construct one just to
// avoid a nil-pointer panic.
func (t *Tracker) Touch(id string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.last[id] = t.now()
}

// IdleFor reports how long id has gone untouched as of at, and whether it has
// ever been touched. A nil *Tracker reports every id as never touched.
func (t *Tracker) IdleFor(id string, at time.Time) (time.Duration, bool) {
	if t == nil {
		return 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	last, ok := t.last[id]
	if !ok {
		return 0, false
	}
	return at.Sub(last), true
}
