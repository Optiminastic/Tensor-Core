// Command winbackworker runs the abandoned-checkout call scheduler on its own.
//
// It consumes exactly one queue - winback_calls - which sweeps every connected
// store's abandoned checkouts and rings the customers who have gone quiet long
// enough (see internal/httpapi/winback_call_worker.go).
//
// SEPARATE FROM THE PIPELINE ON PURPOSE. This is the only process in Tensor
// that telephones a member of the public, and that deserves its own blast
// radius: stop this container and the calling stops dead, while orders keep
// importing, plates keep slicing and the floor keeps printing. Running it
// inside cmd/api would mean an API restart is a calling restart, and taking
// calling down would mean taking the API down.
//
// It needs no object storage and no OpenSCAD - it reads Shopify and talks to
// Sarvam - so it is a small container that can sit anywhere.
//
// SAFE BY DEFAULT: with WINBACK_CALLS_ENABLED unset it runs every rule, logs
// exactly who it would have rung, and places no calls.
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/httpapi"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

const stopTimeout = 30 * time.Second

// schedule enqueues a sweep every interval, for as long as this process lives.
//
// It inserts a job rather than calling Sweep directly, so the sweep keeps
// River's retries, its one-at-a-time queue and its row in river_job - a sweep
// that telephones people should be visible after the fact, not a goroutine
// that left no trace.
//
// Duplicates are impossible rather than unlikely: WinbackCallsArgs.InsertOpts
// is unique by args over a minute across pending, scheduled, available,
// running and retryable, so a tick that lands while the previous sweep is
// still going is refused by the database, not by this loop's timing.
//
// The first tick is one interval in, deliberately. A restart is not a reason
// to ring anybody, and during a deploy loop it would be a reason to ring them
// repeatedly.
func schedule(ctx context.Context, client *river.Client[pgx.Tx], every time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			res, err := client.Insert(ctx, production.WinbackCallsArgs{}, nil)
			switch {
			case err != nil:
				// Logged, never fatal. A database blip should cost one sweep,
				// not the scheduler.
				logger.Error("could not enqueue the win-back sweep", "err", err)
			case res.UniqueSkippedAsDuplicate:
				// Said out loud, because the alternative is silence that looks
				// exactly like the bug this ticker replaced.
				logger.Info("win-back sweep already queued or still running; this tick does nothing")
			default:
				logger.Info("win-back sweep enqueued", "job_id", res.Job.ID)
			}
		}
	}
}

func main() {
	// -once runs one sweep in this process and exits.
	//
	// The long-running mode now ticks for itself rather than waiting on a
	// River leader election it would lose, so this is no longer the only way
	// to make a sweep happen. It is still the only way to make one happen
	// NOW: an operator asking what the rules would do should not have to wait
	// out an interval, and with calling disabled the question is read-only.
	once := flag.Bool("once", false, "run one sweep now, print what it would do, and exit")
	flag.Parse()

	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	logger := obs.New("info")

	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	// Never serves HTTP, so a nil verifier is safe: NewGuards is a cheap
	// constructor and no guard is ever exercised here.
	server := httpapi.NewServer(cfg, store, auth.NewGuards(nil, ""), logger)

	workers := river.NewWorkers()
	worker := httpapi.NewWinbackCallWorker(server, logger)
	river.AddWorker(workers, worker)

	if *once {
		if err := worker.Sweep(ctx); err != nil {
			log.Fatalf("sweep: %v", err)
		}
		return
	}

	every := time.Duration(cfg.WinbackCallIntervalMinutes) * time.Minute
	if every <= 0 {
		log.Fatal("WINBACK_CALL_INTERVAL_MINUTES must be positive")
	}

	client, err := river.NewClient(riverpgxv5.New(store.Pool), &river.Config{
		Logger:  logger,
		Workers: workers,
		Queues: map[string]river.QueueConfig{
			// One at a time. The cost of getting concurrency wrong on a queue
			// that telephones people is not a slow page; it is several
			// strangers' phones ringing at once from a burst nobody intended.
			production.WinbackCallQueueName: {MaxWorkers: 1},
		},
		// NO PeriodicJobs, and that is the whole point of this file's ticker.
		//
		// River runs periodic jobs on the ELECTED LEADER only, and there is
		// one leader per database across every River client - the API, the
		// production worker, the slice worker and this. The lease goes to
		// whoever took it first and stays there while that process lives, so
		// this container, started last and restarted least, is almost never
		// the leader. Registering the sweep here therefore scheduled nothing:
		// on production the lease sat with tensor-production-worker, whose
		// build predated the win-back code entirely, and the result was a
		// worker that started cleanly, reported both channels enabled, and
		// swept exactly zero times in three days with nothing in any log to
		// say why.
		//
		// Inserting is not leader-gated. Working the queue is not either.
		// Only the periodic tick was, so the tick is ours now.
	})
	if err != nil {
		log.Fatalf("build the win-back worker: %v", err)
	}

	if err := client.Start(ctx); err != nil {
		log.Fatalf("start the win-back worker: %v", err)
	}
	logger.Info("win-back call scheduler started",
		"enabled", cfg.WinbackCallsEnabled,
		"delay_minutes", cfg.WinbackCallDelayMinutes,
		"interval_minutes", cfg.WinbackCallIntervalMinutes,
		"max_per_sweep", cfg.WinbackCallMaxPerRun,
		"restricted_to", cfg.WinbackCallOnly)
	if !cfg.WinbackCallsEnabled {
		logger.Warn("WINBACK_CALLS_ENABLED is false: every sweep will run and log, and no call will be placed")
	}

	go schedule(ctx, client, every, logger)

	<-ctx.Done()
	log.Println("shutting down")
	stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := client.Stop(stopCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
