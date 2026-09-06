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
}

func newFakeQueries() *fakeQueries {
	uid := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	return &fakeQueries{
		sessions: map[string]gen.Session{},
		user:     gen.User{ID: uid, Username: "admin", PasswordHash: "x"},
	}
}

func (f *fakeQueries) CreateSession(_ context.Context, a gen.CreateSessionParams) (gen.Session, error) {
	if f.createErr != nil {
		return gen.Session{}, f.createErr
	}
	s := gen.Session{ID: a.ID, UserID: a.UserID, ExpiresAt: a.ExpiresAt, UserAgent: a.UserAgent}
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
	if f.slid != 1 {
		t.Fatalf("expected the session to slide once, got %d", f.slid)
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
