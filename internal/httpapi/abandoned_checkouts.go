package httpapi

// Checkouts a customer started and walked away from.
//
// Read-only and fetched LIVE from Shopify rather than stored. There is no state
// to keep yet - nothing here marks one as handled or recovered - so a table and
// a sync job would be a migration and a poller in exchange for staleness. The
// day this page grows a "contacted" column is the day it earns storage, and by
// then the columns will be known rather than guessed.

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

// defaultAbandonedWindowDays bounds the listing by default.
//
// Recent activity is the point: a checkout abandoned two months ago is history,
// not something anybody is about to act on, and fetching it costs a page of
// Shopify's query budget to render a row nobody reads.
const defaultAbandonedWindowDays = 30

func (s *Server) registerAbandonedCheckouts(r *gin.Engine) {
	g := r.Group("/brands/:slug/abandoned-checkouts")
	g.Use(s.guards.RequireUser())
	// order:read. This is order-adjacent data about real customers, and the
	// roles that may see orders are exactly the ones who would act on a cart
	// somebody left behind.
	g.GET("", s.guards.RequirePermission(auth.OrderRead.Key()), s.listAbandonedCheckouts)
}

type abandonedCheckoutsResponse struct {
	Items []shopify.AbandonedCheckout `json:"items"`
	// Shop is named so the page can say WHICH store these came from, which
	// matters the moment a second brand is connected.
	Shop string `json:"shop"`
}

func (s *Server) listAbandonedCheckouts(c *gin.Context) {
	slug := c.Param("slug")

	conn, err := s.store.Q.GetConnectionWithToken(c.Request.Context(),
		gen.GetConnectionWithTokenParams{BrandSlug: slug, Provider: shopifyProvider})
	shop, token, connected := shopifyCredentials(conn, err)
	if !connected {
		// A brand with no store is not an error: it is a brand nobody has
		// connected yet, and the page should say so rather than show a failure.
		detail(c, http.StatusConflict,
			"This brand has no connected Shopify store. Connect one in Settings first.")
		return
	}

	since := time.Now().AddDate(0, 0, -defaultAbandonedWindowDays)
	if raw := c.Query("days"); raw != "" {
		days, convErr := strconv.Atoi(raw)
		if convErr != nil || days <= 0 || days > 180 {
			detail(c, http.StatusUnprocessableEntity,
				"days must be a whole number between 1 and 180.")
			return
		}
		since = time.Now().AddDate(0, 0, -days)
	}

	items, err := s.shopify.ListAbandonedCheckouts(c.Request.Context(), shop, token, 0, since)
	if err != nil {
		// Shopify's own sentence, not a generic failure. Its refusals name the
		// scope or field that was wrong, and that is what the person reading
		// this can act on.
		detail(c, http.StatusBadGateway, err.Error())
		return
	}

	// Never nil: the frontend renders `items.map`, and a null there is an empty
	// state that crashes instead of one that reads "nothing abandoned".
	if items == nil {
		items = []shopify.AbandonedCheckout{}
	}
	c.JSON(http.StatusOK, abandonedCheckoutsResponse{Items: items, Shop: shop})
}
