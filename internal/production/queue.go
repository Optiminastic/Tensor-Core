package production

// River job definitions for the production pipeline's background stages -
// mirrors internal/slicing/queue.go's shape exactly. Two separate queues so a
// burst of order webhooks never starves batch planning or vice versa:
//   - "production_jobs": Job Creation Worker (Stage 2), one CreateJobsArgs per
//     order, enqueued transactionally by the webhook handler.
//   - "production_batches": Batch Optimizer (Stage 5+), a single debounced
//     PlanBatchesArgs that always replans over everything currently batchable.
//
// Consumed by cmd/productionworker - a separate process from cmd/sliceworker,
// since a stuck Bambu Studio subprocess must never starve job/batch
// processing (very different resource profiles and failure modes).

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// JobCreationQueueName is the River queue order-arrived jobs are routed to.
const JobCreationQueueName = "production_jobs"

// BatchPlanQueueName is the River queue batch-replan triggers are routed to.
const BatchPlanQueueName = "production_batches"

const jobCreationMaxAttempts = 5

// CreateJobsArgs enqueues the Job Creation Worker for one order.
type CreateJobsArgs struct {
	OrderID uuid.UUID `json:"order_id"`
}

// Kind is River's stable job type name; it must not change once jobs exist.
func (CreateJobsArgs) Kind() string { return "create_jobs_from_order" }

// PlanBatchesArgs enqueues one batch-replan pass over everything currently
// batchable. No payload: unlike job creation (one order in, one order's jobs
// out), a replan always considers the full batchable set, so there is nothing
// per-trigger to carry - which is also what makes River's UniqueOpts collapse
// a burst of triggers onto a single pending run (see BatchPlanEnqueuer).
type PlanBatchesArgs struct{}

func (PlanBatchesArgs) Kind() string { return "plan_batches" }

// JobCreationEnqueuer inserts CreateJobsArgs jobs into River. Wraps the same
// insert-only client cmd/api already builds for slicing (see
// slicing.NewInsertOnlyClient) - one such client can enqueue any registered
// Kind, so there's no need for a second, production-specific one.
type JobCreationEnqueuer struct {
	client *river.Client[pgx.Tx]
	queue  string
}

// NewJobCreationEnqueuer wraps an insert-only client. queue is the resolved
// (possibly prefixed) name; empty falls back to the unprefixed default.
func NewJobCreationEnqueuer(client *river.Client[pgx.Tx], queue string) *JobCreationEnqueuer {
	if queue == "" {
		queue = JobCreationQueueName
	}
	return &JobCreationEnqueuer{client: client, queue: queue}
}

// EnqueueTx inserts a job-creation trigger on the given transaction - commit
// to make it visible to the worker, roll back and it never existed. Called
// from the webhook handler in the same transaction as the order upsert, so
// "order arrives" and "jobs will be created" are atomic: sync never succeeds
// while silently failing to schedule job creation.
func (e *JobCreationEnqueuer) EnqueueTx(ctx context.Context, tx pgx.Tx, args CreateJobsArgs) error {
	_, err := e.client.InsertTx(ctx, tx, args, &river.InsertOpts{
		Queue:       e.queue,
		MaxAttempts: jobCreationMaxAttempts,
	})
	return err
}

// BatchPlanEnqueuer inserts debounced PlanBatchesArgs triggers into River.
type BatchPlanEnqueuer struct {
	client   *river.Client[pgx.Tx]
	debounce time.Duration
	queue    string
}

// NewBatchPlanEnqueuer builds an enqueuer that collapses triggers landing
// within debounce of each other into a single run - shared with the periodic
// tick registered in cmd/productionworker/main.go so a scheduled tick and an
// event trigger arriving close together never double-fire (see
// PeriodicBatchPlanConstructor).
func NewBatchPlanEnqueuer(client *river.Client[pgx.Tx], debounce time.Duration, queue string) *BatchPlanEnqueuer {
	if queue == "" {
		queue = BatchPlanQueueName
	}
	return &BatchPlanEnqueuer{client: client, debounce: debounce, queue: queue}
}

