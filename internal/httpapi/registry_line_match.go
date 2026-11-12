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
	"encoding/json"
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

// adoptRegistrySKU gives a job with no SKU its registry product's stand-in
// SKU, from the order line it was made from, so Rebuild can render a job
// created before its product was set up.
//
// Job creation matches SKU-less lines through resolveLineSKU; a job created
// before the product existed missed that and was judged "upload a model".
// Rebuild works from the job's SKU, so without this it had nothing to rebuild
// from. Returns the updated job, or ok=false when nothing claims the line -
// the job is then left exactly as it was.
func (s *Server) adoptRegistrySKU(ctx context.Context, job gen.ProductionJob) (gen.ProductionJob, bool) {
	if strings.TrimSpace(deref(job.Sku)) != "" || job.OrderID == nil {
		return job, false
	}
	order, err := s.store.Q.GetOrderByID(ctx, *job.OrderID)
	if err != nil {
		return job, false
	}
	var items []production.LineItem
	if err := json.Unmarshal(order.LineItems, &items); err != nil {
		return job, false
	}
	line, ok := skulessLineFor(items, deref(job.ProductName))
	if !ok {
		return job, false
	}
	resolved := s.resolveLineSKU(ctx, line)
	sku := strings.TrimSpace(deref(resolved.SKU))
	if sku == "" {
		return job, false
	}
	adopted, err := s.store.Q.AdoptRegistrySKU(ctx, gen.AdoptRegistrySKUParams{ID: job.ID, Sku: sku})
	if err != nil {
		return job, false
	}
	obs.FromContext(ctx).Info("job adopted its registry product's SKU on rebuild",
		"job", job.JobNumber, "sku", sku)
	return adopted, true
}

// skulessLineFor is the order line a SKU-less job was made from: the SKU-less
// line with the job's product name, or the only SKU-less line when the job
// records no name.
func skulessLineFor(items []production.LineItem, product string) (production.LineItem, bool) {
	var skuless []production.LineItem
	for _, li := range items {
		if strings.TrimSpace(deref(li.SKU)) == "" {
			skuless = append(skuless, li)
		}
	}
	for _, li := range skuless {
		if product != "" && strings.EqualFold(strings.TrimSpace(li.ProductName), strings.TrimSpace(product)) {
			return li, true
		}
	}
	if product == "" && len(skuless) == 1 {
		return skuless[0], true
	}
	return production.LineItem{}, false
}
