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
	"github.com/Optiminastic/tensor-core/internal/slicing"
	"github.com/Optiminastic/tensor-core/internal/storage"
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
	if objects, err := storage.New(
		ctx, cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey, cfg.MinIOBucket, cfg.MinIOSecure,
	); err != nil {
		log.Printf("design pipeline disabled: object storage unavailable: %v", err)
	} else {
		server.EnablePipeline(objects, slicing.NewEnqueuer(riverClient))
		log.Printf("design pipeline enabled (storage=%s, queue=river)", cfg.MinIOEndpoint)
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
}
