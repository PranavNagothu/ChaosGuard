// Package store provides database and cache adapters for ChaosGuard.
//
// Postgres implements three interfaces simultaneously:
//   - policy.DBClient  (QueryContext → policy.Rows, QueryRowContext → policy.RowScanner, ExecContext)
//   - audit.DBClient   (QueryContext → audit.Rows, ExecContext)
//
// Because both policy.Rows and audit.Rows define the same method set
// {Next() bool; Scan(...) error; Close() error}, and *sql.Rows satisfies both,
// we return *sql.Rows directly and rely on Go's structural typing.
package store

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/lib/pq"
	"github.com/pnagothu/chaosguard/internal/policy"
)

// Postgres wraps *sql.DB and satisfies policy.DBClient and audit.DBClient.
type Postgres struct {
	db *sql.DB
}

// NewPostgres opens and pings a PostgreSQL connection.
func NewPostgres(dsn string) (*Postgres, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening postgres: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	return &Postgres{db: db}, nil
}

// Close closes the underlying connection pool.
func (p *Postgres) Close() error {
	return p.db.Close()
}

// Migrate runs idempotent DDL migrations.
func (p *Postgres) Migrate() error {
	_, err := p.db.Exec(`
		CREATE TABLE IF NOT EXISTS policies (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL,
			service_id  TEXT NOT NULL,
			enabled     BOOLEAN NOT NULL DEFAULT true,
			spec        JSONB NOT NULL,
			created_at  TIMESTAMPTZ NOT NULL,
			updated_at  TIMESTAMPTZ NOT NULL,
			expires_at  TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS idx_policies_service_id ON policies(service_id);

		CREATE TABLE IF NOT EXISTS audit_events (
			id          TEXT PRIMARY KEY,
			type        TEXT NOT NULL,
			service_id  TEXT NOT NULL,
			policy_id   TEXT,
			actor       TEXT NOT NULL,
			payload     JSONB,
			created_at  TIMESTAMPTZ NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_audit_service_id ON audit_events(service_id);
		CREATE INDEX IF NOT EXISTS idx_audit_created_at ON audit_events(created_at DESC);
	`)
	return err
}

// QueryContext executes a query. *sql.Rows satisfies both policy.Rows and audit.Rows.
func (p *Postgres) QueryContext(ctx context.Context, query string, args ...interface{}) (policy.Rows, error) {
	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// QueryRowContext executes a single-row query. *sql.Row satisfies policy.RowScanner.
func (p *Postgres) QueryRowContext(ctx context.Context, query string, args ...interface{}) policy.RowScanner {
	return p.db.QueryRowContext(ctx, query, args...)
}

// ExecContext executes a statement.
func (p *Postgres) ExecContext(ctx context.Context, query string, args ...interface{}) error {
	_, err := p.db.ExecContext(ctx, query, args...)
	return err
}
