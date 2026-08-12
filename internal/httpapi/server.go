package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/secretbox"
	"github.com/Optiminastic/tensor-core/internal/slicing"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

// Server holds the HTTP layer's dependencies and builds the router.
type Server struct {
	cfg     config.Settings
	store   *db.Store
	guards  *auth.Guards
	logger  *slog.Logger
	shopify *shopify.Client
	// secrets seals Shopify access tokens at rest. Nil when TOKEN_ENCRYPTION_KEY is
	// unset; the Shopify integration routes then fail closed (503).
	secrets *secretbox.Box

	// Design pipeline dependencies. Nil until EnablePipeline is called; the
	// design routes fail closed (503) when they are absent.
	storage  *storage.Client
	enqueuer *slicing.Enqueuer

	// Production pipeline dependencies. Nil until EnableProductionQueue is
	// called; webhook order import then just saves data without scheduling job
	// creation (Stage 2 never fires), matching how the design pipeline degrades.
	jobEnqueuer   *production.JobCreationEnqueuer
	batchEnqueuer *production.BatchPlanEnqueuer
}

// NewServer wires the HTTP layer. logger may be nil, in which case the request
// middleware falls back to slog's default. The Shopify client is built once here
// and shared across publishes so connections are reused.
func NewServer(cfg config.Settings, store *db.Store, guards *auth.Guards, logger *slog.Logger) *Server {
	// A nil box is fine: the Shopify routes check shopifyReady and 503 without it.
	box, _ := secretbox.New(cfg.TokenEncryptionKey)
	return &Server{
		cfg:     cfg,
		store:   store,
		guards:  guards,
		logger:  logger,
		shopify: shopify.New(cfg.ShopifyAPIVersion, cfg.ShopifyTimeout),
		secrets: box,
	}
}

// EnablePipeline attaches the object store and River slice enqueuer so the design
// routes can accept uploads and enqueue slices transactionally.
func (s *Server) EnablePipeline(objects *storage.Client, enqueuer *slicing.Enqueuer) {
	s.storage = objects
	s.enqueuer = enqueuer
}

// EnableProductionQueue attaches the River job-creation and batch-plan
// enqueuers so order webhooks can transactionally schedule Stage 2 (and, once
// jobs land, a debounced Stage 5 replan). Both wrap the same insert-only River
// client EnablePipeline's enqueuer uses - one client enqueues any Kind.
func (s *Server) EnableProductionQueue(jobs *production.JobCreationEnqueuer, batches *production.BatchPlanEnqueuer) {
	s.jobEnqueuer = jobs
	s.batchEnqueuer = batches
}

// Router builds the Gin engine with CORS, health, and every router mounted at
// the same prefixes as the FastAPI app.
func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(s.recoverPanic())
	r.Use(s.requestContext())
	r.Use(s.cors())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "environment": s.cfg.Environment})
	})
	r.GET("/ready", s.readiness)

	s.registerPricing(r)
	s.registerConfig(r)
	s.registerAdmin(r)
	s.registerProjects(r)
	s.registerBrands(r)
	s.registerConnections(r)
	s.registerShopifyProducts(r)
	s.registerDesigns(r)
	s.registerFiles(r)
	s.registerOrders(r)
	s.registerProductionJobs(r)
	s.registerBatches(r)
	s.registerFilament(r)
	s.registerMachineOps(r)
	s.registerFleetMachines(r)
	s.registerDispatch(r)
	s.registerJobEvents(r)
	s.registerJobIssues(r)
	s.registerShopify(r)
	s.registerInternal(r)

	return r
}

// readiness is a real readiness probe (distinct from the always-ok /health
// liveness check): it pings the database so an orchestrator only routes traffic
// once the pool can actually serve queries. It returns 503 with the standard
// {"detail"} shape when the database is unreachable.
func (s *Server) readiness(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Pool.Ping(ctx); err != nil {
		obs.FromContext(c.Request.Context()).Error("readiness: database unreachable", "error", err)
		detail(c, http.StatusServiceUnavailable, "The service is not ready.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

// cors mirrors the FastAPI CORSMiddleware: the configured origins, credentials
// allowed, all methods and headers.
func (s *Server) cors() gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(s.cfg.CORSOrigins))
	for _, o := range s.cfg.CORSOrigins {
		allowed[o] = struct{}{}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if _, ok := allowed[origin]; ok {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", "*")
			c.Header("Access-Control-Allow-Headers", "*")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
