// Package db owns the connection pool and small helpers over the sqlc-generated
// code in internal/db/gen. Handlers and services use Store.Q for single
// statements and Store.InTx for anything that must be atomic.
package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxuuid "github.com/vgarvardt/pgx-google-uuid/v5"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// Store is the database handle: a pgx pool plus a pool-bound query set.
type Store struct {
	Pool *pgxpool.Pool
	Q    *gen.Queries
}

// Open builds the pool and verifies connectivity. google/uuid support is
// registered on every connection so sqlc's uuid.UUID columns scan and bind
// cleanly.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		pgxuuid.Register(conn.TypeMap())
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{Pool: pool, Q: gen.New(pool)}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.Pool.Close() }

// InTx runs fn inside a transaction with a tx-bound query set, committing on
// success and rolling back on any error.
func (s *Store) InTx(ctx context.Context, fn func(*gen.Queries) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(gen.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
