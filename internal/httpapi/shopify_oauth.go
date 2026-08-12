package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// oauthStateTTL bounds how long a Shopify OAuth flow may stay in-flight.
const oauthStateTTL = 10 * time.Minute

func (s *Server) registerShopify(r *gin.Engine) {
	g := r.Group("/integrations/shopify")
	// The callback is Shopify-driven: it authenticates with the query HMAC, not a
	// user token. Everything else is admin-only (integration:manage).
	g.GET("/oauth/callback", s.shopifyCallback)
	manage := []gin.HandlerFunc{s.guards.RequireUser(), s.guards.RequirePermission(auth.IntegrationManage.Key())}
	g.GET("/authorize", append(manage, s.shopifyAuthorize)...)
	g.GET("", append(manage, s.listShopifyConnections)...)
	g.DELETE("/:id", append(manage, s.deleteShopifyConnection)...)
}

// shopifyReady fails closed when the inbound Shopify config is incomplete.
func (s *Server) shopifyReady(c *gin.Context) bool {
	if !s.cfg.ShopifyImportConfigured() || s.secrets == nil {
		detail(c, http.StatusServiceUnavailable, "The Shopify integration is not configured.")
		return false
	}
	return true
}

func (s *Server) callbackURL() string { return s.cfg.PublicBaseURL + "/webhooks/shopify/orders-paid" }

func (s *Server) shopifyAuthorize(c *gin.Context) {
	if !s.shopifyReady(c) {
		return
	}
	shop := c.Query("shop_domain")
	if !shopify.ValidShopDomain(shop) {
		detail(c, http.StatusUnprocessableEntity, "A valid myshopify.com shop_domain is required.")
		return
	}
	state := shopify.SignState(shop, "", uuid.NewString(), s.cfg.ShopifyClientSecret, time.Now().Add(oauthStateTTL))
	redirectURI := s.cfg.PublicBaseURL + "/integrations/shopify/oauth/callback"
	url := shopify.AuthorizeURL(shop, s.cfg.ShopifyClientID, shopify.OrderImportScopes, redirectURI, state)
	c.Redirect(http.StatusFound, url)
}

type shopifyAuthorizeResponse struct {
	AuthorizeURL string `json:"authorize_url"`
}

// shopifyBrandAuthorizeURL builds the OAuth authorize URL for connecting one
// brand's store (with publish scopes). The browser cannot call this guarded
// endpoint directly, so the frontend fetches the URL with the admin's token and
// redirects the browser to it; the callback finishes the connect for that brand.
func (s *Server) shopifyBrandAuthorizeURL(c *gin.Context) {
	if !s.shopifyReady(c) {
		return
	}
	slug, ok := brandSlugParam(c)
	if !ok {
		return
	}
	if !s.brandMustExist(c, slug) {
		return
	}
	shop := c.Query("shop_domain")
	if !shopify.ValidShopDomain(shop) {
		detail(c, http.StatusUnprocessableEntity, "A valid myshopify.com shop_domain is required.")
		return
	}
	state := shopify.SignState(shop, slug, uuid.NewString(), s.cfg.ShopifyClientSecret, time.Now().Add(oauthStateTTL))
	redirectURI := s.cfg.PublicBaseURL + "/integrations/shopify/oauth/callback"
	url := shopify.AuthorizeURL(shop, s.cfg.ShopifyClientID, shopify.BrandConnectScopes, redirectURI, state)
	c.JSON(http.StatusOK, shopifyAuthorizeResponse{AuthorizeURL: url})
}

// shopifyCallback handles Shopify's OAuth redirect: verify the query HMAC and
// state, exchange the code, register the orders/paid webhook, and store the
// encrypted token. Any failure bounces to the frontend with ?shopify=error.
func (s *Server) shopifyCallback(c *gin.Context) {
	fail := func() { c.Redirect(http.StatusFound, s.cfg.FrontendURL+"/dashboard/settings?shopify=error") }
	if !s.cfg.ShopifyImportConfigured() || s.secrets == nil {
		fail()
		return
	}
	query := c.Request.URL.Query()
	if !shopify.VerifyCallbackHMAC(query, s.cfg.ShopifyClientSecret) {
		fail()
		return
	}
	shop, code := query.Get("shop"), query.Get("code")
	if shop == "" || code == "" || !shopify.ValidShopDomain(shop) {
		fail()
		return
	}
	// State is always signed now (brand connect carries the brand; order import
	// carries an empty brand), so it is required and must match the shop.
	stateShop, brand, ok := shopify.VerifyState(query.Get("state"), s.cfg.ShopifyClientSecret, time.Now())
	if !ok || stateShop != shop {
		fail()
		return
	}
	ctx := c.Request.Context()

	token, scopes, err := s.shopify.ExchangeCode(ctx, shop, s.cfg.ShopifyClientID, s.cfg.ShopifyClientSecret, code)
	if err != nil {
		obs.FromContext(ctx).Error("shopify oauth exchange failed", "error", err)
		fail()
		return
	}

	if brand != "" {
		s.completeBrandShopifyConnect(c, brand, shop, token)
		return
	}
	s.completeOrderImportConnect(c, shop, token, scopes)
}

