package httpapi

// A Shopify bundle's component lines each keep only the fields their own
// product reads.

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
)

func TestIntegrationBundleLinesKeepOnlyTheirOwnFields(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	mapField := func(code, sku, key string) {
		productID, _ := seedProductWithVariant(t, store, code, sku)
		if _, err := store.Pool.Exec(ctx, `
			INSERT INTO product_field_maps
			       (id, product_id, role, property_key, scad_variable, value_type, required, position)
			VALUES ($1, $2, 'body', $3, 'NAME', 'string', true, 0)`,
			uuid.New(), productID, key); err != nil {
			t.Fatalf("map %s: %v", code, err)
		}
	}
	mapField("SCR", "SC-ROSE", "step 4 name on 3d rose")
	mapField("SCK", "SC-KEY", "step 5 name on heart keychain")

	s := NewServer(config.Settings{Environment: "development", AuthAudience: "tensor-core"},
		store, auth.NewGuards(newTokenMinter(t).verifier, ""), nil)

	bundle := []shopifyLineProp{
		{Name: "STEP 1 - Colour", Value: "BLACK"},
		{Name: "_has_gpo", Value: "1255456"},
		{Name: "STEP 2 - First Name-", Value: "TUSHAR"},
		{Name: "STEP 3 -Second Name", Value: "SUTHAR"},
		{Name: "STEP 4 - Name On 3D Rose", Value: "EFAWF"},
		{Name: "STEP 5 - Name On Heart Keychain", Value: "FRAFWA"},
	}
	copyOf := func() []shopifyLineProp { return append([]shopifyLineProp(nil), bundle...) }
	got := s.narrowBundleProps(ctx, []shopifyLineItem{
		{SKU: "SC-ROSE", FromBundle: true, Properties: copyOf()},
		{SKU: "SC-KEY", FromBundle: true, Properties: copyOf()},
		{SKU: "SC-PLANK-BLK", FromBundle: true, Properties: copyOf()}, // not in the registry yet
		{SKU: "SC-ROSE", Properties: copyOf()},                        // sold on its own
	})

	if len(got[0].Properties) != 1 || got[0].Properties[0].Value != "EFAWF" {
		t.Errorf("rose kept %v, want only its own name", got[0].Properties)
	}
	if len(got[1].Properties) != 1 || got[1].Properties[0].Value != "FRAFWA" {
		t.Errorf("keychain kept %v, want only its own name", got[1].Properties)
	}
	if len(got[2].Properties) != len(bundle) {
		t.Errorf("an unconfigured part lost fields: %v", got[2].Properties)
	}
	if len(got[3].Properties) != len(bundle) {
		t.Errorf("a line not from a bundle was trimmed: %v", got[3].Properties)
	}
}
