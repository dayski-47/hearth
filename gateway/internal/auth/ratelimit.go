package auth

import (
	"sync"
	"time"
)

// loginLimiter is a fixed-window per-key limiter used only to slow down
// password guessing on the login route: at most limit hits per window.
type loginLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	now    func() time.Time
	hits   map[string]*hitWindow
}

type hitWindow struct {
	start time.Time
	count int
}

func newLoginLimiter(limit int, window time.Duration) *loginLimiter {
	return &loginLimiter{limit: limit, window: window, now: time.Now, hits: map[string]*hitWindow{}}
}

func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	w, ok := l.hits[key]
	if !ok || now.Sub(w.start) >= l.window {
		l.hits[key] = &hitWindow{start: now, count: 1}
		return true
	}
	w.count++
	return w.count <= l.limit
}

func (l *loginLimiter) prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, w := range l.hits {
		if now.Sub(w.start) >= l.window {
			delete(l.hits, k)
		}
	}
}
