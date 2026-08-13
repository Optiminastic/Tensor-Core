package httpapi

import (
	"context"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// Shopify's mandatory GDPR compliance webhooks. Every public app must expose all
// three and authenticate each by its body HMAC (401 on a bad signature, 200 on a
// good one). Tensor stores almost no customer PII - only a customer's name on an
// imported order - so data_request/redact acknowledge and log, while shop/redact
// also deactivates the store's connection so its token can no longer be used.
func (s *Server) registerComplianceWebhooks(g *gin.RouterGroup) {
	// Single endpoint for all three compliance topics: the current Shopify app
	// config model subscribes to compliance_topics with one uri and sends the
	// specific topic in the X-Shopify-Topic header. The three per-topic routes
	// below remain for the older per-URL dashboard style.
	g.POST("/compliance", s.complianceWebhook)
	g.POST("/customers-data-request", s.customersDataRequestWebhook)
	g.POST("/customers-redact", s.customersRedactWebhook)
	g.POST("/shop-redact", s.shopRedactWebhook)
}

// complianceWebhook is the single-URL entry point for all mandatory compliance
// webhooks. It verifies the HMAC once, then dispatches on X-Shopify-Topic so one
// configured uri serves customers/data_request, customers/redact and shop/redact.
func (s *Server) complianceWebhook(c *gin.Context) {
	if _, ok := s.readVerifiedWebhook(c); !ok {
		return
	}
	ctx := c.Request.Context()
	topic := c.GetHeader("X-Shopify-Topic")
	shop := c.GetHeader("X-Shopify-Shop-Domain")
	obs.FromContext(ctx).Info("shopify compliance webhook", "topic", topic, "shop", shop)
	if topic == "shop/redact" && shop != "" {
		s.deactivateShopConnection(ctx, shop)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// deactivateShopConnection revokes a store's connection on shop/redact so its
// access token can no longer be used. A missing connection is not an error.
func (s *Server) deactivateShopConnection(ctx context.Context, shop string) {
	conn, err := s.store.Q.GetActiveConnectionByDomain(ctx, shop)
	if err != nil {
		return
	}
	if _, err := s.store.Q.DeactivateConnection(ctx, conn.ID); err != nil {
		obs.FromContext(ctx).Warn("shop/redact deactivate failed", "shop", shop, "error", err)
	}
}

// readVerifiedWebhook reads the raw body and verifies the Shopify HMAC, writing the
// appropriate error and returning ok=false on any failure. The raw bytes matter:
// the HMAC is computed over them exactly, so nothing may re-encode the body first.
func (s *Server) readVerifiedWebhook(c *gin.Context) ([]byte, bool) {
	if s.cfg.ShopifyClientSecret == "" {
		detail(c, http.StatusServiceUnavailable, "The Shopify integration is not configured.")
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookBytes))
	if err != nil {
		detail(c, http.StatusBadRequest, "Could not read the webhook body.")
		return nil, false
	}
	if !shopify.VerifyWebhookHMAC(body, c.GetHeader("X-Shopify-Hmac-SHA256"), s.cfg.ShopifyClientSecret) {
		detail(c, http.StatusUnauthorized, "Invalid webhook signature.")
		return nil, false
	}
	return body, true
}

// customersDataRequestWebhook handles customers/data_request: a store owner asked
// what data we hold for a customer. Tensor holds no persistent customer PII, so we
// verify, log for audit, and acknowledge.
func (s *Server) customersDataRequestWebhook(c *gin.Context) {
	if _, ok := s.readVerifiedWebhook(c); !ok {
		return
	}
	obs.FromContext(c.Request.Context()).Info("shopify compliance webhook",
		"topic", "customers/data_request", "shop", c.GetHeader("X-Shopify-Shop-Domain"))
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// customersRedactWebhook handles customers/redact: erase a customer's data. Tensor
// keeps no persistent customer PII beyond a transient order name, so we verify,
// log, and acknowledge.
func (s *Server) customersRedactWebhook(c *gin.Context) {
	if _, ok := s.readVerifiedWebhook(c); !ok {
		return
	}
	obs.FromContext(c.Request.Context()).Info("shopify compliance webhook",
		"topic", "customers/redact", "shop", c.GetHeader("X-Shopify-Shop-Domain"))
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// shopRedactWebhook handles shop/redact: 48 hours after a store uninstalls, erase
// its data. We deactivate the store's connection so its access token is no longer
// usable, then acknowledge.
func (s *Server) shopRedactWebhook(c *gin.Context) {
	if _, ok := s.readVerifiedWebhook(c); !ok {
		return
	}
	ctx := c.Request.Context()
	shop := c.GetHeader("X-Shopify-Shop-Domain")
	obs.FromContext(ctx).Info("shopify compliance webhook", "topic", "shop/redact", "shop", shop)
	if shop != "" {
		s.deactivateShopConnection(ctx, shop)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