// completeBrandShopifyConnect stores the OAuth token as the brand's Shopify
// connection - the exact row the publish/edit path reads via
// GetConnectionWithToken - so a new store publishes with no per-store app. The
// token is stored plaintext to match how that path reads it (like the manual
// token); sealing brand_connections is a separate hardening follow-up.
func (s *Server) completeBrandShopifyConnect(c *gin.Context, brand, shop, token string) {
	ctx := c.Request.Context()
	if _, err := s.store.Q.UpsertConnection(ctx, gen.UpsertConnectionParams{
		ID: uuid.New(), BrandSlug: brand, Provider: shopifyProvider, Status: "connected",
		ExternalAccountID: &shop, AccessToken: &token,
	}); err != nil {
		obs.FromContext(ctx).Error("shopify brand connection upsert failed", "error", err)
		c.Redirect(http.StatusFound, s.cfg.FrontendURL+"/dashboard/"+brand+"/integrations?shopify=error")
		return
	}
	c.Redirect(http.StatusFound, s.cfg.FrontendURL+"/dashboard/"+brand+"/integrations?shopify=connected")
}

// completeOrderImportConnect is the legacy, workspace-wide order-import flow:
// register the orders/paid webhook and store the sealed token in
// shopify_connections (keyed by domain). Unchanged behaviour, extracted so the
// callback can branch cleanly.
func (s *Server) completeOrderImportConnect(c *gin.Context, shop, token, scopes string) {
	ctx := c.Request.Context()
	fail := func() { c.Redirect(http.StatusFound, s.cfg.FrontendURL+"/dashboard/settings?shopify=error") }
	if _, err := s.store.Q.GetActiveConnectionByDomain(ctx, shop); err == nil {
		// Already connected; treat as success rather than a hard error.
		c.Redirect(http.StatusFound, s.cfg.FrontendURL+"/dashboard/settings?shopify=connected")
		return
	}
	subID, err := s.shopify.RegisterOrdersPaidWebhook(ctx, shop, token, s.callbackURL())
	if err != nil {
		obs.FromContext(ctx).Error("shopify webhook registration failed", "error", err)
		fail()
		return
	}
	sealed, err := s.secrets.Seal(token)
	if err != nil {
		fail()
		return
	}
	if _, err := s.store.Q.InsertShopifyConnection(ctx, gen.InsertShopifyConnectionParams{
		ID: uuid.New(), ShopDomain: shop, EncryptedAccessToken: sealed,
		Scopes: nonEmptyPtr(scopes), WebhookSubscriptionID: nonEmptyPtr(subID),
	}); err != nil {
		obs.FromContext(ctx).Error("shopify connection insert failed", "error", err)
		fail()
		return
	}
	c.Redirect(http.StatusFound, s.cfg.FrontendURL+"/dashboard/settings?shopify=connected")
}

type shopifyConnectionResponse struct {
	ID          string    `json:"id"`
	ShopDomain  string    `json:"shop_domain"`
	ConnectedAt time.Time `json:"connected_at"`
}

func (s *Server) listShopifyConnections(c *gin.Context) {
	rows, err := s.store.Q.ListActiveConnections(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list Shopify connections.")
		return
	}
	out := make([]shopifyConnectionResponse, 0, len(rows))
	for _, conn := range rows {
		out = append(out, shopifyConnectionResponse{
			ID: conn.ID.String(), ShopDomain: conn.ShopDomain, ConnectedAt: db.Time(conn.ConnectedAt),
		})
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) deleteShopifyConnection(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	conn, err := s.store.Q.GetConnectionByID(ctx, id)
	if err != nil {
		dbError(c, err, "That connection does not exist.", "Could not load the connection.")
		return
	}
	if !conn.IsActive {
		detail(c, http.StatusNotFound, "That connection is not active.")
		return
	}
	// Best-effort: unregister the webhook, but disconnect regardless of the result.
	if conn.WebhookSubscriptionID != nil && s.secrets != nil {
		if token, err := s.secrets.Open(conn.EncryptedAccessToken); err == nil {
			if err := s.shopify.DeleteWebhook(ctx, conn.ShopDomain, token, *conn.WebhookSubscriptionID); err != nil {
				obs.FromContext(ctx).Warn("shopify webhook delete failed", "error", err)
			}
		}
	}
	if _, err := s.store.Q.DeactivateConnection(ctx, id); err != nil {
		detail(c, http.StatusInternalServerError, "Could not disconnect the store.")
		return
	}
	c.Status(http.StatusNoContent)
}
