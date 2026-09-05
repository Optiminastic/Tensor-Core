// Package workerset builds and starts the production pipeline's River workers.
//
// It exists so the pipeline is not a separate thing you have to remember to
// start. Job creation, model rendering, batch planning, dispatch and the Shopify
// order pull all used to live only in cmd/productionworker, so starting the API
// alone produced a system that looked healthy and did nothing: orders imported,
// jobs were created - and then sat there, because nothing consumed the queue.
// The same wiring now runs inside cmd/api by default (see Start's doc comment),
// with cmd/productionworker kept for deployments that want the pipeline on its
// own host.
//
// One definition, two callers: a worker registered here is registered in both,
// so the two processes cannot drift into consuming different sets of jobs.
package workerset

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/httpapi"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

// defaultModelGenConcurrency is how many planks render at once when
// MODEL_GEN_CONCURRENCY is unset.
//
// Four, because a render is a single-threaded OpenSCAD process and this leaves
// most of a typical host for everything else. Raise it on a bigger machine when
// re-rendering the whole catalogue; the work is embarrassingly parallel.
const defaultModelGenConcurrency = 4

// Runner is a started worker set. Stop it to drain in-flight jobs.
type Runner struct {
	client *river.Client[pgx.Tx]
}

// Stop drains and shuts the workers down. Safe on a nil Runner, so a caller
// that never started one can defer this unconditionally.
func (r *Runner) Stop(ctx context.Context) error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Stop(ctx)
}

// Start registers every production worker, attaches the enqueuers to server and
// starts consuming.
//
// The enqueuers matter as much as the workers: they are what make an event
// trigger the next stage. Without them s.batchEnqueuer and friends stay nil,
// every trigger hits its nil guard and returns silently, and the pipeline
// degrades to whatever the periodic ticks happen to catch. That is why this
// attaches them before Start rather than leaving it to each caller.
//
// A consuming client can Insert as well as Work, so the same client serves both
// roles - except for slicing, which deliberately has no worker registered here
// (no Bambu Studio in this process) and so needs an insert-only client of its
// own; a consuming client rejects an insert whose kind it cannot work.
func Start(
	ctx context.Context, server *httpapi.Server, store *db.Store,
	cfg config.Settings, logger *slog.Logger,
) (*Runner, error) {
	concurrency := cfg.ProductionConcurrency
	if concurrency < 1 {
		concurrency = 1
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, httpapi.NewJobCreationWorker(server, logger))
	river.AddWorker(workers, httpapi.NewBatchPlanWorker(server, logger))

	// Rendering personalised models. Registered unconditionally so a render
	// enqueued while OpenSCAD was installed does not sit unclaimed if the
	// binary later goes missing - the worker reports that as a job failure
	// with a reason, which is visible, rather than as a job nothing consumes.
	river.AddWorker(workers, httpapi.NewModelGenWorker(server, logger))

	// Walking approved and sliced batches onto printers. Registered
	// unconditionally, like the renderer: the pass itself is a no-op when
	// BATCH_AUTO_DISPATCH is off, which is better than leaving a queue nothing
	// consumes if the flag is flipped without a redeploy.
	river.AddWorker(workers, httpapi.NewDispatchWorker(server, logger))

	// Pulling Shopify orders. Registered unconditionally: this is the only
	// path an order takes into Tensor, and a queue nothing consumes would mean
	// pressing Sync reports success and imports nothing.
	river.AddWorker(workers, httpapi.NewOrderSyncWorker(
		server, logger, time.Duration(cfg.OrderSyncTimeoutMinutes)*time.Minute,
	))

	fleetSync := time.Duration(cfg.FleetSyncIntervalSeconds) * time.Second
	if fleetSync > 0 {
		river.AddWorker(workers, httpapi.NewFleetSyncWorker(
			server, logger, time.Duration(cfg.FleetSyncTimeoutMinutes)*time.Minute,
		))
	}

	debounce := time.Duration(cfg.BatchPlanDebounceSeconds) * time.Second
	client, err := river.NewClient(riverpgxv5.New(store.Pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			production.JobCreationQueueName: {MaxWorkers: concurrency},
			production.BatchPlanQueueName:   {MaxWorkers: 1},
			// Its own queue, and one at a time: a refresh waits on a printer
			// host that may be asleep, and must never hold the single
			// batch-plan slot while it does.
			production.FleetSyncQueueName: {MaxWorkers: 1},
			// OpenSCAD is CPU-bound for 20-45 seconds per plank. Bounded rather
			// than unlimited: renders are pure CPU and the box also runs the
			// API, the database and Docker.
			production.ModelGenQueueName: {MaxWorkers: modelGenConcurrency(cfg)},
			// One at a time, and off the planning queue: a pass spends its time
			// uploading multi-megabyte plates over the printer host's link, and
			// two passes at once would race to approve the same bed.
			production.DispatchBatchQueueName: {MaxWorkers: 1},
			// One at a time: a pull is minutes of Shopify round trips and order
			// upserts, and two running together would race to write the same
			// rows for no gain - Shopify is the bottleneck, not Tensor.
			production.OrderSyncQueueName: {MaxWorkers: 1},
		},
		Workers:      workers,
		Logger:       logger,
		PeriodicJobs: periodicJobs(cfg, debounce, fleetSync),
	})
	if err != nil {
		return nil, fmt.Errorf("build river client: %w", err)
	}

	server.EnableProductionQueue(
		production.NewJobCreationEnqueuer(client),
		production.NewBatchPlanEnqueuer(client, debounce),
	)
	server.EnableOrderSync(production.NewOrderSyncEnqueuer(client))
	server.EnableBatchDispatchQueue(production.NewDispatchEnqueuer(client, debounce))
	EnableModelGeneration(server, cfg, production.NewModelGenEnqueuer(client), logger)

	// Slicing needs its own insert-only client: a consuming client validates
	// every insert against its Workers bundle, and no slice worker is
	// registered here, so enqueuing a slice through the client above fails with
	// "job kind is not registered in the client's Workers bundle: slice_batch".
	insertOnly, err := slicing.NewInsertOnlyClient(store.Pool)
	if err != nil {
		return nil, fmt.Errorf("build insert-only river client: %w", err)
	}
	server.EnableSliceEnqueuer(slicing.NewEnqueuer(insertOnly))

	if err := client.Start(ctx); err != nil {
		return nil, fmt.Errorf("start river: %w", err)
	}
	logger.Info("production pipeline running",
		"concurrency", concurrency, "renders", modelGenConcurrency(cfg),
		"auto_dispatch", cfg.BatchAutoDispatch,
		"order_sync_minutes", cfg.OrderSyncIntervalMinutes)
	return &Runner{client: client}, nil
}

