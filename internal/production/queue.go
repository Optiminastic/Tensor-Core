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
}

func NewJobCreationEnqueuer(client *river.Client[pgx.Tx]) *JobCreationEnqueuer {
	return &JobCreationEnqueuer{client: client}
}

// EnqueueTx inserts a job-creation trigger on the given transaction - commit
// to make it visible to the worker, roll back and it never existed. Called
// from the webhook handler in the same transaction as the order upsert, so
// "order arrives" and "jobs will be created" are atomic: sync never succeeds
// while silently failing to schedule job creation.
func (e *JobCreationEnqueuer) EnqueueTx(ctx context.Context, tx pgx.Tx, args CreateJobsArgs) error {
	_, err := e.client.InsertTx(ctx, tx, args, &river.InsertOpts{
		Queue:       JobCreationQueueName,
		MaxAttempts: jobCreationMaxAttempts,
	})
	return err
}

// BatchPlanEnqueuer inserts debounced PlanBatchesArgs triggers into River.
type BatchPlanEnqueuer struct {
	client   *river.Client[pgx.Tx]
	debounce time.Duration
}

// NewBatchPlanEnqueuer builds an enqueuer that collapses triggers landing
// within debounce of each other into a single run - shared with the periodic
// tick registered in cmd/productionworker/main.go so a scheduled tick and an
// event trigger arriving close together never double-fire (see
// PeriodicBatchPlanConstructor).
func NewBatchPlanEnqueuer(client *river.Client[pgx.Tx], debounce time.Duration) *BatchPlanEnqueuer {
	return &BatchPlanEnqueuer{client: client, debounce: debounce}
}

// Enqueue schedules a replan, deduplicated against any run already pending
// within the debounce window - a burst of jobs becoming batchable in the same
// few seconds (e.g. a big order finishing personalisation) collapses onto one
// replan instead of one per job. Not transactional: unlike job creation, a
// missed or delayed replan just means the next trigger catches the same work,
// so there's nothing to roll back for.
func (e *BatchPlanEnqueuer) Enqueue(ctx context.Context) error {
	_, err := e.client.Insert(ctx, PlanBatchesArgs{}, &river.InsertOpts{
		Queue:      BatchPlanQueueName,
		UniqueOpts: river.UniqueOpts{ByPeriod: e.debounce},
	})
	return err
}

// PeriodicBatchPlanConstructor is the river.PeriodicJobConstructor for the
// periodic replan tick (see cmd/productionworker/main.go) - uses the same
// debounce as Enqueue so the periodic tick and an event trigger landing
// close together collapse into one run via River's own ByPeriod uniqueness,
// rather than each inserting a separate job.
func PeriodicBatchPlanConstructor(debounce time.Duration) func() (river.JobArgs, *river.InsertOpts) {
	return func() (river.JobArgs, *river.InsertOpts) {
		return PlanBatchesArgs{}, &river.InsertOpts{
			Queue:      BatchPlanQueueName,
			UniqueOpts: river.UniqueOpts{ByPeriod: debounce},
		}
	}
}
