// Package db owns the connection pool and small helpers over the sqlc-generated
// code in internal/db/gen. Handlers and services use Store.Q for single
// statements and Store.InTx for anything that must be atomic.
package db

import (
	"context"
	"strconv"
	"time"

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

// Options tunes the connection pool. The zero value is valid: every field falls
// back to a pgx default (or, for the timeouts, to a small built-in default), so
// callers that do not care about tuning can pass Options{}.
type Options struct {
	MaxConns         int32
	MinConns         int32
	MaxConnLifetime  time.Duration
	MaxConnIdleTime  time.Duration
	HealthCheck      time.Duration
	ConnectTimeout   time.Duration // bounds the initial connectivity check
	StatementTimeout time.Duration // Postgres statement_timeout applied to every query
}

// Open builds the pool and verifies connectivity. google/uuid support is
// registered on every connection so sqlc's uuid.UUID columns scan and bind
// cleanly, and opts tunes the pool size, connection lifetimes, and a
// per-statement timeout so a single slow query cannot hold a connection open
// indefinitely.
func Open(ctx context.Context, dsn string, opts Options) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		pgxuuid.Register(conn.TypeMap())
		return nil
	}

	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	}
	if opts.MinConns > 0 {
		cfg.MinConns = opts.MinConns
	}
	if opts.MaxConnLifetime > 0 {
		cfg.MaxConnLifetime = opts.MaxConnLifetime
	}
	if opts.MaxConnIdleTime > 0 {
		cfg.MaxConnIdleTime = opts.MaxConnIdleTime
	}
	if opts.HealthCheck > 0 {
		cfg.HealthCheckPeriod = opts.HealthCheck
	}
	// statement_timeout is a server-side guard: any query running longer is
	// aborted, so a stuck statement frees its connection instead of pinning it.
	if opts.StatementTimeout > 0 {
		if cfg.ConnConfig.RuntimeParams == nil {
			cfg.ConnConfig.RuntimeParams = map[string]string{}
		}
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(opts.StatementTimeout.Milliseconds(), 10)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Bound the boot-time connectivity check so a hung database fails fast
	// instead of riding the long-lived application context.
	connectTimeout := opts.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = 10 * time.Second
	}
	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
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
	return s.InTxWith(ctx, func(q *gen.Queries, _ pgx.Tx) error { return fn(q) })
}

// InTxWith is InTx but also exposes the raw pgx.Tx, so a caller can enqueue a
// River job transactionally alongside its DB writes (atomic enqueue).
func (s *Store) InTxWith(ctx context.Context, fn func(*gen.Queries, pgx.Tx) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(gen.New(tx), tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
