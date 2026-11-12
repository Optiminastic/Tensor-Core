package httpapi

import (
	"context"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/production"
)

func TestShopifyNumericIDIsTheGidsLastSegment(t *testing.T) {
	for in, want := range map[string]string{
		"gid://shopify/Product/8154757038293": "8154757038293",
		"8154757038293":                       "8154757038293",
		" gid://shopify/Product/12 ":          "12",
	} {
		if got := shopifyNumericID(in); got != want {
			t.Errorf("shopifyNumericID(%q) = %q, want %q", in, got, want)
		}
	}
}

// Couple's Date Plank: every variant without a SKU. Its lines are matched by
// Shopify product id to the stand-in variant an import creates.
func TestIntegrationSKULessLinesMatchTheirRegistryProduct(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	exec := func(sql string) {
		t.Helper()
		if _, err := store.Pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO products (id, code, name, shopify_product_id) VALUES
	        ('00000000-0000-0000-0000-0000000000a1', 'COUPLES-DATE-PLANK', 'Couple''s Date Plank', '8154757038293'),
	        ('00000000-0000-0000-0000-0000000000a2', 'OLDPLANK', 'Old Plank', NULL)`)
	exec(`INSERT INTO product_variants (id, product_id, sku, name) VALUES
	        (gen_random_uuid(), '00000000-0000-0000-0000-0000000000a1', 'COUPLES-DATE-PLANK', 'All variants without a SKU'),
	        (gen_random_uuid(), '00000000-0000-0000-0000-0000000000a2', 'OLDPLANK', 'All variants without a SKU')`)
	s := &Server{store: store}
	sku := func(li production.LineItem) string { return deref(s.resolveLineSKU(ctx, li).SKU) }

	// By Shopify product id, even when the storefront has renamed it.
	if got := sku(production.LineItem{ProductID: "8154757038293", ProductName: "Couples Plank (new name)"}); got != "COUPLES-DATE-PLANK" {
		t.Fatalf("by id = %q", got)
	}
	// By name, for a product imported before its id was recorded.
	if got := sku(production.LineItem{ProductID: "999", ProductName: "old plank"}); got != "OLDPLANK" {
		t.Fatalf("by name = %q", got)
	}
	// A line that already has a SKU is never touched.
	real := "DNPWL-RED"
	if got := sku(production.LineItem{SKU: &real, ProductID: "8154757038293"}); got != "DNPWL-RED" {
		t.Fatalf("real SKU = %q", got)
	}
	// Nothing claims it: left exactly as it came.
	if got := s.resolveLineSKU(ctx, production.LineItem{ProductID: "1", ProductName: "Nope"}); got.SKU != nil {
		t.Fatalf("unclaimed = %q", deref(got.SKU))
	}
	// A retired product matches nothing.
	exec(`UPDATE products SET status = 'retired' WHERE code = 'COUPLES-DATE-PLANK'`)
	if got := sku(production.LineItem{ProductID: "8154757038293"}); got != "" {
		t.Fatalf("retired = %q", got)
	}
}
