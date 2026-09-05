package httpapi

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// shopifyImportSince is the earliest order date Tensor pulls from Shopify.
//
// 26 August 2026 is the Wednesday the current production run began; orders
// before it belong to an earlier way of working and would arrive without the
// personalisation properties the pipeline now expects, creating jobs nobody
// means to print. Everything on or after it is fair game, so pressing Sync
// stays "fetch all the latest orders" - the floor only stops the pull walking
// backwards through history it has no use for.
//
// A constant rather than config because it marks a specific event, not a
// preference. Moving it is a deliberate decision with a date attached.
var shopifyImportSince = time.Date(2026, time.August, 26, 0, 0, 0, 0, time.UTC)

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
	orders, err := s.shopify.ListRecentOrders(ctx, shop, token, 0, shopifyImportSince)
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
		CreatedAt: o.CreatedAt, Note: o.Note, Tags: o.Tags,
		FulfillmentStatus: o.FulfillmentStatus, SourceName: o.SourceName,
		DeliveryStatus: o.DeliveryStatus, ReturnStatus: o.ReturnStatus,
		SubtotalPrice: o.SubtotalPrice, TotalDiscounts: o.TotalDiscounts,
		TotalShipping: o.TotalShipping, TotalReceived: o.TotalReceived,
		DiscountTitle: o.DiscountTitle, ShippingTitle: o.ShippingTitle,
		Email: o.Email, Phone: o.Phone,
		ShippingAddress: o.ShippingAddress, BillingAddress: o.BillingAddress,
	}
	for _, a := range o.Attributes {
		p.Attributes = append(p.Attributes, shopifyLineProp{Name: a.Name, Value: a.Value})
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
			ProductID: li.ProductID, SKU: li.SKU, Name: li.Name, Title: li.Title,
			Quantity: li.Quantity, VariantTitle: li.VariantTitle, ImageURL: li.ImageURL,
			UnitPrice: li.UnitPrice, DiscountedUnitPrice: li.DiscountedUnitPrice,
			LineTotal: li.LineTotal,
		}
		for _, prop := range li.Properties {
			item.Properties = append(item.Properties, shopifyLineProp{Name: prop.Name, Value: prop.Value})
		}
		p.LineItems = append(p.LineItems, item)
	}
	return p
}

// SyncOrdersForBrand pulls and imports one store's recent orders.
//
// Exported so a maintenance command can run exactly what the Sync button runs.
// A backfill driven by a different code path is a backfill that proves nothing
// about the button, which is the only way orders normally arrive.
func (s *Server) SyncOrdersForBrand(ctx context.Context, shop, token string) (int, error) {
	return s.fetchAndImportShopifyOrders(ctx, nil, shop, token)
}
