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
	"github.com/riverqueue/river/rivertype"
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

// ModelGenQueueName is where personalised-model renders are routed.
//
// Its own queue for the same reason cmd/sliceworker is its own process: a
// render is a 20-45 second OpenSCAD subprocess, and putting that on the
// job-creation queue would let one plank hold up every other order's jobs.
const ModelGenQueueName = "model_generation"

// modelGenMaxAttempts is deliberately low.
//
// A render fails for reasons retrying cannot fix - a name OpenSCAD cannot set,
// a template bug, a missing font. Three attempts absorb a restart or a
// transient disk error; past that the job is held with the reason so a person
// sees it, rather than burning a worker slot on a 45s render that will fail
// identically every time.
const modelGenMaxAttempts = 3

// GenerateModelArgs renders the personalised model for one production job.
//
// Keyed on the JOB, not the order: an order can hold several planks with
// different names, and each needs its own file. The worker reads the names back
// from the order's line items, because the job row only carries them joined
// into a single "A & B" string.
type GenerateModelArgs struct {
	JobID uuid.UUID `json:"job_id"`
}

// Kind is River's stable job type name; it must not change once jobs exist.
func (GenerateModelArgs) Kind() string { return "generate_personalised_model" }

// InsertOpts routes the render to its own queue and caps its attempts.
func (GenerateModelArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: ModelGenQueueName, MaxAttempts: modelGenMaxAttempts}
}

// FleetSyncQueueName keeps the fleet refresh off the batch-plan queue. A
// refresh is 1 + N sequential calls to a printer host that may be asleep, and
// it must never occupy the single batch-plan slot while it waits.
const FleetSyncQueueName = "fleet_sync"

// SyncFleetArgs refreshes the machines table from BambuBuddy. No payload: the
// refresh always covers the whole fleet, which is what lets River's UniqueOpts
// collapse a periodic tick and a manual sync landing together into one run.
type SyncFleetArgs struct{}

func (SyncFleetArgs) Kind() string { return "sync_fleet" }

// OrderSyncQueueName keeps the Shopify order pull off every other queue.
//
// Its own queue because it is long and external: hundreds of orders, each a
// round trip to Shopify and a write to the database. Sharing a queue would let
// one sync hold up batch planning for minutes.
const OrderSyncQueueName = "order_sync"

// SyncOrdersArgs pulls a brand's orders from Shopify and imports them.
//
// Carries the brand slug, not the shop domain or the access token: a River job
// is a row in the database that outlives the request, and a Shopify token is a
// credential that has no business sitting in one. The worker resolves it from
// the connection when it runs.
type SyncOrdersArgs struct {
	BrandSlug string `json:"brand_slug"`
}

func (SyncOrdersArgs) Kind() string { return "sync_shopify_orders" }

// InsertOpts routes the sync to its own queue and deduplicates by brand: two
// people pressing Sync within the window get one pull, not two competing ones
// writing the same orders.
func (a SyncOrdersArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: OrderSyncQueueName,
		UniqueOpts: river.UniqueOpts{
			ByArgs:   true,
			ByPeriod: time.Minute,
			// River requires the "required" states be present; pending and
			// retryable come along with the three that matter here.
			ByState: []rivertype.JobState{
				rivertype.JobStatePending, rivertype.JobStateScheduled,
				rivertype.JobStateAvailable, rivertype.JobStateRunning,
				rivertype.JobStateRetryable,
			},
		},
	}
}

// DispatchBatchQueueName keeps batch dispatch off the planning queue.
//
// Its own queue because the two block each other otherwise: a dispatch pass
// spends its time uploading multi-megabyte plates to BambuBuddy, and a replan
// waiting behind that is a replan not happening.
const DispatchBatchQueueName = "batch_dispatch"

// DispatchBatchesArgs enqueues one pass that walks batches toward a printer -
// approving a Draft, or sending an approved and sliced plate - oldest order
// first. Carries no payload for the same reason PlanBatchesArgs does not: the
// pass always works over whatever is currently ready, so a stale trigger is
// harmless.
type DispatchBatchesArgs struct{}

func (DispatchBatchesArgs) Kind() string { return "dispatch_batches" }

// PeriodicDispatchConstructor schedules the dispatch pass, deduped over period
// the same way the other two are - so K worker replicas produce one pass per
// period between them, not K. K passes would each approve up to the per-run cap,
// which is how a backlog turns into every bed committed at once.
func PeriodicDispatchConstructor(period time.Duration) func() (river.JobArgs, *river.InsertOpts) {
	return func() (river.JobArgs, *river.InsertOpts) {
		return DispatchBatchesArgs{}, &river.InsertOpts{
			Queue:      DispatchBatchQueueName,
			UniqueOpts: river.UniqueOpts{ByPeriod: period},
		}
	}
}

