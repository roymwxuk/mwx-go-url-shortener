package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore is a Store backed by PostgreSQL (local Docker or AWS RDS/Aurora).
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	s := &PostgresStore{pool: pool}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// migrate applies embedded SQL files in order. The files are idempotent, which
// is enough for now; switch to a migration tool (goose, golang-migrate) as the
// schema grows.
func (s *PostgresStore) migrate(ctx context.Context) error {
	files, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		sql, err := migrationsFS.ReadFile(f)
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	return nil
}

func (s *PostgresStore) Create(ctx context.Context, l Link) (Link, error) {
	err := s.pool.QueryRow(ctx,
		`INSERT INTO links (code, url) VALUES ($1, $2) RETURNING clicks, created_at`,
		l.Code, l.URL,
	).Scan(&l.Clicks, &l.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return Link{}, ErrConflict
		}
		return Link{}, err
	}
	return l, nil
}

func (s *PostgresStore) Get(ctx context.Context, code string) (Link, error) {
	var l Link
	err := s.pool.QueryRow(ctx,
		`SELECT code, url, clicks, created_at FROM links WHERE code = $1`, code,
	).Scan(&l.Code, &l.URL, &l.Clicks, &l.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Link{}, ErrNotFound
	}
	return l, err
}

func (s *PostgresStore) Resolve(ctx context.Context, code string) (string, error) {
	var url string
	err := s.pool.QueryRow(ctx,
		`UPDATE links SET clicks = clicks + 1 WHERE code = $1 RETURNING url`, code,
	).Scan(&url)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return url, err
}

func (s *PostgresStore) Delete(ctx context.Context, code string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM links WHERE code = $1`, code)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
func (s *PostgresStore) Close()                         { s.pool.Close() }
