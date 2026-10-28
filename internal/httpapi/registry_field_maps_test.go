package httpapi

// The character limit on a mapped field.
//
// product_field_maps.max_runes has existed since migration 0127 and the
// personalisation page has enforced it all along - it is what stops a customer
// typing twenty letters into a box the plate fits twelve of. What never
// existed was a way to SET one: this route clears a product's mapping and
// writes it again from the request, so the column was writable only by hand in
// SQL, and every save made from the page built for editing the mapping deleted
// whatever had been set there.
//
// The Diwali products are what made that matter. The Royal's plate fits twelve
// characters and the lotus holder's box sixteen, both read off the .scad's own
// text box; neither limit could be reproduced on another database through the
// API, and either would have been lost the first time somebody edited the
// mapping on screen.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
)

// fieldMapRow is the slice of the response these tests are about.
type fieldMapRow struct {
	ScadVariable string `json:"scad_variable"`
	PropertyKey  string `json:"property_key"`
	FixedValue   string `json:"fixed_value"`
	MaxRunes     *int32 `json:"max_runes"`
}

func TestIntegrationFieldMapCharacterLimitSurvivesASave(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key(), auth.ConfigManage.Key()})

	productID := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO products (id, code, name) VALUES ($1, 'PWDH', 'Lotus Diya Holder')`,
		productID); err != nil {
		t.Fatalf("product: %v", err)
	}

	const path = "/registry/products/PWDH/field-maps?role=body"
	read := func() map[string]fieldMapRow {
		rr := doJSON(router, http.MethodGet, path, token, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("get field maps = %d %s", rr.Code, rr.Body.String())
		}
		var rows []fieldMapRow
		if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
			t.Fatalf("decode field maps: %v", err)
		}
		out := make(map[string]fieldMapRow, len(rows))
		for _, r := range rows {
			out[r.ScadVariable] = r
		}
		return out
	}

	// The lotus holder's own box: sixteen characters over two lines.
	put := doJSON(router, http.MethodPut, path, token, map[string]any{
		"maps": []map[string]any{
			{
				"property_key": "name", "scad_variable": "TEXT",
				"value_type": "string", "max_runes": 16,
			},
		},
	})
	if put.Code != http.StatusNoContent {
		t.Fatalf("put field maps = %d %s", put.Code, put.Body.String())
	}

	got := read()["TEXT"]
	if got.MaxRunes == nil {
		t.Fatal("max_runes is null after saving 16 - the limit never reached the database")
	}
	if *got.MaxRunes != 16 {
		t.Errorf("max_runes = %d, want 16", *got.MaxRunes)
	}

	// SAVED WHOLE, like the rest of the mapping. A request that omits the
	// limit is a request to have none, not a request to leave the old one
	// alone - the same rule the rows themselves follow, and the one that
	// makes "what the page shows is what the renderer reads" true.
	if rr := doJSON(router, http.MethodPut, path, token, map[string]any{
		"maps": []map[string]any{
			{"property_key": "name", "scad_variable": "TEXT", "value_type": "string"},
		},
	}); rr.Code != http.StatusNoContent {
		t.Fatalf("put without a limit = %d %s", rr.Code, rr.Body.String())
	}
	if again := read()["TEXT"]; again.MaxRunes != nil {
		t.Errorf("max_runes = %d after a save that omitted it, want none", *again.MaxRunes)
	}
}

func TestIntegrationFieldMapRefusesALimitOnAFixedValue(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key(), auth.ConfigManage.Key()})

	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO products (id, code, name) VALUES ($1, 'DNP', 'Dual Name Plank')`,
		uuid.New()); err != nil {
		t.Fatalf("product: %v", err)
	}

	// OUT_X=200 is written here, not typed by a customer, so there is no form
	// to enforce a limit in. Saved rather than refused it would sit in the
	// column looking like a rule that applied to something.
	rr := doJSON(router, http.MethodPut,
		"/registry/products/DNP/field-maps?role=body", token, map[string]any{
			"maps": []map[string]any{
				{
					"property_key": "", "scad_variable": "OUT_X",
					"value_type": "number", "fixed_value": "200", "max_runes": 12,
				},
			},
		})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("put = %d %s, want 422", rr.Code, rr.Body.String())
	}
	// The refusal has to name the variable: the mapping is saved as a list,
	// and "a row is wrong" sends somebody looking down all of them.
	if body := rr.Body.String(); !strings.Contains(body, "OUT_X") {
		t.Errorf("refusal does not name the variable: %s", body)
	}
}
