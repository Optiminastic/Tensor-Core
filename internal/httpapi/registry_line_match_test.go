package httpapi

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"

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

// JOB-116587: a Couple's Date Plank job created before the product was in the
// registry - no SKU, "upload a model". Rebuild gives it the stand-in SKU.
func TestIntegrationRebuildAdoptsTheStandInSKU(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := store.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO products (id, code, name, shopify_product_id) VALUES
	        ('00000000-0000-0000-0000-0000000000a1', 'COUPLES-DATE-PLANK', 'Couple''s Date Plank', '8154757038293')`)
	exec(`INSERT INTO product_variants (id, product_id, sku, name) VALUES
	        (gen_random_uuid(), '00000000-0000-0000-0000-0000000000a1', 'COUPLES-DATE-PLANK', 'All variants without a SKU')`)
	exec(`INSERT INTO orders (id, shopify_order_id, order_number, financial_status, total_price, currency, line_items, status)
	      VALUES ('00000000-0000-0000-0000-0000000000c1', 1, 'T-1', 'paid', 0, 'INR',
	        '[{"sku": null, "product_id": "8154757038293", "product_name": "Couple''s Date Plank", "quantity": 1},
	          {"sku": "DNP-RED", "product_id": "1", "product_name": "Dual Name Plank", "quantity": 1}]', 'imported')`)
	exec(`INSERT INTO production_jobs (id, job_number, description, order_id, sku, product_name, issue_reason)
	      VALUES ('00000000-0000-0000-0000-0000000000d1', 'J-1', 'x', '00000000-0000-0000-0000-0000000000c1',
	              NULL, 'Couple''s Date Plank', 'no_approved_design'),
	             ('00000000-0000-0000-0000-0000000000d2', 'J-2', 'x', '00000000-0000-0000-0000-0000000000c1',
	              'DNP-RED', 'Dual Name Plank', NULL)`)
	s := &Server{store: store}

	job, err := store.Q.GetProductionJobByID(ctx, uuid.MustParse("00000000-0000-0000-0000-0000000000d1"))
	if err != nil {
		t.Fatal(err)
	}
	adopted, ok := s.adoptRegistrySKU(ctx, job)
	if !ok || deref(adopted.Sku) != "COUPLES-DATE-PLANK" || adopted.PersonalisationStatus != "not_required" || !adopted.NameConfirmed {
		t.Fatalf("adopted = %v %+v", ok, adopted)
	}
	// A job that already has a SKU is never touched.
	other, _ := store.Q.GetProductionJobByID(ctx, uuid.MustParse("00000000-0000-0000-0000-0000000000d2"))
	if _, ok := s.adoptRegistrySKU(ctx, other); ok {
		t.Fatal("a job with a SKU was changed")
	}
	// Nothing claims the line: left exactly as it was.
	exec(`UPDATE production_jobs SET sku = NULL WHERE id = '00000000-0000-0000-0000-0000000000d1'`)
	exec(`UPDATE products SET status = 'retired'`)
	job, _ = store.Q.GetProductionJobByID(ctx, uuid.MustParse("00000000-0000-0000-0000-0000000000d1"))
	if _, ok := s.adoptRegistrySKU(ctx, job); ok {
		t.Fatal("adopted a SKU from a retired product")
	}
}

func TestSameLineItemMatchesASKULessLineByName(t *testing.T) {
	name := "Couple's Date Plank"
	real := "DNP-RED"
	if !sameLineItem(production.LineItem{ProductName: name}, "COUPLES-DATE-PLANK", name) {
		t.Fatal("a SKU-less line did not match its stand-in job")
	}
	if sameLineItem(production.LineItem{SKU: &real, ProductName: name}, "COUPLES-DATE-PLANK", name) {
		t.Fatal("a line with a different SKU matched")
	}
}

// 3D Name Plank: SKU-less lines whose names carry their options. The mapping
// editor finds them by Shopify product id.
func TestSameShopifyProductFindsSKULessLines(t *testing.T) {
	id := "7672418959573"
	p := gen.Product{Name: "3D Name Plank", ShopifyProductID: &id}
	line := production.LineItem{ProductID: id, ProductName: "3D Name Plank - Regular - 3 cm height / RED / BLUE"}
	if matchesProduct(line, p.Name, nil) {
		t.Fatal("the name rule now matches - this test no longer proves anything")
	}
	if !sameShopifyProduct(line, p) {
		t.Fatal("a line of the same Shopify product did not match")
	}
	if sameShopifyProduct(production.LineItem{ProductID: "1"}, p) || sameShopifyProduct(line, gen.Product{Name: p.Name}) {
		t.Fatal("matched without a shared Shopify product id")
	}
}
