package db

import (
	"database/sql"
	"embed"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx database/sql driver for goose
	"github.com/pressly/goose/v3"
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
	return goose.Up(sqlDB, "migrations")
}
