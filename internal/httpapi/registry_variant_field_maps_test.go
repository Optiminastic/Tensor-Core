package httpapi

// A SKU's own mapping, and the product-level mapping beside it.
//
// `product_field_maps` holds both: a row with variant_id NULL is the product's
// default and a row carrying one is a SKU's override, and `fieldMapsFor`
// returns the override WHOLE when it exists or the default otherwise. The
// column has existed since migration 0129 and the render path has honoured it
// all along.
//
// What never existed is the care each of those two writers has to take about
// the other's rows. They share a table, and both are "clear this role, write
// it again" - so a save that forgets to say which kind it is deleting takes
// the other kind with it. That is what this file is about, and the first test
// here failed before ClearProductFieldMaps was scoped.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
)

// seedProductWithVariant makes the smallest registry a mapping can hang off.
func seedProductWithVariant(
	t *testing.T, store *db.Store, code, sku string,
) (productID, variantID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	productID, variantID = uuid.New(), uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO products (id, code, name) VALUES ($1, $2, $2)`,
		productID, code); err != nil {
		t.Fatalf("product: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO product_variants (id, product_id, name, sku) VALUES ($1, $2, $3, $3)`,
		variantID, productID, sku); err != nil {
		t.Fatalf("variant: %v", err)
	}
	return productID, variantID
}

// countFieldMaps counts the rows of one kind: a SKU's own when variantID is
// given, the product's default when it is nil.
func countFieldMaps(t *testing.T, store *db.Store, productID uuid.UUID, variantID *uuid.UUID) int {
	t.Helper()
	var n int
	err := store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM product_field_maps
		 WHERE product_id = $1 AND variant_id IS NOT DISTINCT FROM $2`,
		productID, variantID).Scan(&n)
	if err != nil {
		t.Fatalf("count field maps: %v", err)
	}
	return n
}

func TestIntegrationSavingAProductMappingKeepsEachSKUsOwn(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key(), auth.ConfigManage.Key()})

	productID, variantID := seedProductWithVariant(t, store, "PWDH", "PWDH-P")

	// The product's default, written the ordinary way.
	if rr := doJSON(router, http.MethodPut,
		"/registry/products/PWDH/field-maps?role=body", token, map[string]any{
			"maps": []map[string]any{
				{"property_key": "name", "scad_variable": "TEXT", "value_type": "string"},
			},
		}); rr.Code != http.StatusNoContent {
		t.Fatalf("put product maps = %d %s", rr.Code, rr.Body.String())
	}

	// This SKU's own, inserted directly: the route that writes these is built
	// in a later slice, and the rule under test is about the rows, not the
	// door they came through.
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO product_field_maps
		   (id, product_id, variant_id, property_key, scad_variable, value_type, role,
		    required, position)
		 VALUES ($1, $2, $3, 'name', 'TEXT', 'string', 'body', true, 0)`,
		uuid.New(), productID, variantID); err != nil {
		t.Fatalf("variant map: %v", err)
	}

	if n := countFieldMaps(t, store, productID, &variantID); n != 1 {
		t.Fatalf("the SKU has %d mappings before the product is saved, want 1", n)
	}

	// SAVING THE PRODUCT'S MAPPING AGAIN. This is the ordinary act that was
	// destructive: putProductFieldMaps clears the role before rewriting it,
	// and an unscoped clear takes every SKU's override with it - silently, and
	// a render then falls back to the product's mapping for a SKU that was
	// deliberately configured differently.
	if rr := doJSON(router, http.MethodPut,
		"/registry/products/PWDH/field-maps?role=body", token, map[string]any{
			"maps": []map[string]any{
				{"property_key": "name", "scad_variable": "TEXT", "value_type": "string"},
			},
		}); rr.Code != http.StatusNoContent {
		t.Fatalf("second put = %d %s", rr.Code, rr.Body.String())
	}

	if n := countFieldMaps(t, store, productID, &variantID); n != 1 {
		t.Errorf("the SKU has %d mappings after the product was saved, want its own 1 kept", n)
	}
	if n := countFieldMaps(t, store, productID, nil); n != 1 {
		t.Errorf("the product has %d default mappings, want 1", n)
	}
}

func TestIntegrationTheProductMappingIsReadWithoutTheSKUsOwn(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key(), auth.ConfigManage.Key()})

	productID, variantID := seedProductWithVariant(t, store, "PDHR", "PDHR-P")

	// One of each kind, naming DIFFERENT variables so the editor's list would
	// visibly carry the wrong one if the read were unscoped.
	for _, row := range []struct {
		variant  *uuid.UUID
		variable string
	}{{nil, "TEXT"}, {&variantID, "HEADLINE"}} {
		if _, err := store.Pool.Exec(ctx,
			`INSERT INTO product_field_maps
			   (id, product_id, variant_id, property_key, scad_variable, value_type, role,
			    required, position)
			 VALUES ($1, $2, $3, 'name', $4, 'string', 'body', true, 0)`,
			uuid.New(), productID, row.variant, row.variable); err != nil {
			t.Fatalf("map %s: %v", row.variable, err)
		}
	}

	// The product editor shows the PRODUCT's mapping. A SKU's override is not
	// the product's default and listing it here would invite somebody to save
	// it as one.
	rr := doJSON(router, http.MethodGet,
		"/registry/products/PDHR/field-maps?role=body", token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get = %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "TEXT") {
		t.Errorf("the product's own mapping is missing: %s", body)
	}
	if strings.Contains(body, "HEADLINE") {
		t.Errorf("a SKU's override leaked into the product's mapping: %s", body)
	}
}

