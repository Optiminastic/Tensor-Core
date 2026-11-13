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

const (
	// whatsappConfirmSweep is how often newly synced paid orders are checked
	// for a personalisation confirmation.
	whatsappConfirmSweep = 5 * time.Minute
	// whatsappPurge is how often message history past retention is deleted.
	whatsappPurge = 24 * time.Hour
)

// Runner is a started worker set. Stop it to drain in-flight jobs.
type Runner struct {
	client *river.Client[pgx.Tx]
	// stopTickers ends the process-local schedulers this runner owns. They are
	// not River periodic jobs, deliberately - see startWindowSweep.
	stopTickers context.CancelFunc
}

// Stop drains and shuts the workers down. Safe on a nil Runner, so a caller
// that never started one can defer this unconditionally.
func (r *Runner) Stop(ctx context.Context) error {
	if r == nil || r.client == nil {
		return nil
	}
	if r.stopTickers != nil {
		r.stopTickers()
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
// Role is which half of the pipeline a process consumes.
//
// THE SPLIT IS BY WHO IS WAITING. RoleProduction is the floor's work - job
// creation, renders for printing, planning, dispatch, the Shopify pull - and
// none of it has anybody watching a timer. RolePersonalisation is the
// customer's: the link, the preview they are editing right now, and the window
// that must close on time or their order never reaches a bed.
//
// Running them in one process means a backlog of plate renders or a long
// Shopify pull sits in front of a customer's thirty-minute window. Splitting
// them means the window closes on schedule whatever the floor is doing.
type Role string

const (
	// RoleProduction is the floor's queues, plus the customer's while
	// RUN_PERSONALISATION_WORKERS is true.
	RoleProduction Role = "production"
	// RolePersonalisation is the customer's queues and nothing else.
	RolePersonalisation Role = "personalisation"
	// RoleBatch is the bed's whole lifecycle and nothing else: planning jobs
	// of one product and one colour into Drafts, locking them, sending them
	// to printers, pinning them to the printer chosen, and noticing when a
	// print finishes. The pipeline also runs these while RUN_BATCH_WORKERS is
	// true.
	RoleBatch Role = "batch"
)

// Start runs the production pipeline, and the personalisation queues with it
// unless they have been handed to cmd/personalisationworker.
func Start(
	ctx context.Context, server *httpapi.Server, store *db.Store,
	cfg config.Settings, logger *slog.Logger,
) (*Runner, error) {
	return start(ctx, server, store, cfg, logger, RoleProduction)
}

// StartPersonalisation runs ONLY the customer's queues: the link send, the
// preview render and the window sweep.
//
// Deliberately no periodic jobs. River runs those on the elected leader alone,
// and this process must never become the leader for the floor's replan or
// dispatch - it does not consume those queues, so the tick would be swallowed
// by a process that cannot act on it.
func StartPersonalisation(
	ctx context.Context, server *httpapi.Server, store *db.Store,
	cfg config.Settings, logger *slog.Logger,
) (*Runner, error) {
	return start(ctx, server, store, cfg, logger, RolePersonalisation)
}

// StartBatch runs ONLY the bed's queues: plan, dispatch, fleet sync (which
// also notices finished prints) and the printer pin.
//
// No River periodic jobs, for StartPersonalisation's reason. Its own replan,
// dispatch and fleet ticks are process-owned tickers instead (see
// startBatchTickers), so they fire whichever client holds River's lease.
func StartBatch(
	ctx context.Context, server *httpapi.Server, store *db.Store,
	cfg config.Settings, logger *slog.Logger,
) (*Runner, error) {
	return start(ctx, server, store, cfg, logger, RoleBatch)
}

func start(
	ctx context.Context, server *httpapi.Server, store *db.Store,
	cfg config.Settings, logger *slog.Logger, role Role,
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

	// Tying a sent plate to the printer somebody chose. Registered
	// unconditionally: these jobs are enqueued by an operator pressing Queue,
	// not by a flag, so a queue nothing consumes would mean the machine choice
	// silently never takes effect.
	river.AddWorker(workers, httpapi.NewQueuePinWorker(server, logger))

	// Product Creation's background builds - the sized moon lamp, cut by
	// OpenSCAD. Registered unconditionally like the renderer: a build is
	// queued by somebody pressing a button, and a queue nothing consumes would
	// leave the page waiting for ever.
	river.AddWorker(workers, httpapi.NewProductBuildWorker(server, logger))

	// Waiting for a slice, then queueing the result on the printer an operator
	// chose. Registered unconditionally for the same reason as the pin worker:
	// these jobs come from somebody pressing Queue, not from a flag.
	river.AddWorker(workers, httpapi.NewSliceQueueWorker(server, logger))

	// Pulling Shopify orders. Registered unconditionally: this is the only
	// path an order takes into Tensor, and a queue nothing consumes would mean
	// pressing Sync reports success and imports nothing.
	river.AddWorker(workers, httpapi.NewOrderSyncWorker(
		server, logger, time.Duration(cfg.OrderSyncTimeoutMinutes)*time.Minute,
	))

	// NO WIN-BACK WORKER HERE, and no win-back queue or periodic job below.
	//
	// cmd/winbackworker owns that sweep outright. It is the only process given
	// the Sarvam and WhatsApp credentials, and the only one whose two consent
	// switches are set - so a pipeline process picking a sweep off the shared
	// queue would find no channel connected, log that it would have contacted
	// somebody, and consume the tick. Half the sweeps would quietly do
	// nothing, which is worse than none doing anything, because it looks like
	// it is working.
	//
	// The rule is one owner: the process that holds the credentials is the
	// process that schedules the sweep and the process that runs it.

	// The customer's own check on an arriving order, and the sweep that lets
	// the order go again when their window closes.
	//
	// REGISTERED HERE, unlike the win-back. That worker is deliberately
	// elsewhere because it holds credentials no pipeline process has; this one
	// is triggered by the order import that happens in this very process, and
	// a queue registered nowhere is a queue whose jobs sit unworked - which is
	// indistinguishable from the feature being switched off. Extracting it to
	// its own container later is a deployment move, and the day it happens the
	// queue has to move with it.
	river.AddWorker(workers, httpapi.NewPersonalisationLinkWorker(server, logger))
	river.AddWorker(workers, httpapi.NewPersonalisationExpiryWorker(server, logger))

	// The WhatsApp bot, confirmations and inbox housekeeping. Registered
	// unconditionally: whether a brand's bot answers is that brand's own
	// setting (bot_enabled, confirmations_enabled), read from the database by
	// whichever process runs the job, so there is no per-process switch to get
	// out of step - unlike the win-back sweep above.
	river.AddWorker(workers, httpapi.NewWhatsAppInboundWorker(server, logger))
	river.AddWorker(workers, httpapi.NewWhatsAppMediaWorker(server, logger))
	river.AddWorker(workers, httpapi.NewWhatsAppEchoWorker(server))
	river.AddWorker(workers, httpapi.NewWhatsAppFailedWorker(server, logger))
	river.AddWorker(workers, httpapi.NewWhatsAppConfirmSweepWorker(server, logger))
	river.AddWorker(workers, httpapi.NewWhatsAppReminderWorker(server, logger))
	river.AddWorker(workers, httpapi.NewWhatsAppAutoConfirmWorker(server, logger))
	river.AddWorker(workers, httpapi.NewWhatsAppPurgeWorker(server, logger))

	fleetSync := time.Duration(cfg.FleetSyncIntervalSeconds) * time.Second
	if fleetSync > 0 {
		river.AddWorker(workers, httpapi.NewFleetSyncWorker(
			server, logger, time.Duration(cfg.FleetSyncTimeoutMinutes)*time.Minute,
		))
	}

	debounce := time.Duration(cfg.BatchPlanDebounceSeconds) * time.Second
	// WHICH QUEUES THIS PROCESS CONSUMES. Registering a worker is not the
	// same as consuming its queue: the bundle above holds every worker so that
	// either process can ENQUEUE any job - River validates an insert against
	// the Workers bundle, not against this map - while only the queues named
	// here are actually worked.
	queues := map[string]river.QueueConfig{}
	if role == RoleProduction {
		for name, qc := range productionQueues(cfg, concurrency) {
			queues[name] = qc
		}
	}
	// The customer's queues run here too until a dedicated worker takes them.
	ownsPersonalisation := role == RolePersonalisation ||
		(role == RoleProduction && cfg.RunPersonalisationWorkers)
	if ownsPersonalisation {
		for name, qc := range personalisationQueues() {
			queues[name] = qc
		}
	}
	// The bed's queues likewise, until cmd/batchworker takes them.
	ownsBatches := role == RoleBatch ||
		(role == RoleProduction && cfg.RunBatchWorkers)
	if ownsBatches {
		for name, qc := range batchQueues(fleetSync) {
			queues[name] = qc
		}
	}
	if len(queues) == 0 {
		// Reachable only by running the pipeline with every half switched
		// off, which is a deployment that consumes nothing while looking
		// healthy - the failure this whole file keeps having.
		return nil, fmt.Errorf(
			"this process would consume no queues: role=%s, RUN_PERSONALISATION_WORKERS=%v, RUN_BATCH_WORKERS=%v",
			role, cfg.RunPersonalisationWorkers, cfg.RunBatchWorkers)
	}

	// No periodic jobs outside the pipeline. River elects ONE leader across
	// every client on the database and runs them there; a personalisation
	// process winning that election would swallow the floor's replan and
	// dispatch ticks without consuming either queue.
	var periodic []*river.PeriodicJob
	if role == RoleProduction {
		periodic = periodicJobs()
	}

	client, err := river.NewClient(riverpgxv5.New(store.Pool), &river.Config{
		Queues:       queues,
		Workers:      workers,
		Logger:       logger,
		PeriodicJobs: periodic,
	})
	if err != nil {
		return nil, fmt.Errorf("build river client: %w", err)
	}

	server.EnableProductionQueue(
		production.NewJobCreationEnqueuer(client),
		production.NewBatchPlanEnqueuer(client, debounce),
	)
	server.EnableOrderSync(production.NewOrderSyncEnqueuer(client))
	server.EnablePersonalisationCheck(production.NewPersonalisationLinkEnqueuer(client))
	server.EnableWhatsAppQueue(production.NewWhatsAppEnqueuer(client))
	server.EnableBatchDispatchQueue(production.NewDispatchEnqueuer(client, debounce))
	server.EnableQueuePinQueue(production.NewQueuePinEnqueuer(client))
	server.EnableProductBuilds(production.NewProductBuildEnqueuer(client))
	server.EnableSliceQueue(production.NewQueueSlicedPlateEnqueuer(client))
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
	logger.Info("production pipeline running", "role", role,
		"owns_batches", ownsBatches, "owns_personalisation", ownsPersonalisation,
		"concurrency", concurrency, "renders", modelGenConcurrency(cfg),
		"auto_dispatch", cfg.BatchAutoDispatch,
		"order_sync_minutes", cfg.OrderSyncIntervalMinutes)
	tickerCtx, stopTickers := context.WithCancel(context.WithoutCancel(ctx))
	// ONLY WHERE THE QUEUE IS CONSUMED. The sweep enqueues onto
	// personalisation_links; ticking it from a process that does not work that
	// queue would pile up jobs nobody runs, and the unique-by-state insert
	// would then skip every later tick - the sweep would look alive and do
	// nothing. That is the shape of the original bug.
	if ownsPersonalisation {
		startWindowSweep(tickerCtx, client, logger)
	}
	// The order pull belongs to the pipeline, not to the bed: a batch worker
	// pulling Shopify would be a second owner of the import.
	if role == RoleProduction {
		if orderSync := time.Duration(cfg.OrderSyncIntervalMinutes) * time.Minute; orderSync > 0 {
			startOrderSyncTick(tickerCtx, client, orderSync, logger)
		}
	}
	// ONLY WHERE THE BED'S QUEUES ARE CONSUMED, for startWindowSweep's reason.
	if ownsBatches {
		startBatchTickers(tickerCtx, client, cfg, debounce, fleetSync, logger)
	}
	markPersonalisationStart(ctx, store, cfg, logger)

	return &Runner{client: client, stopTickers: stopTickers}, nil
}

// startOrderSyncTick pulls every connected store's orders on a ticker THIS
// PROCESS owns.
//
// NOT A RIVER PERIODIC JOB, which is what it used to be, and it failed the
// same way the personalisation window sweep failed before it - the third time
// this codebase has been bitten by the same thing. River runs periodic jobs on
// the ELECTED LEADER ALONE. Four processes on this database run a River client
// with queues: the production worker, the slice worker, the win-back worker
// and the API when it runs the pipeline itself. Whichever takes the lease
// first holds it, and only the jobs IT registered ever tick. The order sync is
// registered by the production worker alone, so any of the other three holding
// the lease means no store is pulled at all - and the slice worker has been
// crash-looping, which hands the lease round and round.
//
// Nothing on any screen says so. The schedule is configured, the worker is
// running, the log line at startup says "order_sync_minutes 10", and orders
// still only arrive when somebody presses "Sync from Shopify" - which is
// exactly the state this periodic job was written to end.
//
// Safe in every process at once, and that is the point rather than a caveat:
// SyncOrdersArgs is inserted unique BY PERIOD, so K processes ticking the same
// minute produce one pull between them rather than K stampeding one Shopify
// store. A duplicate is the expected outcome, not an error.
func startOrderSyncTick(
	ctx context.Context, client *river.Client[pgx.Tx], every time.Duration, logger *slog.Logger,
) {
	go func() {
		// RunOnStart, as the periodic job had: a deploy is the most likely
		// moment for a backlog to exist, and waiting a full interval to find
		// out is the overnight order all over again.
		enqueueOrderSync(ctx, client, every, logger)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				enqueueOrderSync(ctx, client, every, logger)
			}
		}
	}()
}

