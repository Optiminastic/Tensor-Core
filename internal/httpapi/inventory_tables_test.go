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

// The Inventory page's three tables and the registry's Components, paged in
// Postgres: items (with the shelf's value and codeless count over everything),
// filament (stock tabs, material select, the materials list) and the colour
// map (paged by colour, never splitting one).

type anyPage struct {
	Items      []map[string]any `json:"items"`
	Total      int64            `json:"total"`
	Counts     map[string]int64 `json:"counts"`
	TotalValue *float64         `json:"total_value"`
	Materials  []string         `json:"materials"`
}

func getAnyPage(t *testing.T, router http.Handler, token, path string) anyPage {
	t.Helper()
	rr := doJSON(router, http.MethodGet, path, token, nil)
	var out anyPage
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &out) != nil {
		t.Fatalf("GET %s = %d: %s", path, rr.Code, rr.Body.String())
	}
	return out
}

func TestIntegrationInventoryTablesPages(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE inventory_items, filament_inventory, colour_map CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	reader := minter.mint(t, []string{auth.FilamentRead.Key()})
	withCosts := minter.mint(t, []string{auth.FilamentRead.Key(), auth.ConfigRead.Key()})

	// 20 items at ₹10 x 2 each; every fourth has no code.
	for i := 0; i < 20; i++ {
		code := fmt.Sprintf("IT-%02d", i)
		if i%4 == 0 {
			code = ""
		}
		if _, err := store.Pool.Exec(ctx,
			`INSERT INTO inventory_items (id, name, unit, quantity, unit_price, code)
			 VALUES ($1, $2, 'pc', 2, 10, NULLIF($3, ''))`,
			uuid.New(), fmt.Sprintf("Item %02d", i), code); err != nil {
			t.Fatalf("item %d: %v", i, err)
		}
	}
	items := getAnyPage(t, router, withCosts, "/inventory-items?page=1")
	if len(items.Items) != 15 || items.Total != 20 || items.Counts["without_code"] != 5 ||
		items.TotalValue == nil || *items.TotalValue != 400 {
		t.Fatalf("items page = %d of %d, counts %v, value %v", len(items.Items), items.Total, items.Counts, items.TotalValue)
	}
	if hidden := getAnyPage(t, router, reader, "/inventory-items?page=1"); hidden.TotalValue != nil ||
		hidden.Items[0]["unit_price"] != nil {
		t.Errorf("a caller without config:read was sent prices: value %v, row %v", hidden.TotalValue, hidden.Items[0])
	}
	if got := getAnyPage(t, router, reader, "/inventory-items?page=1&search=IT-1"); got.Total != 8 {
		t.Errorf("search by code IT-1 found %d, want 8 (IT-10..IT-19 bar the codeless 12 and 16)", got.Total)
	}

	// 18 spools: PLA and PETG alternating; every third below its reorder level.
	for i := 0; i < 18; i++ {
		material := map[bool]string{true: "PLA", false: "PETG"}[i%2 == 0]
		grams := 1000
		if i%3 == 0 {
			grams = 10
		}
		if _, err := store.Pool.Exec(ctx,
			`INSERT INTO filament_inventory (id, material, colour, grams_available, reorder_level_grams)
			 VALUES ($1, $2, $3, $4, 100)`, uuid.New(), material, fmt.Sprintf("Colour %02d", i), grams); err != nil {
			t.Fatalf("spool %d: %v", i, err)
		}
	}
	spools := getAnyPage(t, router, reader, "/filament-inventory?page=1")
	if len(spools.Items) != 15 || spools.Total != 18 || spools.Counts["low"] != 6 ||
		spools.Counts["in_stock"] != 12 || len(spools.Materials) != 2 {
		t.Fatalf("filament page = %d of %d, counts %v, materials %v", len(spools.Items), spools.Total, spools.Counts, spools.Materials)
	}
	if got := getAnyPage(t, router, reader, "/filament-inventory?page=1&tab=low&material=PLA"); got.Total != 3 {
		t.Errorf("low PLA = %d, want 3 (i = 0, 6, 12)", got.Total)
	}

	// 17 colours, each with a primary and an alternative.
	for i := 0; i < 17; i++ {
		for _, primary := range []bool{true, false} {
			if _, err := store.Pool.Exec(ctx,
				`INSERT INTO colour_map (id, colour_name, hex, is_primary) VALUES ($1, $2, $3, $4)`,
				uuid.New(), fmt.Sprintf("Colour %02d", i), map[bool]string{true: "#111111", false: "#222222"}[primary], primary); err != nil {
				t.Fatalf("colour %d: %v", i, err)
			}
		}
	}
	colours := getAnyPage(t, router, reader, "/filament-inventory/colour-map?page=1")
	if colours.Total != 17 || len(colours.Items) != 30 {
		t.Fatalf("colour page = %d entries for %d colours, want 15 colours' 30 entries", len(colours.Items), colours.Total)
	}
	if last := getAnyPage(t, router, reader, "/filament-inventory/colour-map?page=2"); len(last.Items) != 4 {
		t.Errorf("colour page 2 = %d entries, want the last 2 colours' 4", len(last.Items))
	}
}
