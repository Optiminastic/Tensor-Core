package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// A brand can connect to these external ad and commerce platforms. The actual
// OAuth dance runs in the frontend, which holds the provider app credentials;
// this service only stores the resulting connection so the platform can be
// called on the brand's behalf.
var connectionProviders = map[string]struct{}{
	"google_ads":       {},
	"google_analytics": {},
	"meta_ads":         {},
	"shopify":          {},
}

// A connection is in exactly one of these states.
var connectionStatuses = map[string]struct{}{
	"connected":    {},
	"disconnected": {},
	"error":        {},
}

type connectionResponse struct {
	ID                string     `json:"id"`
	BrandSlug         string     `json:"brand_slug"`
	Provider          string     `json:"provider"`
	Status            string     `json:"status"`
	ExternalAccountID *string    `json:"external_account_id"`
	ExpiresAt         *time.Time `json:"expires_at"`
	ConnectedBy       *string    `json:"connected_by"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

func connectionDTO(
	id uuid.UUID, brandSlug, provider, status string, externalAccountID *string,
	expiresAt pgtype.Timestamptz, connectedBy *string, created, updated pgtype.Timestamptz,
) connectionResponse {
	return connectionResponse{
		ID: id.String(), BrandSlug: brandSlug, Provider: provider, Status: status,
		ExternalAccountID: externalAccountID, ExpiresAt: db.TimePtr(expiresAt),
		ConnectedBy: connectedBy, CreatedAt: db.Time(created), UpdatedAt: db.Time(updated),
	}
}

func (s *Server) registerConnections(r *gin.Engine) {
	g := r.Group("/brands/:slug/connections")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.BrandRead.Key()), s.listConnections)
	g.PUT("/:provider", s.guards.RequirePermission(auth.BrandManage.Key()), s.upsertConnection)
	g.DELETE("/:provider", s.guards.RequirePermission(auth.BrandManage.Key()), s.deleteConnection)
	g.POST("/shopify/sync", s.guards.RequirePermission(auth.BrandManage.Key()), s.syncShopifyOrders)

	// Not under /brands/:slug - it deliberately spans every brand, and the
	// all-brands view's slug is a sentinel with no connection of its own.
	all := r.Group("/connections/shopify")
	all.Use(s.guards.RequireUser())
	all.POST("/sync-all", s.guards.RequirePermission(auth.BrandManage.Key()), s.syncAllShopifyOrders)
}

// providerParam validates the {provider} path segment against the fixed set.
func providerParam(c *gin.Context) (string, bool) {
	provider := c.Param("provider")
	if _, ok := connectionProviders[provider]; !ok {
		detail(c, http.StatusUnprocessableEntity, "Unknown integration provider.")
		return "", false
	}
	return provider, true
}

func (s *Server) listConnections(c *gin.Context) {
	slug, ok := brandSlugParam(c)
	if !ok {
		return
	}
	if !s.brandMustExist(c, slug) {
		return
	}
	rows, err := s.store.Q.ListConnectionsForBrand(c.Request.Context(), slug)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list connections.")
		return
	}
	out := make([]connectionResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, connectionDTO(r.ID, r.BrandSlug, r.Provider, r.Status, r.ExternalAccountID,
			r.ExpiresAt, r.ConnectedBy, r.CreatedAt, r.UpdatedAt))
	}
	c.JSON(http.StatusOK, out)
}

type connectionUpsertRequest struct {
	Status            string     `json:"status" binding:"required"`
	ExternalAccountID *string    `json:"external_account_id"`
	AccessToken       *string    `json:"access_token"`
	RefreshToken      *string    `json:"refresh_token"`
	ExpiresAt         *time.Time `json:"expires_at"`
}

// upsertConnection stores (or refreshes) a brand's connection to a provider. It
// is called by the frontend after it completes the provider's OAuth flow, with
// the resulting tokens. Tokens are written but never read back out.
func (s *Server) upsertConnection(c *gin.Context) {
	slug, ok := brandSlugParam(c)
	if !ok {
		return
	}
	provider, ok := providerParam(c)
	if !ok {
		return
	}
	if !s.brandMustExist(c, slug) {
		return
	}

	var req connectionUpsertRequest
	if !bindJSON(c, &req) {
		return
	}
	if _, ok := connectionStatuses[req.Status]; !ok {
		detail(c, http.StatusUnprocessableEntity, "Status must be 'connected', 'disconnected', or 'error'.")
		return
	}

	user, _ := auth.UserFrom(c) // connected_by comes from the token, never the body.
	connectedBy := user.ID

	r, err := s.store.Q.UpsertConnection(c.Request.Context(), gen.UpsertConnectionParams{
		ID: uuid.New(), BrandSlug: slug, Provider: provider, Status: req.Status,
		ExternalAccountID: req.ExternalAccountID, AccessToken: req.AccessToken,
		RefreshToken: req.RefreshToken, ExpiresAt: db.Timestamptz(req.ExpiresAt),
		ConnectedBy: &connectedBy,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the connection.")
		return
	}
	c.JSON(http.StatusOK, connectionDTO(r.ID, r.BrandSlug, r.Provider, r.Status, r.ExternalAccountID,
		r.ExpiresAt, r.ConnectedBy, r.CreatedAt, r.UpdatedAt))
}

func (s *Server) deleteConnection(c *gin.Context) {
	slug, ok := brandSlugParam(c)
	if !ok {
		return
	}
	provider, ok := providerParam(c)
	if !ok {
		return
	}
	rows, err := s.store.Q.DeleteConnection(c.Request.Context(), gen.DeleteConnectionParams{
		BrandSlug: slug, Provider: provider,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not remove the connection.")
		return
	}
	if rows == 0 {
		detail(c, http.StatusNotFound, "No connection to remove for this provider.")
		return
	}
	c.Status(http.StatusNoContent)
}

// syncShopifyOrders schedules an import of the brand's Shopify orders.
//
// Scheduled, not performed: the pull is minutes of work - up to 2000 orders,
// each a database write - and it used to run right here, on the request
// context. The browser gives up after five seconds (TIMEOUT_MS in the
// frontend's connections.service.ts), Gin cancels the context when it does, and
// every import still to come failed.
//
// It failed silently, which is what made it costly. Per-order failures are
// logged and skipped by design, so the handler returned {"imported": 4} and
// looked like a success while thirty-five orders in the middle of the range
// never arrived. Orders 114775-114809 were missing from the Orders page for
// exactly this reason.
//
// Re-running is safe either way: each order upserts by shopify_order_id.
func (s *Server) syncShopifyOrders(c *gin.Context) {
	slug, ok := brandSlugParam(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	conn, err := s.store.Q.GetConnectionWithToken(ctx, gen.GetConnectionWithTokenParams{
		BrandSlug: slug, Provider: shopifyProvider,
	})
	// Checked here rather than left to the worker so pressing Sync on a brand
	// with no store still says so immediately, instead of appearing to start
	// work that quietly does nothing.
	if _, _, connected := shopifyCredentials(conn, err); !connected {
		detail(c, http.StatusConflict, "This brand's Shopify isn't connected yet.")
		return
	}
	s.startOrderSync(c, slug)
}

// startOrderSync hands a pull to the worker and reports that it started.
//
// brandSlug empty means every connected store.
func (s *Server) startOrderSync(c *gin.Context, brandSlug string) {
	ctx := c.Request.Context()
	if s.orderSyncEnqueuer == nil {
		detail(c, http.StatusServiceUnavailable,
			"The order sync worker isn't running, so orders can't be fetched right now.")
		return
	}
	if err := s.orderSyncEnqueuer.Enqueue(ctx, brandSlug); err != nil {
		obs.FromContext(ctx).Error("could not schedule a shopify order sync",
			"brand", brandSlug, "error", err)
		detail(c, http.StatusInternalServerError, "Could not start the Shopify sync.")
		return
	}
	// 202, and no count: the import has not happened yet, and an "imported"
	// number here could only be a guess. The page refreshes to show what
	// actually landed.
	c.JSON(http.StatusAccepted, gin.H{"started": true})
}
