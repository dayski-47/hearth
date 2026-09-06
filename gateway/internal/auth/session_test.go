package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/jackc/pgx/v5/pgtype"
)

// fakeQueries is the shared in-memory stand-in for the generated store.
type fakeQueries struct {
	sessions  map[string]gen.Session
	user      gen.User
	slid      int
	deleted   []string
	createErr error
	// now stamps last_seen_at on insert, mirroring the column default.
	now func() time.Time
}

func newFakeQueries() *fakeQueries {
	uid := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	return &fakeQueries{
		sessions: map[string]gen.Session{},
		user:     gen.User{ID: uid, Username: "admin", PasswordHash: "x"},
		now:      time.Now,
	}
}

func (f *fakeQueries) CreateSession(_ context.Context, a gen.CreateSessionParams) (gen.Session, error) {
	if f.createErr != nil {
		return gen.Session{}, f.createErr
	}
	s := gen.Session{
		ID: a.ID, UserID: a.UserID, ExpiresAt: a.ExpiresAt, UserAgent: a.UserAgent,
		LastSeenAt: pgtype.Timestamptz{Time: f.now(), Valid: true},
	}
	f.sessions[a.ID] = s
	return s, nil
}

func (f *fakeQueries) GetSessionWithUser(_ context.Context, id string) (gen.GetSessionWithUserRow, error) {
	s, ok := f.sessions[id]
	if !ok {
		return gen.GetSessionWithUserRow{}, errors.New("not found")
	}
	return gen.GetSessionWithUserRow{Session: s, User: f.user}, nil
}

func (f *fakeQueries) SlideSession(_ context.Context, a gen.SlideSessionParams) error {
	if _, ok := f.sessions[a.ID]; !ok {
		return errors.New("not found")
	}
	f.slid++
	return nil
}

func (f *fakeQueries) DeleteSession(_ context.Context, id string) error {
	delete(f.sessions, id)
	f.deleted = append(f.deleted, id)
	return nil
}

func testManager(t *testing.T, q Queries) *Manager {
	t.Helper()
	m := NewManager(q, []byte("0123456789abcdef0123456789abcdef"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	return m
}

func TestManagerCreateAndAuthenticate(t *testing.T) {
	f := newFakeQueries()
	m := testManager(t, f)
	ctx := context.Background()

	cookie, err := m.Create(ctx, f.user.ID, "ua", netip.MustParseAddr("10.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	u, err := m.Authenticate(ctx, cookie)
	if err != nil || u.Username != "admin" {
		t.Fatalf("authenticate: u=%v err=%v", u, err)
	}
	// The row was just written, so it is already current and costs no UPDATE.
	if f.slid != 0 {
		t.Fatalf("a fresh session should not slide, got %d writes", f.slid)
	}
}

func TestAuthenticateSlidesOnlyWhenStale(t *testing.T) {
	f := newFakeQueries()
	m := testManager(t, f)
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	f.now = func() time.Time { return start }
	m.now = func() time.Time { return start }
	ctx := context.Background()

	cookie, err := m.Create(ctx, f.user.ID, "ua", netip.Addr{})
	if err != nil {
		t.Fatal(err)
	}

	// Well inside the threshold: no write.
	m.now = func() time.Time { return start.Add(slideThreshold - time.Minute) }
	if _, err := m.Authenticate(ctx, cookie); err != nil {
		t.Fatal(err)
	}
	if f.slid != 0 {
		t.Fatalf("expected no slide inside the threshold, got %d", f.slid)
	}

	// Past it: exactly one write.
	m.now = func() time.Time { return start.Add(slideThreshold + time.Minute) }
	if _, err := m.Authenticate(ctx, cookie); err != nil {
		t.Fatal(err)
	}
	if f.slid != 1 {
		t.Fatalf("expected one slide past the threshold, got %d", f.slid)
	}
}

// A row with no last_seen_at is treated as stale so the column gets populated.
func TestAuthenticateSlidesWhenLastSeenUnset(t *testing.T) {
	f := newFakeQueries()
	m := testManager(t, f)
	ctx := context.Background()
	cookie, err := m.Create(ctx, f.user.ID, "ua", netip.Addr{})
	if err != nil {
		t.Fatal(err)
	}
	for id, s := range f.sessions {
		s.LastSeenAt = pgtype.Timestamptz{}
		f.sessions[id] = s
	}
	if _, err := m.Authenticate(ctx, cookie); err != nil {
		t.Fatal(err)
	}
	if f.slid != 1 {
		t.Fatalf("expected a slide for an unset last_seen_at, got %d", f.slid)
	}
}

func TestAuthenticateRejectsBadSignature(t *testing.T) {
	f := newFakeQueries()
	m := testManager(t, f)
	if _, err := m.Authenticate(context.Background(), "forged.sig"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("want ErrNoSession, got %v", err)
	}
	if len(f.deleted) != 0 {
		t.Fatal("no query should run for a bad signature")
	}
}

func TestAuthenticateRejectsAndDeletesExpired(t *testing.T) {
	f := newFakeQueries()
	m := testManager(t, f)
	m.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	ctx := context.Background()

	cookie, _ := m.Create(ctx, f.user.ID, "ua", netip.Addr{})
	m.now = func() time.Time { return time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) } // well past TTL

	if _, err := m.Authenticate(ctx, cookie); !errors.Is(err, ErrNoSession) {
		t.Fatalf("want ErrNoSession, got %v", err)
	}
	if len(f.deleted) != 1 {
		t.Fatalf("expired session should be deleted, deletes=%v", f.deleted)
	}
}

func TestDestroyDeletesSession(t *testing.T) {
	f := newFakeQueries()
	m := testManager(t, f)
	ctx := context.Background()
	cookie, _ := m.Create(ctx, f.user.ID, "ua", netip.Addr{})
	m.Destroy(ctx, cookie)
	if len(f.deleted) != 1 {
		t.Fatalf("destroy should delete once, got %v", f.deleted)
	}
}
