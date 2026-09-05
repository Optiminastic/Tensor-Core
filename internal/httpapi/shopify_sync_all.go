package httpapi

// POST /connections/shopify/sync-all - pull orders for every connected store.
//
// The Orders page can be scoped to one brand or to "All brands", and the
// all-brands view is the one people actually work from. Its slug is a
// sentinel ("all"), not a real brand, so the per-brand sync route had no
// connection to look up and the button sat permanently disabled - the page
// where an operator most expects to press Sync was the one place it did
// nothing.
//
// Like the per-brand sync this now schedules the pull rather than running it on
// the request; see syncShopifyOrders in connections.go for what running it there
// cost. Spanning every store made that worse, not better - each store's orders
// were imported one after another inside a request the browser abandoned after
// five seconds.

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/obs"
)

func (s *Server) syncAllShopifyOrders(c *gin.Context) {
	ctx := c.Request.Context()

	// Read first so "no store is connected" is still an immediate answer
	// rather than a job that starts and finds nothing to do.
	conns, err := s.store.Q.ListConnectedShopifyBrands(ctx)
	if err != nil {
		obs.FromContext(ctx).Error("could not list connected shopify stores", "error", err)
		detail(c, http.StatusInternalServerError, "Could not read the Shopify connections.")
		return
	}
	if len(conns) == 0 {
		detail(c, http.StatusConflict, "No brand has Shopify connected yet.")
		return
	}

	// An empty slug is the worker's "every connected store".
	s.startOrderSync(c, "")
}
