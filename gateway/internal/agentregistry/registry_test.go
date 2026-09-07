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

func TestAddrLooksUpByID(t *testing.T) {
	r := agentregistry.NewInMemory()
	ctx := context.Background()

	if _, ok := r.Addr("h1"); ok {
		t.Fatal("expected miss for unknown agent")
	}
	_ = r.Register(ctx, "h1", "https://localhost:9091", agentregistry.Capacity{})
	addr, ok := r.Addr("h1")
	if !ok || addr != "https://localhost:9091" {
		t.Fatalf("Addr = %q %v, want the advertise addr", addr, ok)
	}
}

func TestSweepMarksStaleLost(t *testing.T) {
	ctx := context.Background()
	base := time.Now()
	clk := base
	r := agentregistry.NewInMemoryWithClock(func() time.Time { return clk })

	// ready
	_ = r.Register(ctx, "h1", "addr", agentregistry.Capacity{})
	if _, err := r.Pick(ctx); err != nil {
		t.Fatalf("expected h1 ready after register: %v", err)
	}

	// ready -> lost via the sweep clock
	r.Sweep(ctx, base.Add(31*time.Second), 30*time.Second)
	if _, err := r.Pick(ctx); err == nil {
		t.Fatal("expected h1 to be lost and unpickable")
	}

	// lost -> ready via a fresh heartbeat, stamped at the injected clock
	clk = base.Add(40 * time.Second)
	_ = r.Heartbeat(ctx, "h1")
	if _, err := r.Pick(ctx); err != nil {
		t.Fatalf("expected revive: %v", err)
	}
	// and the revived agent survives a sweep relative to its new heartbeat
	if lost := r.Sweep(ctx, base.Add(50*time.Second), 30*time.Second); len(lost) != 0 {
		t.Fatalf("expected revived agent to survive sweep, got %v", lost)
	}
}

func TestRestorePreservesLostStatus(t *testing.T) {
	ctx := context.Background()
	r := agentregistry.NewInMemory()
	old := time.Now().Add(-time.Hour)

	r.Restore("h1", "addr", "lost", agentregistry.Capacity{}, old)
	if _, err := r.Pick(ctx); err == nil {
		t.Fatal("a restored lost agent must not be Pick-able")
	}

	// a real heartbeat revives it
	if err := r.Heartbeat(ctx, "h1"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if _, err := r.Pick(ctx); err != nil {
		t.Fatalf("expected restored agent to revive after heartbeat: %v", err)
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
