package httpapi

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// shopDomainPattern matches a Shopify store's canonical admin domain, e.g.
// "your-store.myshopify.com". The publish/catalog calls build the Admin API URL
// as https://<domain>/admin/api/..., so anything that is not a myshopify.com
// domain (a bare email, a storefront URL) silently sends requests to the wrong
// host and fails later with an opaque 404. Validating here stops it at the door.
var shopDomainPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\.myshopify\.com$`)

// normalizeShopDomain lowercases and strips a pasted scheme/trailing slash, then
// checks the result is a myshopify.com domain. Returns ok=false when it is not.
func normalizeShopDomain(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimSuffix(s, "/")
	if !shopDomainPattern.MatchString(s) {
		return "", false
	}
	return s, true
}

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

	// A connected Shopify brand must carry a real *.myshopify.com store domain in
	// external_account_id - the publish/catalog calls turn it into the Admin API
	// host. Reject (and normalize) anything else so a bad value can never be saved.
	if provider == shopifyProvider && req.Status == "connected" {
		domain, ok := normalizeShopDomain(deref(req.ExternalAccountID))
		if !ok {
			detail(c, http.StatusUnprocessableEntity,
				"The Shopify store domain must look like your-store.myshopify.com.")
			return
		}
		req.ExternalAccountID = &domain
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
