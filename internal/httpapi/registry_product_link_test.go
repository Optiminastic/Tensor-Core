package httpapi

// ONE SWITCH FOR THE PRODUCT, where there used to be one per colourway.
//
// Offered per SKU it was twenty switches that all had to agree, and one left
// out of step is a single colourway silently skipping its customer and going
// to the floor with a default name on it. PWDH-W sat that way unnoticed.
//
// Two things are pinned here, because both are easy to get wrong and neither
// is visible from the response: that the write reaches EVERY variant of the
// product including retired ones, and that a body which forgot the field is
// refused rather than read as "off".

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
)

// askingSKUs reports which of a product's SKUs ask their customer, retired
// included - the state the sender actually reads.
func askingSKUs(t *testing.T, store *db.Store, productID uuid.UUID) map[string]bool {
	t.Helper()
	rows, err := store.Pool.Query(context.Background(),
		`SELECT sku, personalisation_link_enabled FROM product_variants WHERE product_id = $1`,
		productID)
	if err != nil {
		t.Fatalf("read variants: %v", err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var sku *string
		var enabled bool
		if err := rows.Scan(&sku, &enabled); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if sku != nil {
			out[*sku] = enabled
		}
	}
	return out
}

func TestIntegrationOneSwitchSettlesEverySKUIncludingRetiredOnes(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryManage.Key()})

	productID, _ := seedProductWithVariant(t, store, "PWDH", "PWDH-P")
	// A second live SKU, and a retired one. The retired SKU is the reason the
	// query is not scoped to active rows: it still has orders in flight, and
	// one of them held for a customer who is never asked is the bug.
	for _, v := range []struct{ sku, status string }{
		{"PWDH-R", "active"},
		{"PWDH-W", "retired"},
	} {
		if _, err := store.Pool.Exec(ctx,
			`INSERT INTO product_variants (id, product_id, name, sku, status)
			 VALUES ($1, $2, $3, $3, $4)`,
			uuid.New(), productID, v.sku, v.status); err != nil {
			t.Fatalf("variant %s: %v", v.sku, err)
		}
	}

	rr := doJSON(router, http.MethodPatch, "/registry/products/PWDH/personalisation-link",
		token, map[string]any{"enabled": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH = %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Code                string `json:"code"`
		PersonalisationLink bool   `json:"personalisation_link"`
		SKUs                int64  `json:"skus"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.SKUs != 3 {
		t.Errorf("skus = %d, want 3 - the retired SKU must be written too", body.SKUs)
	}
	if body.PersonalisationLink {
		t.Error("response says the product still asks after switching it off")
	}

	for sku, asking := range askingSKUs(t, store, productID) {
		if asking {
			t.Errorf("%s still asks its customer after one switch settled the product", sku)
		}
	}

	// And back on, because a switch that only works one way is half a switch.
	rr = doJSON(router, http.MethodPatch, "/registry/products/PWDH/personalisation-link",
		token, map[string]any{"enabled": true})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH on = %d %s", rr.Code, rr.Body.String())
	}
	for sku, asking := range askingSKUs(t, store, productID) {
		if !asking {
			t.Errorf("%s does not ask its customer after switching the product back on", sku)
		}
	}
}

func TestIntegrationAForgottenFieldIsRefusedNotReadAsOff(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryManage.Key()})

	productID, _ := seedProductWithVariant(t, store, "PWDH", "PWDH-P")

	// Go's zero value for a bool is false, so a body with no `enabled` would
	// bind as "switch it off" - which is why the field is a pointer.
	rr := doJSON(router, http.MethodPatch, "/registry/products/PWDH/personalisation-link",
		token, map[string]any{})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("PATCH with no field = %d %s, want 422", rr.Code, rr.Body.String())
	}
	if asking := askingSKUs(t, store, productID); !asking["PWDH-P"] {
		t.Error("a refused PATCH switched the SKU off anyway")
	}

	rr = doJSON(router, http.MethodPatch, "/registry/products/NOSUCH/personalisation-link",
		token, map[string]any{"enabled": false})
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown product = %d, want 404", rr.Code)
	}
}