// Enqueue schedules a replan, deduplicated against any run already pending
// within the debounce window - a burst of jobs becoming batchable in the same
// few seconds (e.g. a big order finishing personalisation) collapses onto one
// replan instead of one per job. Not transactional: unlike job creation, a
// missed or delayed replan just means the next trigger catches the same work,
// so there's nothing to roll back for.
func (e *BatchPlanEnqueuer) Enqueue(ctx context.Context) error {
	_, err := e.client.Insert(ctx, PlanBatchesArgs{}, &river.InsertOpts{
		Queue:      e.queue,
		UniqueOpts: river.UniqueOpts{ByPeriod: e.debounce},
	})
	return err
}

// PeriodicBatchPlanConstructor is the river.PeriodicJobConstructor for the
// periodic replan tick (see cmd/productionworker/main.go) - uses the same
// debounce as Enqueue so the periodic tick and an event trigger landing
// close together collapse into one run via River's own ByPeriod uniqueness,
// rather than each inserting a separate job.
func PeriodicBatchPlanConstructor(debounce time.Duration, queue string) func() (river.JobArgs, *river.InsertOpts) {
	if queue == "" {
		queue = BatchPlanQueueName
	}
	return func() (river.JobArgs, *river.InsertOpts) {
		return PlanBatchesArgs{}, &river.InsertOpts{
			Queue:      queue,
			UniqueOpts: river.UniqueOpts{ByPeriod: debounce},
		}
	}
}

// DispatchQueueName is the River queue print dispatches are routed to. Separate
// from job creation and batch planning because it is I/O-bound on a link to the
// shop LAN: a slow or flapping tunnel must not hold up order intake.
const DispatchQueueName = "print_dispatch"

const dispatchMaxAttempts = 5

// DispatchPrintArgs enqueues the hand-off of one sliced plate to BamBuddy.
//
// It carries the BATCH id, not a dispatch id, because the two producers - the
// plate slice worker finishing, and an operator retrying by hand - both know the
// batch and neither should have to create the dispatch row first. The worker owns
// creating (or reusing) that row, so there is exactly one place that decides what
// "already dispatched" means. Everything else is read fresh at run time.
type DispatchPrintArgs struct {
	BatchID uuid.UUID `json:"batch_id"`
}

// Kind is River's stable job type name; it must not change once jobs exist.
func (DispatchPrintArgs) Kind() string { return "dispatch_print" }

// DispatchEnqueuer inserts print-dispatch jobs into River.
type DispatchEnqueuer struct {
	client *river.Client[pgx.Tx]
	queue  string
}

// NewDispatchEnqueuer wraps an insert-only client. queue is the resolved
// (possibly prefixed) name; empty falls back to the unprefixed default.
func NewDispatchEnqueuer(client *river.Client[pgx.Tx], queue string) *DispatchEnqueuer {
	if queue == "" {
		queue = DispatchQueueName
	}
	return &DispatchEnqueuer{client: client, queue: queue}
}

// EnqueueTx schedules a dispatch on the given transaction, so the dispatch row
// and the job that acts on it commit together - a dispatch row can never exist
// with nothing scheduled to send it.
func (e *DispatchEnqueuer) EnqueueTx(ctx context.Context, tx pgx.Tx, args DispatchPrintArgs) error {
	_, err := e.client.InsertTx(ctx, tx, args, &river.InsertOpts{
		Queue:       e.queue,
		MaxAttempts: dispatchMaxAttempts,
	})
	return err
}

// SyncDispatchesArgs enqueues one pass over every live print dispatch. No
// payload: a pass always considers the full open set, which is also what lets
// River's ByPeriod uniqueness collapse a backlog into a single run if a pass
// ever takes longer than the interval.
type SyncDispatchesArgs struct{}

// Kind is River's stable job type name; it must not change once jobs exist.
func (SyncDispatchesArgs) Kind() string { return "sync_print_dispatches" }

// PeriodicDispatchSyncConstructor is the river.PeriodicJobConstructor for the
// status poll. It shares the dispatch queue so polling and dispatching never run
// concurrently against the same printer manager.
func PeriodicDispatchSyncConstructor(interval time.Duration, queue string) func() (river.JobArgs, *river.InsertOpts) {
	if queue == "" {
		queue = DispatchQueueName
	}
	return func() (river.JobArgs, *river.InsertOpts) {
		return SyncDispatchesArgs{}, &river.InsertOpts{
			Queue:      queue,
			UniqueOpts: river.UniqueOpts{ByPeriod: interval},
		}
	}
}
