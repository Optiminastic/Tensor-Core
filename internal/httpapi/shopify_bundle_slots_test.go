package httpapi

// A pack sold as a bundle of numbered slots of ONE product - a Photo Fridge
// Magnet FIVE arrives as PFM-1 ... PFM-5 - keeps on each line only the fields
// that slot's own mapping reads, even before the product has a design file.

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
)

func TestIntegrationEachBundleSlotKeepsOnlyItsOwnFields(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	productID, slot1 := seedProductWithVariant(t, store, "PFMS", "PFM-1")
	slot2 := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO product_variants (id, product_id, name, sku) VALUES ($1, $2, 'PFM-2', 'PFM-2')`,
		slot2, productID); err != nil {
		t.Fatalf("variant: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO product_parts (id, product_id, role, per_variant) VALUES ($1, $2, 'magnet', false)`,
		uuid.New(), productID); err != nil {
		t.Fatalf("part: %v", err)
	}
	mapSlot := func(variantID uuid.UUID, keys ...string) {
		for i, key := range keys {
			if _, err := store.Pool.Exec(ctx, `
				INSERT INTO product_field_maps
				       (id, product_id, variant_id, role, property_key, scad_variable, value_type, required, position)
				VALUES ($1, $2, $3, 'magnet', $4, $5, 'string', true, $6)`,
				uuid.New(), productID, variantID, key, []string{"colour", "photo", "text"}[i], i); err != nil {
				t.Fatalf("map %s: %v", key, err)
			}
		}
	}
	mapSlot(slot1, "colour 1", "photo 1", "text 1")
	mapSlot(slot2, "colour 2", "photo 2", "text 2")

	s := NewServer(config.Settings{Environment: "development", AuthAudience: "tensor-core"},
		store, auth.NewGuards(newTokenMinter(t).verifier, ""), nil)

	pack := []shopifyLineProp{
		{Name: "_has_gpo", Value: "1091733"},
		{Name: "COLOUR - 1", Value: "Red & White"},
		{Name: "PHOTO - 1", Value: "https://example.com/1.png"},
		{Name: "TEXT - 1", Value: "SHINCHEN"},
		{Name: "colour 2", Value: "White & Red"},
		{Name: "PHOTO - 2", Value: "https://example.com/2.png"},
		{Name: "text-2", Value: "CARTOON"},
	}
	copyOf := func() []shopifyLineProp { return append([]shopifyLineProp(nil), pack...) }
	got := s.narrowBundleProps(ctx, []shopifyLineItem{
		{SKU: "PFM-1", FromBundle: true, Properties: copyOf()},
		{SKU: "PFM-2", FromBundle: true, Properties: copyOf()},
	})

	want := map[int][]string{0: {"Red & White", "https://example.com/1.png", "SHINCHEN"},
		1: {"White & Red", "https://example.com/2.png", "CARTOON"}}
	for i, values := range want {
		if len(got[i].Properties) != len(values) {
			t.Fatalf("slot %d kept %v, want only its own three fields", i+1, got[i].Properties)
		}
		for j, v := range values {
			if got[i].Properties[j].Value != v {
				t.Errorf("slot %d field %d = %q, want %q", i+1, j, got[i].Properties[j].Value, v)
			}
		}
	}
}
