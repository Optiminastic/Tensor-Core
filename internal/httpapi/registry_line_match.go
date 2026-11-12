package httpapi

// Matching an order line that carries no SKU to its registry product.
//
// The registry finds a product through a SKU, and that is the only key nine
// lookups use. Many storefront products have none - Couple's Date Plank has 64
// variants and not one SKU - so their orders never reached the registry and no
// job of theirs could render from it.
//
// Rather than teach every lookup a second key, the line is given one. An
// import of such a product creates a stand-in variant whose SKU is the product
// code (registry_import.go); here a line with no SKU is matched to that
// product by its Shopify product id, or by its name for a product imported
// before ids were recorded, and takes the stand-in SKU. The job then carries
// it, and the print file and field mapping configured on that variant drive
// the render like any other.

import (
	"context"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// resolveLineSKU returns the line with its registry stand-in SKU filled in
// when it carries none and a registry product claims it. A line that already
// has a SKU, or that no product claims, is returned unchanged - so every line
// that worked before this existed works the same way.
func (s *Server) resolveLineSKU(ctx context.Context, li production.LineItem) production.LineItem {
	if strings.TrimSpace(deref(li.SKU)) != "" {
		return li
	}
	productID := strings.TrimSpace(li.ProductID)
	name := strings.TrimSpace(li.ProductName)
	if productID == "" && name == "" {
		return li
	}
	sku, err := s.store.Q.FindStandInSKUForShopifyLine(ctx, gen.FindStandInSKUForShopifyLineParams{
		ShopifyProductID: productID, ProductName: name,
	})
	if err != nil || strings.TrimSpace(sku) == "" {
		if err != nil && !isNoRows(err) {
			obs.FromContext(ctx).Warn("could not match an SKU-less line to the registry",
				"product_id", productID, "product", name, "error", err)
		}
		return li
	}
	li.SKU = &sku
	return li
}