// PeriodicFleetSyncConstructor builds the periodic insert for the fleet
// refresh, deduped over period the same way PeriodicBatchPlanConstructor is -
// so K worker replicas produce one refresh per period between them, not K.
func PeriodicFleetSyncConstructor(period time.Duration) func() (river.JobArgs, *river.InsertOpts) {
	return func() (river.JobArgs, *river.InsertOpts) {
		return SyncFleetArgs{}, &river.InsertOpts{
			Queue:      FleetSyncQueueName,
			UniqueOpts: river.UniqueOpts{ByPeriod: period},
		}
	}
}

// PeriodicOrderSyncConstructor builds the periodic pull of every connected
// store's orders.
//
// The empty BrandSlug is the worker's "all brands". Deduped over period like
// the other periodic inserts, so K worker replicas produce one pull between
// them rather than K stampeding the same Shopify store.
//
// This is the first thing that imports orders without somebody pressing a
// button. It exists because pressing the button was the ONLY path, so an order
// placed overnight simply was not in Tensor until an operator arrived and
// noticed.
func PeriodicOrderSyncConstructor(period time.Duration) func() (river.JobArgs, *river.InsertOpts) {
	return func() (river.JobArgs, *river.InsertOpts) {
		return SyncOrdersArgs{}, &river.InsertOpts{
			Queue:      OrderSyncQueueName,
			UniqueOpts: river.UniqueOpts{ByPeriod: period},
		}
	}
}

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

// ModelGenEnqueuer inserts GenerateModelArgs renders into River.
type ModelGenEnqueuer struct {
	client *river.Client[pgx.Tx]
}

func NewModelGenEnqueuer(client *river.Client[pgx.Tx]) *ModelGenEnqueuer {
	return &ModelGenEnqueuer{client: client}
}

// Enqueue schedules a render for one job.
//
// Not transactional, unlike job creation. The job it renders for already
// exists and carries issue_reason = 'stl_missing' until the model lands, so a
// render that is never scheduled leaves a visibly incomplete job rather than a
// silently lost one - and re-enqueuing is safe, since the render is
// idempotent from the job's point of view.
func (e *ModelGenEnqueuer) Enqueue(ctx context.Context, jobID uuid.UUID) error {
	_, err := e.client.Insert(ctx, GenerateModelArgs{JobID: jobID}, nil)
	return err
}

// OrderSyncEnqueuer inserts SyncOrdersArgs jobs into River.
type OrderSyncEnqueuer struct{ client *river.Client[pgx.Tx] }

// NewOrderSyncEnqueuer builds an enqueuer for Shopify order pulls.
func NewOrderSyncEnqueuer(client *river.Client[pgx.Tx]) *OrderSyncEnqueuer {
	return &OrderSyncEnqueuer{client: client}
}

// Enqueue schedules a pull for one brand. SyncOrdersArgs.InsertOpts dedupes by
// brand, so pressing Sync twice produces one run rather than two writing the
// same orders over each other.
func (e *OrderSyncEnqueuer) Enqueue(ctx context.Context, brandSlug string) error {
	if e == nil || e.client == nil {
		return nil
	}
	_, err := e.client.Insert(ctx, SyncOrdersArgs{BrandSlug: brandSlug}, nil)
	return err
}

// DispatchEnqueuer inserts dispatch passes into River.
//
// The pass also runs on a periodic tick, but a tick only fires on the River
// leader - and the leader may be another process entirely, possibly an older
// build with no such tick registered. A bed that has just been planned and
// locked should not wait on that: this is the event trigger that walks it to a
// printer as soon as it exists.
type DispatchEnqueuer struct {
	client   *river.Client[pgx.Tx]
	debounce time.Duration
}

// NewDispatchEnqueuer builds an enqueuer that collapses triggers landing within
// debounce of each other into one pass - a plan run that creates six beds must
// produce one dispatch pass, not six racing to approve the same bed.
func NewDispatchEnqueuer(client *river.Client[pgx.Tx], debounce time.Duration) *DispatchEnqueuer {
	return &DispatchEnqueuer{client: client, debounce: debounce}
}

// Enqueue schedules a dispatch pass.
func (e *DispatchEnqueuer) Enqueue(ctx context.Context) error {
	if e == nil || e.client == nil {
		return nil
	}
	_, err := e.client.Insert(ctx, DispatchBatchesArgs{}, &river.InsertOpts{
		Queue:      DispatchBatchQueueName,
		UniqueOpts: river.UniqueOpts{ByPeriod: e.debounce},
	})
	return err
}