// enqueueOrderSync asks for one pull of every connected store.
//
// The insert options come from production.OrderSyncInsertOpts rather than
// being written here, so the dedupe window cannot drift from the thing that
// documents it.
func enqueueOrderSync(
	ctx context.Context, client *river.Client[pgx.Tx], every time.Duration, logger *slog.Logger,
) {
	res, err := client.Insert(ctx, production.SyncOrdersArgs{},
		production.OrderSyncInsertOpts(every))
	switch {
	case err != nil:
		// One missed pull, not a dead scheduler: the next tick tries again.
		logger.Error("could not enqueue the Shopify order sync", "error", err)
	case res.UniqueSkippedAsDuplicate:
		// Ordinary when several processes tick: one of them wins.
		logger.Debug("Shopify order sync already queued")
	}
}

// markPersonalisationStart writes down that the shop began asking NOW.
//
// The watermark is what makes the personalisation check mean "every order from
// here on", and it is laid down the first time an order is weighed against it.
// That is one order too late. The rule refuses anything placed at or before
// the mark, so the very first order to arrive after the switch was flipped
// would create the mark during its own evaluation and then be refused by it -
// the customer never asked, and the log saying "this order was placed before
// the shop began asking" about an order from thirty seconds ago.
//
// Writing it at startup pins it to the moment somebody turned the feature on,
// which is what they meant by turning it on. Every order placed after that is
// asked about, including the first.
//
// Only when the check is enabled, and only ever written once per brand -
// EnsurePersonalisationWatermark never moves a mark that exists, so a restart
// cannot push the line forward and quietly skip the orders that arrived while
// the service was down.
func markPersonalisationStart(
	ctx context.Context, store *db.Store, cfg config.Settings, logger *slog.Logger,
) {
	if !cfg.PersonalisationCheckEnabled {
		return
	}
	slugs, err := store.Q.ListConnectedShopifyBrandSlugs(ctx)
	if err != nil {
		logger.Warn("personalisation: could not read the connected brands, so the "+
			"watermark will be set by the first order instead", "error", err)
		return
	}
	for _, slug := range slugs {
		mark, err := store.Q.EnsurePersonalisationWatermark(ctx, slug)
		if err != nil {
			logger.Warn("personalisation: could not mark when this brand began asking",
				"brand", slug, "error", err)
			continue
		}
		if mark.WatchFrom.Valid {
			logger.Info("personalisation: asking about orders placed after this",
				"brand", slug, "since", mark.WatchFrom.Time.UTC())
		}
	}
}

