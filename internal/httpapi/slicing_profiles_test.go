package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
)

// A stub BambuBuddy with three pipelines: one that imports, one with no
// presets, and one aimed at a printer class Tensor does not run.
func presetStub(t *testing.T, infill *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/slicer-pipelines/":
			_, _ = w.Write([]byte(`{"pipelines":[
				{"id":7,"name":"Heart H2C","target_kind":"printer_class","target_model_class":"H2C",
				 "printer_preset":{"source":"local","id":"12"},"process_preset":{"source":"local","id":"8"},
				 "filament_presets":[{"source":"local","id":"13"}]},
				{"id":8,"name":"Empty","target_kind":"printer_class","target_model_class":"A2L"},
				{"id":9,"name":"X1C","target_kind":"printer_class","target_model_class":"X1C",
				 "printer_preset":{"source":"local","id":"12"},"process_preset":{"source":"local","id":"8"},
				 "filament_presets":[{"source":"local","id":"13"}]}
			]}`))
		case "/api/v1/local-presets/12":
			_, _ = w.Write([]byte(`{"name":"Bambu Lab H2C 0.4 nozzle","setting":{"type":"machine","name":"Bambu Lab H2C 0.4 nozzle","from":"System"}}`))
		case "/api/v1/local-presets/8":
			_, _ = w.Write([]byte(`{"name":"0.28mm heart","setting":{"type":"process","name":"0.28mm heart","from":"User",
				"inherits":"0.20mm Standard @BBL H2C","layer_height":"0.28","wall_loops":"2","sparse_infill_density":"` + *infill + `"}}`))
		case "/api/v1/local-presets/13":
			_, _ = w.Write([]byte(`{"name":"Bambu PLA Basic @BBL H2C - Copy","setting":{"type":"filament","name":"Bambu PLA Basic @BBL H2C - Copy"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestIntegrationSlicingProfilesImportAndOverride(t *testing.T) {
	infill := "10%"
	stub := presetStub(t, &infill)
	defer stub.Close()

	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE slicing_profiles`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	cfg := config.Settings{
		Environment: "development", AuthAudience: "tensor-core",
		BambuBuddyURL: stub.URL, BambuBuddyAPIKey: "test-key",
	}
	router := NewServer(cfg, store, auth.NewGuards(minter.verifier, ""), nil).Router()
	manage := minter.mint(t, []string{auth.RegistryRead.Key(), auth.RegistryManage.Key()})
	readOnly := minter.mint(t, []string{auth.RegistryRead.Key()})

	type profile struct {
		ID        string         `json:"id"`
		Version   int            `json:"version"`
		Family    string         `json:"machine_family"`
		Overrides map[string]any `json:"overrides"`
		Settings  map[string]any `json:"settings"`
	}
	importAll := func() importProfilesResponse {
		t.Helper()
		rr := doJSON(router, http.MethodPost, "/registry/slicing-profiles/import", manage, nil)
		var out importProfilesResponse
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &out) != nil {
			t.Fatalf("import = %d: %s", rr.Code, rr.Body.String())
		}
		return out
	}
	list := func() []profile {
		t.Helper()
		rr := doJSON(router, http.MethodGet, "/registry/slicing-profiles", readOnly, nil)
		var out []profile
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &out) != nil {
			t.Fatalf("list = %d: %s", rr.Code, rr.Body.String())
		}
		return out
	}

	if rr := doJSON(router, http.MethodPost, "/registry/slicing-profiles/import", readOnly, nil); rr.Code != http.StatusForbidden {
		t.Fatalf("import without registry:manage = %d, want 403", rr.Code)
	}

	first := importAll()
	if len(first.Imported) != 1 || len(first.Skipped) != 2 {
		t.Fatalf("imported %d, skipped %d; want 1 and 2: %+v", len(first.Imported), len(first.Skipped), first.Skipped)
	}
	got := list()
	if len(got) != 1 || got[0].Family != "H2C" || got[0].Version != 1 || got[0].Settings["layer_height"] != "0.28" {
		t.Fatalf("after import: %+v", got)
	}
	id := got[0].ID

	// An unchanged pipeline re-imports without moving the version.
	importAll()
	if v := list()[0].Version; v != 1 {
		t.Fatalf("re-importing an unchanged pipeline moved the version to %d", v)
	}

	put := func(body map[string]any) int {
		return doJSON(router, http.MethodPut, "/registry/slicing-profiles/"+id+"/overrides", manage,
			map[string]any{"overrides": body}).Code
	}
	if code := put(map[string]any{"wall_loop": "3"}); code != http.StatusUnprocessableEntity {
		t.Errorf("a key the process does not have = %d, want 422", code)
	}
	if code := put(map[string]any{"filament_map": []any{"1"}}); code != http.StatusUnprocessableEntity {
		t.Errorf("a per-bed key = %d, want 422", code)
	}
	if code := put(map[string]any{"wall_loops": "3"}); code != http.StatusOK {
		t.Fatalf("a valid override = %d", code)
	}
	p := list()[0]
	if p.Version != 2 || p.Settings["wall_loops"] != "3" || p.Overrides["wall_loops"] != "3" {
		t.Fatalf("after override: %+v", p)
	}

	// A preset changed in BambuBuddy refreshes the base, keeps the override,
	// and moves the version.
	infill = "15%"
	importAll()
	p = list()[0]
	if p.Version != 3 || p.Settings["sparse_infill_density"] != "15%" || p.Settings["wall_loops"] != "3" {
		t.Fatalf("after a changed re-import: %+v", p)
	}

	rr := doJSON(router, http.MethodGet, "/registry/slicing-profiles/"+id, readOnly, nil)
	var detailResp struct {
		Process map[string]any `json:"process"`
	}
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &detailResp) != nil ||
		detailResp.Process["inherits"] != "0.20mm Standard @BBL H2C" {
		t.Fatalf("detail = %d: %s", rr.Code, rr.Body.String())
	}
}
