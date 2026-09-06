package auth

import (
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
