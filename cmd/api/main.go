// Command api runs the Tensor-Core HTTP service.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/httpapi"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/slicing"
	"github.com/Optiminastic/tensor-core/internal/storage"
	"github.com/Optiminastic/tensor-core/internal/workerset"
)

func main() {
	_ = godotenv.Load("env/local.env")
	cfg := config.Load()

	// Structured JSON logging for the whole process.
	logger := obs.New(cfg.LogLevel)

	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}
	// Fail fast on a misconfigured production deploy (missing secrets, dev
	// defaults) rather than coming up "healthy" and failing per request.
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}
	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{
		MaxConns:         cfg.DBMaxConns,
		MinConns:         cfg.DBMinConns,
		MaxConnLifetime:  cfg.DBMaxConnLifetime,
		MaxConnIdleTime:  cfg.DBMaxConnIdleTime,
		HealthCheck:      cfg.DBHealthCheck,
		ConnectTimeout:   cfg.DBConnectTimeout,
		StatementTimeout: cfg.DBStatementTimeout,
	})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	verifier := auth.NewVerifier(
		ctx, cfg.AuthJWKSURL, cfg.AuthIssuer, cfg.AuthAudience,
		time.Duration(cfg.JWKSCacheSeconds)*time.Second,
	)
	guards := auth.NewGuards(verifier, cfg.InternalAPISecret)
	// Enforce token permission freshness: a revoked grant takes effect within the
	// TTL instead of lingering until the token expires. Fails closed. TTL 0 keeps
	// the previous behaviour (no per-request check).
	if cfg.PermissionFreshnessTTL > 0 {
		guards.EnablePermissionFreshness(
			func(ctx context.Context, userID string) (int, error) {
				return auth.CurrentPermissionsVersion(ctx, store.Q, userID)
			},
			cfg.PermissionFreshnessTTL,
		)
	}
	server := httpapi.NewServer(cfg, store, guards, logger)

	// Design pipeline: attach object storage + the River slice enqueuer. If storage
	// is unreachable the API still serves everything else; the design routes fail
	// closed with 503 until it is available. The enqueuer only inserts jobs (the
	// worker process consumes them), so it needs just the pool.
	riverClient, err := slicing.NewInsertOnlyClient(store.Pool)
	if err != nil {
		log.Fatalf("build river client: %v", err)
	}

	// One insert-only client enqueues any registered Kind, so the production
	// pipeline's enqueuers wrap the same client as the slice enqueuer above.
	server.EnableProductionQueue(
		production.NewJobCreationEnqueuer(riverClient),
		production.NewBatchPlanEnqueuer(riverClient, time.Duration(cfg.BatchPlanDebounceSeconds)*time.Second),
	)
	// Same client for the Shopify order pull. The API is where "Sync from
	// Shopify" lands, and without this the button would 503 - it no longer
	// imports on the request, having discovered that a five-second browser
	// timeout cancelled the import halfway and lost thirty-five orders.
	server.EnableOrderSync(production.NewOrderSyncEnqueuer(riverClient))

	// The API enqueues renders (a manual retry from the jobs page) but never
	// runs them; cmd/productionworker is where the OpenSCAD subprocess lives.
	if cfg.OpenSCADBin != "" {
		server.EnableModelGeneration(
			personalise.NewRenderer(cfg.OpenSCADBin, cfg.OpenSCADAssetDir, 0),
			production.NewModelGenEnqueuer(riverClient),
		)
	}

	storageReady := false
	if objects, err := storage.New(ctx, storage.Options{
		Endpoint:           cfg.S3Endpoint,
		AccessKey:          cfg.S3AccessKey,
		SecretKey:          cfg.S3SecretKey,
		Bucket:             cfg.S3Bucket,
		KeyPrefix:          cfg.S3KeyPrefix,
		Secure:             cfg.S3Secure,
		AssumeBucketExists: cfg.S3AssumeBucketExists,
	}); err != nil {
		log.Printf("design pipeline disabled: object storage unavailable: %v", err)
	} else {
		server.EnablePipeline(objects, slicing.NewEnqueuer(riverClient))
		storageReady = true
		log.Printf("design pipeline enabled (storage=%s, queue=river)", cfg.S3Endpoint)
	}

	// The production pipeline, in this process: job creation, model rendering,
	// batch planning, dispatch and the Shopify order pull.
	//
	// On by default, because "the backend is running" should mean the pipeline
	// is running. It used to live only in cmd/productionworker, so starting the
	// API alone gave you a system that imported orders, created jobs and then
	// stopped - with nothing on screen to say why, because every stage was
	// enqueued perfectly well and simply never consumed.
	//
	// Set RUN_PRODUCTION_WORKERS=false to run the API alone, which is what a
	// deployment with a dedicated worker host wants; the enqueuers attached
	// above keep working either way, so the API can still queue work for that
	// host. Storage is a hard requirement: every job here reads or writes a
	// model file.
	var workers *workerset.Runner
	switch {
	case !cfg.RunProductionWorkers:
		log.Printf("production pipeline not started here (RUN_PRODUCTION_WORKERS=false); " +
			"run cmd/productionworker to consume the queues")
	case !storageReady:
		log.Printf("production pipeline not started: it needs object storage, which is unavailable")
	default:
		started, err := workerset.Start(ctx, server, store, cfg, logger)
		if err != nil {
			log.Fatalf("start production workers: %v", err)
		}
		workers = started
	}

	addr := ":" + cfg.Port
	srv := &http.Server{
		Addr:    addr,
		Handler: server.Router(),
		// ReadTimeout is intentionally unset: the design upload streams large
		// multipart bodies and a short read deadline would truncate it. Query
		// latency is bounded server-side by the DB statement timeout instead.
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}

	go func() {
		log.Printf("Tensor-Core listening on %s (env=%s)", addr, cfg.Environment)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
	// After the HTTP server, so a request that enqueues work still finds a
	// running client; Stop drains what is already in flight.
	if err := workers.Stop(shutdownCtx); err != nil {
		log.Printf("production workers did not stop cleanly: %v", err)
	}
}