func TestIntegrationASKUsOverrideIsWrittenAndWinsWhole(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key(), auth.ConfigManage.Key()})
	s := registryServer(t, store)

	productID, variantID := seedProductWithVariant(t, store, "PWDH", "PWDH-P")

	// The product's default: one field.
	if rr := doJSON(router, http.MethodPut,
		"/registry/products/PWDH/field-maps?role=body", token, map[string]any{
			"maps": []map[string]any{
				{"property_key": "name", "scad_variable": "TEXT", "value_type": "string"},
			},
		}); rr.Code != http.StatusNoContent {
		t.Fatalf("product maps = %d %s", rr.Code, rr.Body.String())
	}

	// This SKU's own: a DIFFERENT variable, so "whole, not merged" is visible.
	path := "/registry/variants/" + variantID.String() + "/field-maps?role=body"
	if rr := doJSON(router, http.MethodPut, path, token, map[string]any{
		"maps": []map[string]any{
			{
				"property_key": "name", "scad_variable": "HEADLINE",
				"value_type": "string", "max_runes": 16,
			},
		},
	}); rr.Code != http.StatusNoContent {
		t.Fatalf("variant maps = %d %s", rr.Code, rr.Body.String())
	}

	// WHAT THE RENDERER WILL USE. The override whole - not the two fields
	// merged, which would be a mapping that exists in neither place.
	got := s.fieldMapsFor(ctx, productID, "PWDH-P", "body")
	if len(got) != 1 || got[0].ScadVariable != "HEADLINE" {
		t.Fatalf("fieldMapsFor = %v, want exactly the SKU's own HEADLINE", got)
	}
	if got[0].MaxRunes == nil || *got[0].MaxRunes != 16 {
		t.Error("the override's character limit did not survive the write")
	}

	// And the product's own row is still there, untouched, for its siblings.
	if n := countFieldMaps(t, store, productID, nil); n != 1 {
		t.Errorf("the product has %d default mappings after a SKU override, want 1", n)
	}

	// Removing the override returns the SKU to the product's mapping.
	if rr := doJSON(router, http.MethodDelete, path, token, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("delete override = %d %s", rr.Code, rr.Body.String())
	}
	after := s.fieldMapsFor(ctx, productID, "PWDH-P", "body")
	if len(after) != 1 || after[0].ScadVariable != "TEXT" {
		t.Errorf("fieldMapsFor = %v after removing the override, want the product's TEXT", after)
	}
}

func TestIntegrationAnEmptyOverrideIsRefusedRatherThanStored(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.ConfigManage.Key()})

	_, variantID := seedProductWithVariant(t, store, "PWDH", "PWDH-W")

	// Stored, an empty override would read back as "no override" - the same
	// state as never having set one - so saving it would mean the opposite of
	// what it looks like.
	rr := doJSON(router, http.MethodPut,
		"/registry/variants/"+variantID.String()+"/field-maps?role=body", token,
		map[string]any{"maps": []map[string]any{}})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("an empty override = %d %s, want 422", rr.Code, rr.Body.String())
	}
}

func TestIntegrationSavingOneSKUsOverrideLeavesItsSiblingsAlone(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.ConfigManage.Key()})

	productID, purple := seedProductWithVariant(t, store, "PWDH", "PWDH-P")
	red := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO product_variants (id, product_id, name, sku) VALUES ($1, $2, 'Red', 'PWDH-R')`,
		red, productID); err != nil {
		t.Fatalf("sibling: %v", err)
	}

	for _, v := range []uuid.UUID{purple, red} {
		if rr := doJSON(router, http.MethodPut,
			"/registry/variants/"+v.String()+"/field-maps?role=body", token, map[string]any{
				"maps": []map[string]any{
					{"property_key": "name", "scad_variable": "TEXT", "value_type": "string"},
				},
			}); rr.Code != http.StatusNoContent {
			t.Fatalf("override for %s = %d %s", v, rr.Code, rr.Body.String())
		}
	}

	// Saving the second must not clear the first: they share a table and a
	// product id, and only the variant tells them apart.
	if n := countFieldMaps(t, store, productID, &purple); n != 1 {
		t.Errorf("the first SKU has %d mappings after its sibling was saved, want 1", n)
	}
	if n := countFieldMaps(t, store, productID, &red); n != 1 {
		t.Errorf("the second SKU has %d mappings, want 1", n)
	}
}

func TestIntegrationPointingASKUAtAFileThatDoesNotExistIsRefused(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.ConfigManage.Key()})

	_, variantID := seedProductWithVariant(t, store, "PWDH", "PWDH-P")

	// A typo used to be accepted here and found at render time, on a
	// customer's order, hours later. Its sibling route has always refused it.
	rr := doJSON(router, http.MethodPut, "/registry/variants/"+variantID.String()+"/design",
		token, map[string]any{"role": "body", "template_key": "pwdh_purpel"})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown template = %d %s, want 422", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "pwdh_purpel") {
		t.Errorf("the refusal does not name the file: %s", body)
	}
}

func TestIntegrationASKUWithNoMappingYetCanStillBeGivenAFile(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.ConfigManage.Key()})

	_, variantID := seedProductWithVariant(t, store, "PWDH", "PWDH-W")

	// THE TEST THAT KEEPS THE REGISTRY USABLE. A brand-new product has no
	// mapping and no colour scheme, and refusing a file until it has both
	// would force one order of operations on somebody setting it up. The
	// readiness panel is what reports a half-finished product.
	//
	// dnp_two_heart is a template this binary ships with, so it exists
	// without anything being uploaded.
	rr := doJSON(router, http.MethodPut, "/registry/variants/"+variantID.String()+"/design",
		token, map[string]any{"role": "body", "template_key": "dnp_two_heart"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("assigning a file before anything else = %d %s, want 204",
			rr.Code, rr.Body.String())
	}
}
