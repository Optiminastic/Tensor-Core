package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/pricing"
)

// TestIntegrationDesignPartsCRUD covers authoring a design's parts: create (with a
// unique-role guard), list, edit, and delete - the endpoints that turn a design
// into a multi-part product.
func TestIntegrationDesignPartsCRUD(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)
	brand := string(pricing.BrandGifting)

	designID := seedPricedDesign(t, store, brand, verdictGreen).String()

	editor := minter.mint(t, []string{"design:update", "brand:manage"})
	reader := minter.mint(t, []string{"design:read", "brand:manage"})
	deleter := minter.mint(t, []string{"design:delete", "brand:manage"})

	// design:update is required to add a part.
	noPerm := minter.mint(t, []string{"brand:manage"})
	if rr := doJSON(router, http.MethodPost, "/designs/"+designID+"/parts", noPerm,
		map[string]any{"role": "body"}); rr.Code != http.StatusForbidden {
		t.Errorf("add part without design:update = %d, want 403", rr.Code)
	}

	// Create two parts: a single body and two identical legs.
	if rr := doJSON(router, http.MethodPost, "/designs/"+designID+"/parts", editor,
		map[string]any{"role": "body", "part_index": 0}); rr.Code != http.StatusCreated {
		t.Fatalf("create body = %d body=%s", rr.Code, rr.Body.String())
	}
	var leg designPartResponse
	rr := doJSON(router, http.MethodPost, "/designs/"+designID+"/parts", editor,
		map[string]any{"role": "leg", "part_index": 1, "quantity": 2})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create leg = %d body=%s", rr.Code, rr.Body.String())
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &leg)
	if leg.Quantity != 2 {
		t.Errorf("leg quantity = %d, want 2", leg.Quantity)
	}

	// A duplicate role on the same design is a conflict.
	if rr := doJSON(router, http.MethodPost, "/designs/"+designID+"/parts", editor,
		map[string]any{"role": "body"}); rr.Code != http.StatusConflict {
		t.Errorf("duplicate role = %d, want 409", rr.Code)
	}

	// List returns both parts, in declared order.
	rr = doJSON(router, http.MethodGet, "/designs/"+designID+"/parts", reader, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list parts = %d body=%s", rr.Code, rr.Body.String())
	}
	var parts []designPartResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &parts)
	if len(parts) != 2 || parts[0].Role != "body" || parts[1].Role != "leg" {
		t.Fatalf("parts = %+v, want [body, leg]", parts)
	}

	// Edit the leg down to one, then delete the body.
	if rr := doJSON(router, http.MethodPatch, "/designs/"+designID+"/parts/"+leg.ID, editor,
		map[string]any{"quantity": 1}); rr.Code != http.StatusOK {
		t.Fatalf("update leg = %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := doJSON(router, http.MethodDelete, "/designs/"+designID+"/parts/"+parts[0].ID, deleter, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("delete body = %d body=%s", rr.Code, rr.Body.String())
	}
	rr = doJSON(router, http.MethodGet, "/designs/"+designID+"/parts", reader, nil)
	_ = json.Unmarshal(rr.Body.Bytes(), &parts)
	if len(parts) != 1 || parts[0].Role != "leg" || parts[0].Quantity != 1 {
		t.Fatalf("after edits parts = %+v, want [leg x1]", parts)
	}
}
