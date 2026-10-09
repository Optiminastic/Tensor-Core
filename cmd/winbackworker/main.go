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
	"os"
	"os/signal"
	"syscall"
	"time"

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

func main() {
	// -once runs one sweep in this process and exits.
	//
	// Not a convenience: the periodic tick only fires on the River LEADER, and
	// the leader is whichever process took the lease first - usually the API.
	// So starting this worker and waiting proves nothing, and an operator who
	// wants to know what the rules would do right now has no way to ask.
	// With calling disabled this is a read-only question, which is why it is
	// safe to answer on demand.
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
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(every),
				production.PeriodicWinbackCallConstructor(every),
				// Not on start: a restart is not a reason to ring anybody, and
				// during a deploy loop it would be a reason to ring them
				// repeatedly. The first sweep is one interval in, by which
				// time the process has proved it can stay up.
				&river.PeriodicJobOpts{RunOnStart: false},
			),
		},
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

	<-ctx.Done()
	log.Println("shutting down")
	stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := client.Stop(stopCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
