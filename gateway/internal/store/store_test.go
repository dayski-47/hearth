package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func gen_UpsertUserParams(t *testing.T) gen.UpsertUserParams {
	t.Helper()
	return gen.UpsertUserParams{Username: "admin", PasswordHash: "x"}
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("hearth"),
		postgres.WithUsername("hearth"),
		postgres.WithPassword("hearth"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx, nil); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMigrateAndUpsertUser(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := s.Queries().UpsertUser(ctx, gen_UpsertUserParams(t))
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "admin" {
		t.Fatalf("got %q", u.Username)
	}
	got, err := s.Queries().GetUserByUsername(ctx, "admin")
	if err != nil || got.ID != u.ID {
		t.Fatalf("get mismatch: %v", err)
	}
}
