package slicing

// River job definition + a thin transactional enqueuer. The HTTP layer inserts a
// SliceArgs job in the *same pgx transaction* as the design and slice_job rows, so
// a design can never be created without its slice being queued (and vice versa).
// The Go worker (cmd/sliceworker) consumes these jobs. River's river_job table is
// only the transport; slice_jobs stays the domain record (attempt/status/metrics).

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// QueueName is the River queue slice jobs are routed to.
const QueueName = "slice"

// sliceMaxAttempts caps River's automatic retries before the job is marked failed.
const sliceMaxAttempts = 3

// SliceArgs is everything the worker needs to slice one design: the domain job and
// design ids, the brand (for pricing), the STL object key, and the slice specs.
type SliceArgs struct {
	JobID       uuid.UUID `json:"job_id"`
	DesignID    uuid.UUID `json:"design_id"`
	BrandSlug   string    `json:"brand_slug"`
	StlKey      string    `json:"stl_key"`
	Material    string    `json:"material"`
	Quality     string    `json:"quality"`
	UnitsPerBed int       `json:"units_per_bed"`
	InfillPct   float64   `json:"infill_pct"`
}

// Kind is River's stable job type name; it must not change once jobs exist.
func (SliceArgs) Kind() string { return "slice" }

// NewInsertOnlyClient builds a River client that can only enqueue: no queues and
// no workers, so it never consumes jobs and is never Start()ed. The API process
// uses it to insert slice jobs transactionally; the worker process builds its own
// consuming client (see cmd/sliceworker).
func NewInsertOnlyClient(pool *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
	return river.NewClient(riverpgxv5.New(pool), &river.Config{})
}

// Enqueuer inserts slice jobs into River. It wraps an insert-only River client
// (one built with no queues/workers), so the API process can enqueue without
// running any workers itself.
type Enqueuer struct {
	client *river.Client[pgx.Tx]
}

// NewEnqueuer wraps an insert-only River client.
func NewEnqueuer(client *river.Client[pgx.Tx]) *Enqueuer { return &Enqueuer{client: client} }

// EnqueueTx inserts a slice job on the given transaction. Commit the tx to make
// the job visible to workers; roll back and the job never existed - atomic enqueue.
func (e *Enqueuer) EnqueueTx(ctx context.Context, tx pgx.Tx, args SliceArgs) error {
	_, err := e.client.InsertTx(ctx, tx, args, &river.InsertOpts{
		Queue:       QueueName,
		MaxAttempts: sliceMaxAttempts,
	})
	return err
}
