package httpapi

import (
	"context"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// fetchAndImportShopifyOrders pulls the store's most recently created orders
// (every financial status - a COD order sitting at "pending" is still a real
// order) and imports each one, the same way a live webhook delivery would.
// Shared by backfillShopifyOrders (the one-time, best-effort catch-up right
// after a shopify_connections OAuth grant completes) and syncShopifyOrders in
// connections.go (the on-demand sync off a brand's already-connected
// brand_connections row, with no shopify_connections row to reference - hence
// connID is nullable). A per-order import failure is logged and skipped
// rather than aborting the whole sync; only a failure to even list orders
// from Shopify is returned to the caller.
func (s *Server) fetchAndImportShopifyOrders(ctx context.Context, connID *uuid.UUID, shop, token string) (int, error) {
	orders, err := s.shopify.ListRecentOrders(ctx, shop, token, 0)
	if err != nil {
		return 0, err
	}
	imported := 0
	for _, o := range orders {
		if _, err := s.importShopifyOrder(ctx, connID, toOrderPayload(o), "pending"); err != nil {
			obs.FromContext(ctx).Warn("shopify order sync: import failed",
				"shop", shop, "shopify_order_id", o.ID, "error", err)
			continue
		}
		imported++
	}
	return imported, nil
}

// backfillShopifyOrders does a one-time catch-up import of the store's most
// recently created orders right after it connects: webhooks only fire for
// events from that point forward, they never replay history, so without this
// an order placed before the connection existed would simply never show up.
// Best-effort - the store is already fully connected once its webhooks are
// registered, so a failure here is logged and swallowed rather than failing
// the OAuth callback; future orders keep flowing in regardless.
func (s *Server) backfillShopifyOrders(ctx context.Context, connID *uuid.UUID, shop, token string) {
	imported, err := s.fetchAndImportShopifyOrders(ctx, connID, shop, token)
	if err != nil {
		obs.FromContext(ctx).Warn("shopify order backfill: could not list orders", "shop", shop, "error", err)
		return
	}
	obs.FromContext(ctx).Info("shopify order backfill complete", "shop", shop, "imported", imported)
}

// toOrderPayload adapts a GraphQL OrderSummary into the payload shape
// importShopifyOrder consumes. It kept the REST webhook payload's field names
// when both paths existed; the pull is now the only path.
func toOrderPayload(o shopify.OrderSummary) shopifyOrderPayload {
	p := shopifyOrderPayload{
		ID: o.ID, Name: o.Name, FinancialStatus: o.FinancialStatus,
		TotalPrice: o.TotalPrice, Currency: o.Currency,
	}
	if o.Customer != nil {
		p.Customer = &shopifyCustomer{
			ID: o.Customer.ID, FirstName: o.Customer.FirstName, LastName: o.Customer.LastName,
			Email: o.Customer.Email, Phone: o.Customer.Phone,
		}
	}
	p.LineItems = make([]shopifyLineItem, 0, len(o.LineItems))
	for _, li := range o.LineItems {
		item := shopifyLineItem{
			ProductID: li.ProductID, SKU: li.SKU, Name: li.Name, Title: li.Title, Quantity: li.Quantity,
		}
		for _, prop := range li.Properties {
			item.Properties = append(item.Properties, shopifyLineProp{Name: prop.Name, Value: prop.Value})
		}
		p.LineItems = append(p.LineItems, item)
	}
	return p
}
