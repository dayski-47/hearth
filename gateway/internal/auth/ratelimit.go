package auth

import (
	"sync"
	"time"
)

// maxLimiterKeys caps how many windows the limiter tracks at once. The key is
// derived from the peer address, so a spoofed source or a botnet can still
// invent keys faster than the hourly prune collects them; the cap keeps that
// bounded at a few hundred kilobytes.
const maxLimiterKeys = 8192

// loginLimiter is a fixed-window per-key limiter used only to slow down
// password guessing on the login route: at most limit hits per window.
type loginLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	maxKeys int
	now     func() time.Time
	hits    map[string]*hitWindow
}

type hitWindow struct {
	start time.Time
	count int
}

func newLoginLimiter(limit int, window time.Duration) *loginLimiter {
	return &loginLimiter{
		limit: limit, window: window, maxKeys: maxLimiterKeys,
		now: time.Now, hits: map[string]*hitWindow{},
	}
}

func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if w, ok := l.hits[key]; ok && now.Sub(w.start) < l.window {
		w.count++
		return w.count <= l.limit
	} else if !ok && len(l.hits) >= l.maxKeys {
		l.makeRoom(now)
	}
	l.hits[key] = &hitWindow{start: now, count: 1}
	return true
}

func (l *loginLimiter) prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dropExpired(l.now())
}

// makeRoom frees a slot for a new key: expired windows first, then the oldest
// surviving one. Caller holds mu.
func (l *loginLimiter) makeRoom(now time.Time) {
	l.dropExpired(now)
	if len(l.hits) < l.maxKeys {
		return
	}
	oldestKey, oldest := "", time.Time{}
	for k, w := range l.hits {
		if oldestKey == "" || w.start.Before(oldest) {
			oldestKey, oldest = k, w.start
		}
	}
	delete(l.hits, oldestKey)
}

// dropExpired deletes every window whose fixed window has elapsed. Caller holds mu.
func (l *loginLimiter) dropExpired(now time.Time) {
	for k, w := range l.hits {
		if now.Sub(w.start) >= l.window {
			delete(l.hits, k)
		}
	}
}