// modelGenConcurrency is the configured render concurrency, floored at one so a
// zero or negative setting cannot silently stop rendering altogether.
func modelGenConcurrency(cfg config.Settings) int {
	if cfg.ModelGenConcurrency > 0 {
		return cfg.ModelGenConcurrency
	}
	return defaultModelGenConcurrency
}

// periodicJobs assembles the scheduled work the pipeline owns.
//
// RunOnStart differs per entry, and deliberately. A replan on every deploy would
// be wasted work against an unchanged backlog, and a dispatch pass on every
// deploy would commit filament. A fleet refresh is the opposite: a fresh
// deployment's machines table is empty until something populates it, which is
// exactly the "Machine Management shows nothing" state operators hit. So is the
// order pull: starting up is precisely when you want to know what the store has
// been doing while you were down.
func periodicJobs(cfg config.Settings, debounce, fleetSync time.Duration) []*river.PeriodicJob {
	jobs := []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(time.Duration(cfg.BatchPlanIntervalMinutes)*time.Minute),
			production.PeriodicBatchPlanConstructor(debounce),
			&river.PeriodicJobOpts{RunOnStart: false},
		),
	}
	if cfg.BatchAutoDispatch {
		// Reuses the batch-plan interval: dispatch has nothing to do until a
		// plan has run, so checking more often than the planner produces beds
		// is pure polling.
		jobs = append(jobs, river.NewPeriodicJob(
			river.PeriodicInterval(time.Duration(cfg.BatchPlanIntervalMinutes)*time.Minute),
			production.PeriodicDispatchConstructor(time.Duration(cfg.BatchPlanIntervalMinutes)*time.Minute),
			&river.PeriodicJobOpts{RunOnStart: false},
		))
	}
	if orderSync := time.Duration(cfg.OrderSyncIntervalMinutes) * time.Minute; orderSync > 0 {
		jobs = append(jobs, river.NewPeriodicJob(
			river.PeriodicInterval(orderSync),
			production.PeriodicOrderSyncConstructor(orderSync),
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}
	if fleetSync <= 0 {
		return jobs // FLEET_SYNC_INTERVAL_SECONDS=0 opts out entirely
	}
	return append(jobs, river.NewPeriodicJob(
		river.PeriodicInterval(fleetSync),
		production.PeriodicFleetSyncConstructor(fleetSync),
		&river.PeriodicJobOpts{RunOnStart: true},
	))
}

// EnableModelGeneration turns on rendering personalised models, or explains
// once why it is off.
//
// Off is a supported state, not a failure: without OpenSCAD a Dual Name Plank
// job is still created, still carries issue_reason = 'stl_missing', and is
// still fixable by uploading a model by hand. Saying so at startup is what
// stops that looking like a silent bug later.
func EnableModelGeneration(
	server *httpapi.Server, cfg config.Settings,
	enqueuer *production.ModelGenEnqueuer, logger *slog.Logger,
) {
	if cfg.OpenSCADBin == "" {
		logger.Info("model generation disabled: OPENSCAD_BIN is unset; " +
			"personalised jobs will be held for a manual upload")
		return
	}
	renderer := personalise.NewRenderer(cfg.OpenSCADBin, cfg.OpenSCADAssetDir, 0)
	if !renderer.Available() {
		logger.Warn("model generation disabled: OpenSCAD is not runnable",
			"bin", cfg.OpenSCADBin)
		return
	}
	if cfg.GeneratedMachineFamily == "" {
		// Worth a warning rather than a refusal: the model still renders and
		// attaches, the job simply stays held on profile_missing until a
		// family is set, which is a one-line fix an operator can make.
		logger.Warn("GENERATED_MACHINE_FAMILY is unset; rendered jobs will be " +
			"held as profile_missing until it names the printer family planks run on")
	}
	server.EnableModelGeneration(renderer, enqueuer)
	logger.Info("model generation enabled", "openscad", cfg.OpenSCADBin,
		"machine_family", cfg.GeneratedMachineFamily)
}
