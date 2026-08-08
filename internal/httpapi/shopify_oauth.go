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

func (s *Server) createCallbackURL() string {
	return s.cfg.PublicBaseURL + "/webhooks/shopify/orders-create"
}

// shopifyAuthorize starts the order-import OAuth flow for one brand's
// Integrations page. brand is carried through the signed state purely so the
// callback can send the browser back to where it started -
// shopify_connections itself has no brand association (see the doc comment
// on shopifyCallback's redirect below).
func (s *Server) shopifyAuthorize(c *gin.Context) {
	if !s.shopifyReady(c) {
		return
	}
	shop := c.Query("shop_domain")
	if !shopify.ValidShopDomain(shop) {
		detail(c, http.StatusUnprocessableEntity, "A valid myshopify.com shop_domain is required.")
		return
	}
	brand := c.Query("brand")
	if !slugPattern.MatchString(brand) {
		detail(c, http.StatusUnprocessableEntity, "A valid brand is required.")
		return
	}
	if !s.brandMustExist(c, brand) {
		return
	}
	state := shopify.SignState(shop, brand, uuid.NewString(), s.cfg.ShopifyClientSecret, time.Now().Add(oauthStateTTL))
	redirectURI := s.cfg.PublicBaseURL + "/integrations/shopify/oauth/callback"
	url := shopify.AuthorizeURL(shop, s.cfg.ShopifyClientID, shopify.OrderImportScopes, redirectURI, state)
	c.Redirect(http.StatusFound, url)
}

// shopifyIntegrationsURL is where a brand's order-import connection attempt
// ends up, success or failure - the Integrations page reads the
// shopify_orders query param and shows a message. Falls back to the bare
// dashboard when brand isn't known yet (an early failure before state -
// carrying the brand - was verified, e.g. a bad HMAC).
func (s *Server) shopifyIntegrationsURL(brand, status string) string {
	if brand == "" {
		return s.cfg.FrontendURL + "/dashboard?shopify_orders=" + status
	}
	return s.cfg.FrontendURL + "/dashboard/" + brand + "/integrations?shopify_orders=" + status
}

// shopifyCallback handles Shopify's OAuth redirect: verify the query HMAC and
// state, exchange the code, register the orders/paid + orders/create
// webhooks, and store the encrypted token. Any failure bounces back to the
// brand's Integrations page with ?shopify_orders=error.
func (s *Server) shopifyCallback(c *gin.Context) {
	// brand is filled in once state verifies below; every fail() call before
	// that point necessarily redirects to the brand-less fallback.
	var brand string
	fail := func() { c.Redirect(http.StatusFound, s.shopifyIntegrationsURL(brand, "error")) }
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
	state := query.Get("state")
	if state == "" {
		fail()
		return
	}
	stateShop, stateBrand, ok := shopify.VerifyState(state, s.cfg.ShopifyClientSecret, time.Now())
	if !ok || stateShop != shop {
		fail()
		return
	}
	brand = stateBrand
	ctx := c.Request.Context()

	if _, err := s.store.Q.GetActiveConnectionByDomain(ctx, shop); err == nil {
		// Already connected; treat as success rather than a hard error.
		c.Redirect(http.StatusFound, s.shopifyIntegrationsURL(brand, "connected"))
		return
	}

	token, scopes, err := s.shopify.ExchangeCode(ctx, shop, s.cfg.ShopifyClientID, s.cfg.ShopifyClientSecret, code)
	if err != nil {
		obs.FromContext(ctx).Error("shopify oauth exchange failed", "error", err)
		fail()
		return
	}
	subID, err := s.shopify.RegisterOrdersPaidWebhook(ctx, shop, token, s.callbackURL())
	if err != nil {
		obs.FromContext(ctx).Error("shopify webhook registration failed", "error", err)
		fail()
		return
	}
	// Also registered, but its subscription id isn't persisted (the schema has
	// one webhook_subscription_id column, sized for the original orders/paid
	// integration) - a store disconnect won't auto-unregister this one from
	// Shopify. ORDERS_CREATE is what catches a COD order, which may sit at
	// financial_status "pending" indefinitely and would otherwise never fire
	// ORDERS_PAID.
	if _, err := s.shopify.RegisterOrdersCreateWebhook(ctx, shop, token, s.createCallbackURL()); err != nil {
		obs.FromContext(ctx).Error("shopify orders/create webhook registration failed", "error", err)
		fail()
		return
	}
	sealed, err := s.secrets.Seal(token)
	if err != nil {
		fail()
		return
	}
	conn, err := s.store.Q.InsertShopifyConnection(ctx, gen.InsertShopifyConnectionParams{
		ID: uuid.New(), ShopDomain: shop, EncryptedAccessToken: sealed,
		Scopes: nonEmptyPtr(scopes), WebhookSubscriptionID: nonEmptyPtr(subID),
	})
	if err != nil {
		obs.FromContext(ctx).Error("shopify connection insert failed", "error", err)
		fail()
		return
	}
	// Best-effort catch-up on orders placed before this connection existed -
	// the webhooks just registered above only fire for events from now on.
	s.backfillShopifyOrders(ctx, conn.ID, shop, token)
	c.Redirect(http.StatusFound, s.shopifyIntegrationsURL(brand, "connected"))
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
