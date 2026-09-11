package idle_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/activity"
	"github.com/dayski-47/hearth/gateway/internal/idle"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/jackc/pgx/v5/pgtype"
)

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	u, err := store.ParseUUID(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return u
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeStore struct {
	rows    []gen.Workspace
	listErr error
}

func (f fakeStore) ListRunningWorkspaces(context.Context) ([]gen.Workspace, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.rows, nil
}

type fakeStopper struct {
	stopped []pgtype.UUID
	calls   int
	stopErr error
}

func (f *fakeStopper) StopIdle(_ context.Context, id pgtype.UUID) (gen.Workspace, error) {
	f.calls++
	if f.stopErr != nil {
		return gen.Workspace{}, f.stopErr
	}
	f.stopped = append(f.stopped, id)
	return gen.Workspace{ID: id, State: "stopped"}, nil
}

func TestSweepSeedsAnUnseenWorkspaceInsteadOfStoppingIt(t *testing.T) {
	id := mustUUID(t, "11111111-1111-1111-1111-111111111111")
	st := fakeStore{rows: []gen.Workspace{{ID: id, State: "running"}}}
	stop := &fakeStopper{}
	tr := activity.NewTracker()
	now := time.Now()

	if err := idle.Sweep(context.Background(), idle.Deps{
		Store: st, Tracker: tr, Stop: stop, Timeout: time.Minute, Logger: testLogger(),
	}, now); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(stop.stopped) != 0 {
		t.Fatalf("stopped = %v, want none on first sight", stop.stopped)
	}
	if _, ok := tr.IdleFor(store.UUIDString(id), now); !ok {
		t.Fatal("expected Sweep to seed the tracker on first sight")
	}
}

func TestSweepStopsAWorkspacePastItsTimeout(t *testing.T) {
	id := mustUUID(t, "11111111-1111-1111-1111-111111111111")
	st := fakeStore{rows: []gen.Workspace{{ID: id, State: "running"}}}
	stop := &fakeStopper{}
	start := time.Now()
	tr := activity.NewTrackerWithClock(func() time.Time { return start })
	tr.Touch(store.UUIDString(id))

	past := start.Add(31 * time.Minute)
	if err := idle.Sweep(context.Background(), idle.Deps{
		Store: st, Tracker: tr, Stop: stop, Timeout: 30 * time.Minute, Logger: testLogger(),
	}, past); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(stop.stopped) != 1 || store.UUIDString(stop.stopped[0]) != store.UUIDString(id) {
		t.Fatalf("stopped = %v, want [%s]", stop.stopped, store.UUIDString(id))
	}
}

func TestSweepLeavesAWorkspaceWithinItsTimeout(t *testing.T) {
	id := mustUUID(t, "11111111-1111-1111-1111-111111111111")
	st := fakeStore{rows: []gen.Workspace{{ID: id, State: "running"}}}
	stop := &fakeStopper{}
	start := time.Now()
	tr := activity.NewTrackerWithClock(func() time.Time { return start })
	tr.Touch(store.UUIDString(id))

	soon := start.Add(5 * time.Minute)
	if err := idle.Sweep(context.Background(), idle.Deps{
		Store: st, Tracker: tr, Stop: stop, Timeout: 30 * time.Minute, Logger: testLogger(),
	}, soon); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(stop.stopped) != 0 {
		t.Fatalf("stopped = %v, want none", stop.stopped)
	}
}

func TestSweepReturnsAListError(t *testing.T) {
	wantErr := errors.New("boom")
	st := fakeStore{listErr: wantErr}
	if err := idle.Sweep(context.Background(), idle.Deps{
		Store: st, Tracker: activity.NewTracker(), Stop: &fakeStopper{}, Timeout: time.Minute, Logger: testLogger(),
	}, time.Now()); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestSweepContinuesPastAStopFailure(t *testing.T) {
	idA := mustUUID(t, "11111111-1111-1111-1111-111111111111")
	idB := mustUUID(t, "22222222-2222-2222-2222-222222222222")
	st := fakeStore{rows: []gen.Workspace{{ID: idA, State: "running"}, {ID: idB, State: "running"}}}
	stop := &fakeStopper{stopErr: errors.New("agent unreachable")}
	start := time.Now()
	tr := activity.NewTrackerWithClock(func() time.Time { return start })
	tr.Touch(store.UUIDString(idA))
	tr.Touch(store.UUIDString(idB))

	past := start.Add(time.Hour)
	if err := idle.Sweep(context.Background(), idle.Deps{
		Store: st, Tracker: tr, Stop: stop, Timeout: 30 * time.Minute, Logger: testLogger(),
	}, past); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if stop.calls != 2 {
		t.Fatalf("StopIdle calls = %d, want 2 (a failure must not stop the pass)", stop.calls)
	}
}