// windowSweepInterval is how often a closed window is noticed. A customer's
// thirty minutes does not need second-accuracy, and a minute keeps the
// released order moving while the floor is still looking at the screen.
const windowSweepInterval = time.Minute

// startWindowSweep closes expired personalisation windows on a ticker THIS
// PROCESS owns.
//
// NOT A RIVER PERIODIC JOB, which is what it used to be. River runs those on
// the elected leader alone, and on a shared database the lease is held by
// whichever client took it first - routinely a deployed container rather than
// the process that was meant to be doing the work. When that container runs a
// build without this job registered, the tick simply never happens: the sweep
// was observed running every minute for the five minutes this process held the
// lease, then stopping dead for the next eighteen while a container held it,
// with an order held open the whole time.
//
// That matters more here than anywhere else. The sweep is the ONLY thing that
// releases a job held for its customer, so when it stops, orders stop - and
// nothing on any screen says why.
//
// Safe in every process at once: ExpirePersonalisationLinksArgs is unique by
// state, so a second insert while one is pending or running is skipped rather
// than doubling the work.
func startWindowSweep(ctx context.Context, client *river.Client[pgx.Tx], logger *slog.Logger) {
	go func() {
		ticker := time.NewTicker(windowSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				res, err := client.Insert(ctx, production.ExpirePersonalisationLinksArgs{}, nil)
				switch {
				case err != nil:
					// One missed sweep, not a dead scheduler.
					logger.Error("could not enqueue the personalisation window sweep", "error", err)
				case res.UniqueSkippedAsDuplicate:
					// Ordinary when several processes tick: one of them wins.
					logger.Debug("personalisation window sweep already queued")
				}
			}
		}
	}()
}

