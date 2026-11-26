package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
)

func TestCleanPhotoRules(t *testing.T) {
	ok, refusal := cleanPhotoRules([]photoRule{
		{WidthMM: 106.04, HeightMM: 80, WhenField: "photo_frames", WhenValue: "3"},
		{WidthMM: 51, HeightMM: 9, Shape: "Circle"},
	})
	if refusal != "" || ok[0].WidthMM != 106 || ok[0].Shape != shapeRect || ok[1].HeightMM != 51 {
		t.Fatalf("cleaned %+v %q", ok, refusal)
	}
	for _, bad := range [][]photoRule{
		{{WidthMM: 0, HeightMM: 10}},
		{{WidthMM: 250, HeightMM: 250}},
		{{WidthMM: 10, HeightMM: 10, WhenField: "photo_frames"}},
		{{WidthMM: 10, HeightMM: 10}, {WidthMM: 20, HeightMM: 20}},
		{{WidthMM: 10, HeightMM: 10, Shape: "star"}},
	} {
		if _, refusal := cleanPhotoRules(bad); refusal == "" {
			t.Errorf("%+v was accepted", bad)
		}
	}
}

// The sizes saved on a product are what the Custom Photo sheet prints its
// SKU at, and a product with none shows the built-in sizes it falls back to.
func TestIntegrationPhotoPrintSizesDriveTheSheet(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	s := NewServer(config.Settings{Environment: "development", AuthAudience: "tensor-core"},
		store, auth.NewGuards(minter.verifier, ""), nil)
	router := s.Router()
	token := minter.mint(t, []string{auth.RegistryRead.Key(), auth.RegistryManage.Key()})
	seedProductWithVariant(t, store, "PFMX", "PFM-9")

	var got photoPrintResponse
	rr := doJSON(router, http.MethodGet, "/registry/products/PFMX/photo-print", token, nil)
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &got) != nil ||
		len(got.Sizes) != 0 || len(got.BuiltIn) != 1 || got.BuiltIn[0].WidthMM != 56.6 || got.Sample != nil {
		t.Fatalf("before = %d %s", rr.Code, rr.Body.String())
	}

	rr = doJSON(router, http.MethodPut, "/registry/products/PFMX/photo-print/sizes", token, map[string]any{
		"sizes": []map[string]any{
			{"width_mm": 70, "height_mm": 75, "shape": "rect", "when_field": "Magnet size", "when_value": "Big"},
			{"width_mm": 50, "height_mm": 52},
		},
	})
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &got) != nil || len(got.Sizes) != 2 {
		t.Fatalf("save = %d %s", rr.Code, rr.Body.String())
	}
	if rr := doJSON(router, http.MethodPut, "/registry/products/PFMX/photo-print/sizes", token, map[string]any{
		"sizes": []map[string]any{{"width_mm": 300, "height_mm": 300}},
	}); rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "A4") {
		t.Fatalf("too big = %d %s", rr.Code, rr.Body.String())
	}

	rulesFor := s.photoRulesResolver(ctx)
	big, _, _ := photoSpecFor(sheetLine("pfm-9", 1, "MAGNET SIZE", "big"), rulesFor)
	plain, _, _ := photoSpecFor(sheetLine("PFM-9", 1), rulesFor)
	other, _, _ := photoSpecFor(sheetLine("PFM-2", 1), rulesFor)
	if big != (photoSpec{w: 70, h: 75}) || plain != (photoSpec{w: 50, h: 52}) || other != (photoSpec{w: 56.6, h: 59.7}) {
		t.Fatalf("big %+v plain %+v other %+v", big, plain, other)
	}

	// Without storage there is nowhere to keep a sample, and it says so.
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, multipartFile(t, "/registry/products/PFMX/photo-print/sample", token, "s.pdf", []byte("%PDF-1.4")))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("sample without storage = %d", rr.Code)
	}
}

func TestIntegrationPhotoPrintSampleIsKept(t *testing.T) {
	objects := testObjectStore(t)
	store := setupStore(t)
	seedAll(t, store)
	if _, err := store.Pool.Exec(context.Background(), `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	s := NewServer(config.Settings{Environment: "development", AuthAudience: "tensor-core"},
		store, auth.NewGuards(minter.verifier, ""), nil)
	s.EnablePipeline(objects, nil)
	router := s.Router()
	token := minter.mint(t, []string{auth.RegistryRead.Key(), auth.RegistryManage.Key()})
	seedProductWithVariant(t, store, "DUOX", "T3DPS-DUO-9")
	send := func(name string, data []byte) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, multipartFile(t, "/registry/products/DUOX/photo-print/sample", token, name, data))
		return rr
	}
	if rr := send("x.pdf", []byte("not a pdf")); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("not a pdf = %d", rr.Code)
	}
	pdf := []byte("%PDF-1.4\n% sample\n%%EOF\n")
	var got photoPrintResponse
	if rr := send("duosnap size.pdf", pdf); rr.Code != http.StatusOK ||
		json.Unmarshal(rr.Body.Bytes(), &got) != nil || got.Sample == nil || got.Sample.Filename != "duosnap size.pdf" {
		t.Fatalf("upload = %d %s", rr.Code, rr.Body.String())
	}
	rr := doJSON(router, http.MethodGet, "/registry/products/DUOX/photo-print/sample", token, nil)
	if rr.Code != http.StatusOK || rr.Body.String() != string(pdf) {
		t.Fatalf("download = %d", rr.Code)
	}
	if rr := doJSON(router, http.MethodDelete, "/registry/products/DUOX/photo-print/sample", token, nil); rr.Code != http.StatusOK {
		t.Fatalf("delete = %d", rr.Code)
	}
}
