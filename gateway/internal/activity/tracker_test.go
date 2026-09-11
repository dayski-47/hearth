package activity_test

import (
	"testing"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/activity"
)

func TestIdleForReportsUntouchedAsNotSeen(t *testing.T) {
	tr := activity.NewTracker()
	if _, ok := tr.IdleFor("a", time.Now()); ok {
		t.Fatal("expected ok = false for an id never touched")
	}
}

func TestTouchThenIdleForMeasuresElapsedTime(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := start
	tr := activity.NewTrackerWithClock(func() time.Time { return clock })

	tr.Touch("a")
	clock = clock.Add(90 * time.Second)

	d, ok := tr.IdleFor("a", clock)
	if !ok {
		t.Fatal("expected ok = true after a touch")
	}
	if d != 90*time.Second {
		t.Fatalf("idle for = %v, want 90s", d)
	}
}

func TestTouchIsPerWorkspace(t *testing.T) {
	tr := activity.NewTracker()
	tr.Touch("a")
	if _, ok := tr.IdleFor("b", time.Now()); ok {
		t.Fatal("expected workspace b to be untouched")
	}
}

func TestNilTrackerIsANoOp(t *testing.T) {
	var tr *activity.Tracker
	tr.Touch("a") // must not panic
	if _, ok := tr.IdleFor("a", time.Now()); ok {
		t.Fatal("expected a nil tracker to report every id as unseen")
	}
}

func TestTouchIsConcurrencySafe(t *testing.T) {
	tr := activity.NewTracker()
	done := make(chan struct{})
	for i := 0; i < 50; i++ {
		go func() {
			tr.Touch("a")
			done <- struct{}{}
		}()
	}
	for i := 0; i < 50; i++ {
		<-done
	}
	if _, ok := tr.IdleFor("a", time.Now()); !ok {
		t.Fatal("expected a touch to have landed")
	}
}