// modelGenConcurrency is the configured render concurrency, floored at one so a
// zero or negative setting cannot silently stop rendering altogether.
func modelGenConcurrency(cfg config.Settings) int {
	if cfg.ModelGenConcurrency > 0 {
		return cfg.ModelGenConcurrency
	}
	return defaultModelGenConcurrency
}

// productionQueues is the floor's work: nobody is watching a clock.
func productionQueues(cfg config.Settings, concurrency int) map[string]river.QueueConfig {
	return map[string]river.QueueConfig{
		production.JobCreationQueueName: {MaxWorkers: concurrency},
		// OpenSCAD is CPU-bound for 20-45 seconds per plank. Bounded rather
		// than unlimited: renders are pure CPU and the box also runs the
		// API, the database and Docker.
		production.ModelGenQueueName: {MaxWorkers: modelGenConcurrency(cfg)},
		// One build at a time: a moon lamp is several seconds of OpenSCAD
		// on both cores already (top and cap side by side).
		production.ProductBuildQueueName: {MaxWorkers: 1},
		// One at a time: a pull is minutes of Shopify round trips and order
		// upserts, and two running together would race to write the same
		// rows for no gain - Shopify is the bottleneck, not Tensor.
		production.OrderSyncQueueName: {MaxWorkers: 1},
		// One at a time, so a customer's messages are answered in the
		// order they arrived. See WhatsAppInboundQueueName.
		production.WhatsAppInboundQueueName: {MaxWorkers: 1},
		production.WhatsAppQueueName:        {MaxWorkers: 2},
	}
}

