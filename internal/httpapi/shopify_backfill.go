package httpapi

import (
	"context"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// backfillShopifyOrders does a one-time catch-up import of the store's most
// recently created orders right after it connects: webhooks only fire for
// events from that point forward, they never replay history, so without this
// an order placed before the connection existed would simply never show up.
// Best-effort - the store is already fully connected once its webhooks are
// registered, so a failure here is logged and swallowed rather than failing
// the OAuth callback; future orders keep flowing in regardless.
func (s *Server) backfillShopifyOrders(ctx context.Context, connID uuid.UUID, shop, token string) {
	orders, err := s.shopify.ListRecentOrders(ctx, shop, token, 0)
	if err != nil {
		obs.FromContext(ctx).Warn("shopify order backfill: could not list orders", "shop", shop, "error", err)
		return
	}
	imported := 0
	for _, o := range orders {
		if _, err := s.importShopifyOrder(ctx, connID, toWebhookPayload(o), "pending"); err != nil {
			obs.FromContext(ctx).Warn("shopify order backfill: import failed",
				"shop", shop, "shopify_order_id", o.ID, "error", err)
			continue
		}
		imported++
	}
	obs.FromContext(ctx).Info("shopify order backfill complete", "shop", shop, "found", len(orders), "imported", imported)
}

// toWebhookPayload adapts a GraphQL OrderSummary (backfill) into the same
// shape a live REST webhook delivery parses into, so both paths go through
// importShopifyOrder identically.
func toWebhookPayload(o shopify.OrderSummary) shopifyOrderPayload {
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
