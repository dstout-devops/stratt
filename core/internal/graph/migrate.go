package graph

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate brings the graph schema to the latest version. Migrations are
// hand-written SQL run by goose (ADR-0004), embedded so the control plane
// stays a single static binary. goose needs database/sql, so the pool is
// adapted via pgx's stdlib bridge for the duration of the migration run only;
// runtime queries stay native pgx.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("graph: migrations fs: %w", err)
	}
	// A Postgres advisory session lock serializes concurrent Up() calls, so N
	// strattd replicas racing migrations at boot is safe (HA, ADR-0040): the
	// first replica migrates while the rest block, then observe the schema
	// already current. Boring — a goose built-in, no new dependency.
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("graph: migration locker: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, sub, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("graph: migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("graph: migrate up: %w", err)
	}
	return nil
}
