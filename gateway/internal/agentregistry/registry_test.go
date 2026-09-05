package agentregistry_test

import (
	"context"
	"testing"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/agentregistry"
)

func TestPickAfterRegister(t *testing.T) {
	r := agentregistry.NewInMemory()
	ctx := context.Background()
	if _, err := r.Pick(ctx); err == nil {
		t.Fatal("expected error with no agents")
	}
	_ = r.Register(ctx, "h1", "https://localhost:9091", agentregistry.Capacity{CPUMillis: 4000})
	a, err := r.Pick(ctx)
	if err != nil || a.ID != "h1" {
		t.Fatalf("pick: %v %+v", err, a)
	}
}

func TestSweepMarksStaleLost(t *testing.T) {
	r := agentregistry.NewInMemory()
	ctx := context.Background()
	_ = r.Register(ctx, "h1", "addr", agentregistry.Capacity{})
	base := time.Now()
	r.Sweep(ctx, base.Add(31*time.Second), 30*time.Second)
	if _, err := r.Pick(ctx); err == nil {
		t.Fatal("expected h1 to be lost and unpickable")
	}
	// a fresh heartbeat revives it
	_ = r.Heartbeat(ctx, "h1")
	if _, err := r.Pick(ctx); err != nil {
		t.Fatalf("expected revive: %v", err)
	}
}

func TestSweepReturnsLostIDs(t *testing.T) {
	r := agentregistry.NewInMemory()
	ctx := context.Background()
	_ = r.Register(ctx, "h1", "addr", agentregistry.Capacity{})
	base := time.Now()
	lost := r.Sweep(ctx, base.Add(31*time.Second), 30*time.Second)
	if len(lost) != 1 || lost[0] != "h1" {
		t.Fatalf("expected [h1], got %v", lost)
	}
	// idempotent: already lost, not reported again
	if again := r.Sweep(ctx, base.Add(62*time.Second), 30*time.Second); len(again) != 0 {
		t.Fatalf("expected no re-report, got %v", again)
	}
}
