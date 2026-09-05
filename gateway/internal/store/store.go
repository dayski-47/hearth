// Package store owns the gateway's Postgres connection, migrations, and queries.
package store

import (
	"context"
	"embed"
	"errors"

	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool, q: gen.New(pool)}, nil
}

func (s *Store) Queries() *gen.Queries { return s.q }
func (s *Store) Pool() *pgxpool.Pool   { return s.pool }
func (s *Store) Close()                { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Migrate(ctx context.Context) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	db := stdlib.OpenDBFromPool(s.pool)
	defer db.Close()
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return errors.Join(errors.New("store: migrate"), err)
	}
	return nil
}
