// Package store owns the gateway's Postgres connection, migrations, and queries.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"

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

// gooseSlogLogger adapts goose's Logger interface onto a *slog.Logger so that
// migration progress is emitted as structured JSON like the rest of the gateway,
// rather than goose's default plaintext lines on stdout.
type gooseSlogLogger struct{ l *slog.Logger }

func (g gooseSlogLogger) Printf(format string, v ...any) {
	g.l.Info(trimNewline(fmt.Sprintf(format, v...)), "source", "goose")
}

func (g gooseSlogLogger) Fatalf(format string, v ...any) {
	msg := trimNewline(fmt.Sprintf(format, v...))
	g.l.Error(msg, "source", "goose")
	panic("goose: " + msg)
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func (s *Store) Migrate(ctx context.Context, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(gooseSlogLogger{l: logger})
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
