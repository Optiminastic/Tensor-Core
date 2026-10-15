package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
)

// A product's variants, paged in Postgres with only the page's parts read; and
// the product detail without its variant list, carrying the counts instead.
func TestIntegrationRegistryVariantsTablePages(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products, inventory_items CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key()})

	productID, itemID := uuid.New(), uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO products (id, code, name) VALUES ($1, 'DNP', 'Dual Name Plank')`, productID); err != nil {
		t.Fatalf("product: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO inventory_items (id, name, unit) VALUES ($1, 'LED strip', 'pc')`, itemID); err != nil {
		t.Fatalf("item: %v", err)
	}
	// 20 variants, V-00..V-19, each with one part.
	for i := 0; i < 20; i++ {
		variantID := uuid.New()
		if _, err := store.Pool.Exec(ctx,
			`INSERT INTO product_variants (id, product_id, name, sku) VALUES ($1, $2, $3, $4)`,
			variantID, productID, fmt.Sprintf("V-%02d", i), fmt.Sprintf("DNP-%02d", i)); err != nil {
			t.Fatalf("variant %d: %v", i, err)
		}
		if _, err := store.Pool.Exec(ctx,
			`INSERT INTO variant_bom (id, variant_id, inventory_item_id, quantity) VALUES ($1, $2, $3, 1)`,
			uuid.New(), variantID, itemID); err != nil {
			t.Fatalf("bom %d: %v", i, err)
		}
	}

	type variantPage struct {
		Items []struct {
			Name  string            `json:"name"`
			Parts []json.RawMessage `json:"parts"`
		} `json:"items"`
		Total      int64 `json:"total"`
		TotalPages int32 `json:"total_pages"`
	}
	get := func(q string) variantPage {
		rr := doJSON(router, http.MethodGet, "/registry/products/DNP/variants?"+q, token, nil)
		var out variantPage
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &out) != nil {
			t.Fatalf("variants %s = %d: %s", q, rr.Code, rr.Body.String())
		}
		return out
	}
	p1 := get("page=1")
	if len(p1.Items) != 15 || p1.Total != 20 || p1.TotalPages != 2 || p1.Items[0].Name != "V-00" {
		t.Fatalf("page 1 = %d variants of %d, first %v", len(p1.Items), p1.Total, p1.Items)
	}
	if len(p1.Items[0].Parts) != 1 {
		t.Errorf("a variant on the page arrived with %d parts, want its 1", len(p1.Items[0].Parts))
	}
	if p2 := get("page=2"); len(p2.Items) != 5 || p2.Items[0].Name != "V-15" {
		t.Errorf("page 2 = %d variants starting %v", len(p2.Items), p2.Items)
	}

	// The detail without variants: none sent, counts carried.
	var detailOut struct {
		Variants         []json.RawMessage `json:"variants"`
		VariantCount     int64             `json:"variant_count"`
		VariantPartCount *int64            `json:"variant_part_count"`
	}
	rr := doJSON(router, http.MethodGet, "/registry/products/DNP?variants=none", token, nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &detailOut); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if len(detailOut.Variants) != 0 || detailOut.VariantCount != 20 ||
		detailOut.VariantPartCount == nil || *detailOut.VariantPartCount != 20 {
		t.Errorf("detail without variants = %d variants, count %d, parts %v",
			len(detailOut.Variants), detailOut.VariantCount, detailOut.VariantPartCount)
	}
	// And the full detail is as it was.
	rr = doJSON(router, http.MethodGet, "/registry/products/DNP", token, nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &detailOut); err != nil || len(detailOut.Variants) != 20 {
		t.Errorf("full detail = %d variants (%v), want all 20", len(detailOut.Variants), err)
	}
}