// batchQueues is the bed's whole lifecycle: Draft -> Locked -> printing ->
// completed.
//
// SEPARABLE BECAUSE IT IS ONE STATE MACHINE. The planner forms and locks
// beds, dispatch sends them, the pin ties a plate to its printer, and the
// fleet sync is what notices a print finished and completes the bed. Split
// across processes, each half waits on the other's backlog; in one process
// they cannot fall out of step, and a burst of renders or a Shopify pull on
// the pipeline no longer sits in front of a bed that is ready to print.
func batchQueues(fleetSync time.Duration) map[string]river.QueueConfig {
	queues := map[string]river.QueueConfig{
		// One at a time: two planners would dissolve and reform the same
		// Drafts under each other.
		production.BatchPlanQueueName: {MaxWorkers: 1},
		// One at a time, and off the planning queue: a pass spends its time
		// uploading multi-megabyte plates over the printer host's link, and
		// two passes at once would race to approve the same bed.
		production.DispatchBatchQueueName: {MaxWorkers: 1},
		// Pins waiting on a slice. More than one at a time because each
		// spends its life asleep between retries, and two operators sending
		// two beds must not queue behind each other.
		production.QueuePinQueueName: {MaxWorkers: 2},
	}
	if fleetSync > 0 {
		// Its own queue, and one at a time: a refresh waits on a printer
		// host that may be asleep, and must never hold the single
		// batch-plan slot while it does. Only where the worker exists -
		// FLEET_SYNC_INTERVAL_SECONDS=0 registers none.
		queues[production.FleetSyncQueueName] = river.QueueConfig{MaxWorkers: 1}
	}
	return queues
}

