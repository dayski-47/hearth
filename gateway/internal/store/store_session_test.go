package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	u, err := s.Queries().UpsertUser(ctx, gen.UpsertUserParams{Username: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}

	exp := pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}
	if _, err := s.Queries().CreateSession(ctx, gen.CreateSessionParams{
		ID: "sess-1", UserID: u.ID, ExpiresAt: exp, UserAgent: "test-agent",
	}); err != nil {
		t.Fatal(err)
	}

	row, err := s.Queries().GetSessionWithUser(ctx, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if row.User.Username != "admin" || row.Session.UserAgent != "test-agent" {
		t.Fatalf("unexpected row: %+v", row)
	}

	newExp := pgtype.Timestamptz{Time: time.Now().Add(48 * time.Hour), Valid: true}
	if err := s.Queries().SlideSession(ctx, gen.SlideSessionParams{ID: "sess-1", ExpiresAt: newExp}); err != nil {
		t.Fatal(err)
	}

	if err := s.Queries().DeleteSession(ctx, "sess-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Queries().GetSessionWithUser(ctx, "sess-1"); err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	u, err := s.Queries().UpsertUser(ctx, gen.UpsertUserParams{Username: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	past := pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
	future := pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}
	_, _ = s.Queries().CreateSession(ctx, gen.CreateSessionParams{ID: "old", UserID: u.ID, ExpiresAt: past})
	_, _ = s.Queries().CreateSession(ctx, gen.CreateSessionParams{ID: "new", UserID: u.ID, ExpiresAt: future})

	n, err := s.Queries().DeleteExpiredSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted %d, want 1", n)
	}
	if _, err := s.Queries().GetSessionWithUser(ctx, "new"); err != nil {
		t.Fatalf("live session should remain: %v", err)
	}
}
