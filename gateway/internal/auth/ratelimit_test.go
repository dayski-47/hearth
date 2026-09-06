package auth

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiterAllowsUpToLimit(t *testing.T) {
	l := newLoginLimiter(5, time.Minute)
	for i := 0; i < 5; i++ {
		if !l.allow("ip1") {
			t.Fatalf("hit %d should be allowed", i+1)
		}
	}
	if l.allow("ip1") {
		t.Fatal("sixth hit should be blocked")
	}
}

func TestLimiterWindowResets(t *testing.T) {
	l := newLoginLimiter(2, time.Minute)
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	l.allow("ip1")
	l.allow("ip1")
	if l.allow("ip1") {
		t.Fatal("third hit in-window should block")
	}
	now = now.Add(61 * time.Second)
	if !l.allow("ip1") {
		t.Fatal("hit after the window should be allowed")
	}
}

func TestLimiterKeysAreIndependent(t *testing.T) {
	l := newLoginLimiter(1, time.Minute)
	if !l.allow("a") || !l.allow("b") {
		t.Fatal("different keys must not share a budget")
	}
	if l.allow("a") {
		t.Fatal("key a is over budget")
	}
}

func TestLimiterCapsTrackedKeys(t *testing.T) {
	l := newLoginLimiter(5, time.Minute)
	l.maxKeys = 16
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < 500; i++ {
		// Every key is fresh and in-window, so nothing expires on its own.
		l.allow(fmt.Sprintf("key-%d", i))
		now = now.Add(time.Millisecond)
		if len(l.hits) > l.maxKeys {
			t.Fatalf("limiter grew to %d keys, cap is %d", len(l.hits), l.maxKeys)
		}
	}
	if len(l.hits) != l.maxKeys {
		t.Fatalf("expected the limiter to sit at its cap, got %d", len(l.hits))
	}
}

// Eviction must prefer expired windows over live ones.
func TestLimiterEvictsExpiredBeforeLive(t *testing.T) {
	l := newLoginLimiter(5, time.Minute)
	l.maxKeys = 2
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	l.allow("stale")
	now = now.Add(30 * time.Second)
	l.allow("live")
	now = now.Add(31 * time.Second) // stale is past its window, live is not
	l.allow("new")
	if _, ok := l.hits["stale"]; ok {
		t.Fatal("the expired window should have been evicted")
	}
	if _, ok := l.hits["live"]; !ok {
		t.Fatal("a live window was evicted while an expired one remained")
	}
}

func TestLimiterPruneDropsStale(t *testing.T) {
	l := newLoginLimiter(1, time.Minute)
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	l.allow("old")
	now = now.Add(2 * time.Minute)
	l.prune()
	if len(l.hits) != 0 {
		t.Fatalf("stale window not pruned: %d", len(l.hits))
	}
}