// startBatchTickers schedules the bed's replan, dispatch pass and fleet
// refresh from THIS PROCESS rather than as River periodic jobs.
//
// River runs periodic jobs on the elected leader alone, and on this database
// that lease is held by whichever of several clients took it first. A
// periodic replan registered only by the pipeline therefore ticked only while
// the pipeline was leader - the same way the order sync and the window sweep
// once stopped (see startOrderSyncTick). Every insert below is unique BY
// PERIOD, so several processes ticking together produce one run between them.
//
// RunOnStart as before: the fleet refresh runs at once, because a fresh
// deployment's machines table is empty until it does; a replan or a dispatch
// on every deploy would be wasted work, or would commit filament.
func startBatchTickers(
	ctx context.Context, client *river.Client[pgx.Tx], cfg config.Settings,
	debounce, fleetSync time.Duration, logger *slog.Logger,
) {
	plan := time.Duration(cfg.BatchPlanIntervalMinutes) * time.Minute
	if plan > 0 {
		tick(ctx, client, plan, false, "batch replan",
			production.PeriodicBatchPlanConstructor(debounce), logger)
		if cfg.BatchAutoDispatch {
			// Reuses the batch-plan interval: dispatch has nothing to do
			// until a plan has run.
			tick(ctx, client, plan, false, "batch dispatch",
				production.PeriodicDispatchConstructor(plan), logger)
		}
	}
	if fleetSync > 0 {
		tick(ctx, client, fleetSync, true, "fleet refresh",
			production.PeriodicFleetSyncConstructor(fleetSync), logger)
	}
}

// tick inserts build()'s job every period until ctx ends.
func tick(
	ctx context.Context, client *river.Client[pgx.Tx], every time.Duration, onStart bool,
	what string, build func() (river.JobArgs, *river.InsertOpts), logger *slog.Logger,
) {
	insert := func() {
		args, opts := build()
		res, err := client.Insert(ctx, args, opts)
		switch {
		case err != nil:
			// One missed tick, not a dead scheduler: the next one tries again.
			logger.Error("could not enqueue the "+what, "error", err)
		case res.UniqueSkippedAsDuplicate:
			// Ordinary when several processes tick: one of them wins.
			logger.Debug(what + " already queued")
		}
	}
	go func() {
		if onStart {
			insert()
		}
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				insert()
			}
		}
	}()
}

// personalisationQueues is the customer's work, and all of it is on a clock.
//
// SEPARABLE BECAUSE IT IS URGENT, not because it is big. A thirty-minute
// window that closes late is an order that misses its bed; a plate render that
// runs late is a plate that renders later. Behind one process the second
// delays the first, and the customer's timer is the one that cannot wait.
func personalisationQueues() map[string]river.QueueConfig {
	return map[string]river.QueueConfig{
		// One at a time. This queue messages customers, and the cost of
		// getting concurrency wrong is several strangers' phones buzzing
		// at once from a burst nobody intended. It also carries the window
		// sweep, which is why that cannot be starved by a render.
		production.PersonalisationLinkQueueName: {MaxWorkers: 1},
		// Renders a customer is waiting on, kept off the floor's own pool
		// so neither can starve the other. Two at a time: enough that a
		// second customer editing is not stuck behind the first, few
		// enough that OpenSCAD does not take the box.
		production.PersonalisationPreviewQueueName: {MaxWorkers: 2},
	}
}

// periodicJobs is the River periodic work the pipeline still owns: the
// WhatsApp housekeeping.
//
// Everything else ticks for itself. The bed's replan, dispatch and fleet
// refresh moved to startBatchTickers, the order pull to startOrderSyncTick,
// the window sweep to startWindowSweep and the win-back to
// cmd/winbackworker - all for one reason: River runs periodic jobs on the
// elected leader alone, and the lease on this database is held by whichever
// of several clients took it first, so a job registered by one process
// ticks only while that process happens to lead.
func periodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		// Confirmations ride on the order sync, which pulls every
		// ORDER_SYNC_INTERVAL_MINUTES; sweeping more often than that only
		// finds nothing new.
		river.NewPeriodicJob(
			river.PeriodicInterval(whatsappConfirmSweep),
			production.PeriodicWhatsAppConstructor(production.WhatsAppConfirmSweepArgs{}, whatsappConfirmSweep),
			&river.PeriodicJobOpts{RunOnStart: true},
		),
		river.NewPeriodicJob(
			river.PeriodicInterval(whatsappPurge),
			production.PeriodicWhatsAppConstructor(production.WhatsAppPurgeArgs{}, whatsappPurge),
			&river.PeriodicJobOpts{RunOnStart: true},
		),
	}
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
	server.EnableModelGeneration(renderer, enqueuer)
	// No machine family to report: a rendered job names no printer class, and
	// which machine takes its bed is BambuBuddy's call.
	logger.Info("model generation enabled", "openscad", cfg.OpenSCADBin)
}
