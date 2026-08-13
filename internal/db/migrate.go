package db

import (
	"context"
	"database/sql"
	"embed"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx database/sql driver for goose
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies all pending goose migrations against the given DSN. It is
// idempotent: the baseline uses IF NOT EXISTS so it is a safe no-op on a
// database that already has the tables.
func Migrate(dsn string) error {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	// WithAllowMissing lets goose apply out-of-order migrations. Neon is seeded
	// from dumps that can be ahead of the goose sequence, so a migration added
	// later with a lower version number (e.g. 0027) shows up as "missing" below
	// the current DB version. Every migration is written with IF NOT EXISTS, so
	// applying a missing one out of order is a safe no-op if it was already run.
	return goose.Up(sqlDB, "migrations", goose.WithAllowMissing())
}

// MigrateRiver applies River's own schema (river_job, river_leader, ...) against
// the DSN. River is a third migrator alongside goose and Better Auth; each owns a
// disjoint set of tables, so running all three in sequence is safe and idempotent.
// Run this after Migrate.
func MigrateRiver(ctx context.Context, dsn string) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return err
}
