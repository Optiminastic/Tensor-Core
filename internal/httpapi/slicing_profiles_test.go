package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

// A stub BambuBuddy: an H2C pipeline on saved presets and a P2S one on a Bambu
// system filament, both import; one with no presets, one aimed at a printer
// class Tensor does not run and one with an unreadable filament are skipped.
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
				{"id":10,"name":"P2S stock PLA","target_kind":"printer_class","target_model_class":"P2S",
				 "printer_preset":{"source":"local","id":"12"},"process_preset":{"source":"local","id":"8"},
				 "filament_presets":[{"source":"cloud","id":"GFSA00_11"}]},
				{"id":11,"name":"Odd filament","target_kind":"printer_class","target_model_class":"P2S",
				 "printer_preset":{"source":"local","id":"12"},"process_preset":{"source":"local","id":"8"},
				 "filament_presets":[{"source":"usb","id":"x"}]},
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
	heart := func() profile {
		t.Helper()
		for _, p := range list() {
			if p.Family == "H2C" {
				return p
			}
		}
		t.Fatal("the H2C profile is missing")
		return profile{}
	}

	if rr := doJSON(router, http.MethodPost, "/registry/slicing-profiles/import", readOnly, nil); rr.Code != http.StatusForbidden {
		t.Fatalf("import without registry:manage = %d, want 403", rr.Code)
	}

	first := importAll()
	if len(first.Imported) != 2 || len(first.Skipped) != 3 {
		t.Fatalf("imported %d, skipped %d; want 2 and 3: %+v", len(first.Imported), len(first.Skipped), first.Skipped)
	}
	if n := len(list()); n != 2 {
		t.Fatalf("listed %d profiles, want 2", n)
	}
	// A pipeline on one of Bambu's own filaments is stored as a reference the
	// slice worker resolves from the bundled profiles.
	var filament []byte
	var filamentName string
	if err := store.Pool.QueryRow(ctx, `SELECT filament_preset_name, filament_preset FROM slicing_profiles
		WHERE bambu_pipeline_id = 10`).Scan(&filamentName, &filament); err != nil {
		t.Fatalf("stock-filament profile: %v", err)
	}
	var ref map[string]any
	if json.Unmarshal(filament, &ref) != nil || !slicing.IsSystemFilamentRef(ref) ||
		filamentName != "Bambu system filament GFSA00_11" {
		t.Fatalf("stock filament stored as %q %s", filamentName, filament)
	}
	got := heart()
	if got.Version != 1 || got.Settings["layer_height"] != "0.28" {
		t.Fatalf("after import: %+v", got)
	}
	id := got.ID

	// An unchanged pipeline re-imports without moving the version.
	importAll()
	if v := heart().Version; v != 1 {
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
	p := heart()
	if p.Version != 2 || p.Settings["wall_loops"] != "3" || p.Overrides["wall_loops"] != "3" {
		t.Fatalf("after override: %+v", p)
	}

	// A preset changed in BambuBuddy refreshes the base, keeps the override,
	// and moves the version.
	infill = "15%"
	importAll()
	p = heart()
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
