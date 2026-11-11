package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/Optiminastic/tensor-core/internal/auth"
)

// shopWorkbook is the floor's stock workbook as it is kept: a Filament sheet
// counting spools, and a Boxes sheet with an unheaded size column.
func shopWorkbook(t *testing.T) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	write := func(sheet string, rows [][]any) {
		for i, row := range rows {
			cell, _ := excelize.CoordinatesToCellName(1, i+1)
			if err := f.SetSheetRow(sheet, cell, &row); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = f.SetSheetName("Sheet1", "Filament")
	write("Filament", [][]any{
		{"Color", "Opening stock", "New", "Used", "Closing", "Taken by"},
		{"Red", 16, nil, 4, 12, "ADITYA"},
		{"Black", 40, 25, 8, 57},
	})
	if _, err := f.NewSheet("Boxes"); err != nil {
		t.Fatal(err)
	}
	write("Boxes", [][]any{
		{"Boxes", nil, "Total", "Used", "Remaining"},
		{"DNP Box", "25x10x8.5", 3000, 450, 2550},
		{"Moon box", "18x18x18"},
		{"Moon box", "15x15x10"},
	})
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func uploadSheet(t *testing.T, router http.Handler, token, path string, file []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("file", "stock.xlsx")
	_, _ = part.Write(file)
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

func TestIntegrationInventoryImportsTheShopsWorkbook(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx,
		`TRUNCATE inventory_items, filament_inventory, inventory_custom_columns, colour_map CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.FilamentRead.Key(), auth.FilamentManage.Key()})
	book := shopWorkbook(t)

	// Filament: reads the Filament sheet, spools x 1000 g, PLA.
	rr := uploadSheet(t, router, token, "/filament-inventory/import", book)
	if rr.Code != http.StatusOK {
		t.Fatalf("filament import = %d: %s", rr.Code, rr.Body.String())
	}
	var res importResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &res)
	if res.Created != 2 || len(res.Columns) != 4 {
		t.Fatalf("filament result = %+v", res)
	}
	var grams float64
	var extra string
	var hex *string
	if err := store.Pool.QueryRow(ctx, `SELECT grams_available, extra::text, colour_hex FROM filament_inventory
		WHERE material = 'PLA' AND colour = 'Red'`).Scan(&grams, &extra, &hex); err != nil {
		t.Fatal(err)
	}
	// No colour map in this test, so the swatch is the built-in red.
	if hex == nil || *hex != "#E4002B" {
		t.Fatalf("red swatch = %v", hex)
	}
	if grams != 12000 || extra != `{"Used": "4", "Taken by": "ADITYA", "Opening stock": "16"}` {
		t.Fatalf("red = %v g, extra %s", grams, extra)
	}

	// Re-importing the same file changes nothing.
	rr = uploadSheet(t, router, token, "/filament-inventory/import", book)
	_ = json.Unmarshal(rr.Body.Bytes(), &res)
	if res.Created != 0 || res.Updated != 2 {
		t.Fatalf("re-import = %+v", res)
	}

	// Items: reads the Boxes sheet; the two Moon boxes stay two.
	rr = uploadSheet(t, router, token, "/inventory-items/import", book)
	if rr.Code != http.StatusOK {
		t.Fatalf("items import = %d: %s", rr.Code, rr.Body.String())
	}
	var names []string
	rows, _ := store.Pool.Query(ctx, `SELECT name FROM inventory_items ORDER BY name`)
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	if len(names) != 3 || names[1] != "Moon box (15x15x10)" {
		t.Fatalf("items = %v", names)
	}

	// A column added by hand, a value set inline, and the column removed.
	rr = doJSON(router, http.MethodPost, "/inventory-columns", token,
		map[string]string{"shelf": "items", "name": "Supplier"})
	var col inventoryColumnResponse
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &col) != nil {
		t.Fatalf("add column = %d: %s", rr.Code, rr.Body.String())
	}
	var itemID string
	_ = store.Pool.QueryRow(ctx, `SELECT id FROM inventory_items WHERE name = 'DNP Box'`).Scan(&itemID)
	rr = doJSON(router, http.MethodPatch, "/inventory-items/"+itemID+"/extra", token,
		map[string]string{"column": "supplier", "value": "Packwell"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("set value = %d: %s", rr.Code, rr.Body.String())
	}
	rr = doJSON(router, http.MethodPatch, "/inventory-items/"+itemID+"/extra", token,
		map[string]string{"column": "Nope", "value": "x"})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown column = %d", rr.Code)
	}
	rr = doJSON(router, http.MethodDelete, "/inventory-columns/"+col.ID, token, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete column = %d: %s", rr.Code, rr.Body.String())
	}
	_ = store.Pool.QueryRow(ctx, `SELECT extra::text FROM inventory_items WHERE id = $1`, itemID).Scan(&extra)
	if extra != `{"Used": "450", "Total": "3000", "Details": "25x10x8.5"}` {
		t.Fatalf("after delete, extra = %s", extra)
	}
}

func TestIntegrationFilamentRowEditAndDelete(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE filament_inventory, colour_map CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.FilamentRead.Key(), auth.FilamentManage.Key()})
	add := func(colour string) string {
		rr := doJSON(router, http.MethodPost, "/filament-inventory", token,
			map[string]any{"material": "PLA", "colour": colour, "grams_available": 1000})
		var f filamentResponse
		if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &f) != nil {
			t.Fatalf("add %s = %d: %s", colour, rr.Code, rr.Body.String())
		}
		return f.ID
	}
	typo, _ := add("Bbay Blue"), add("Red")

	// A misspelt colour corrected, with a hex given by hand.
	rr := doJSON(router, http.MethodPatch, "/filament-inventory/"+typo, token, map[string]any{
		"material": "PLA", "colour": "Baby Blue", "colour_hex": "89cff0",
		"grams_available": 2000, "reorder_level_grams": 500,
	})
	var f filamentResponse
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &f) != nil {
		t.Fatalf("edit = %d: %s", rr.Code, rr.Body.String())
	}
	if deref(f.Colour) != "Baby Blue" || deref(f.ColourHex) != "#89CFF0" || f.GramsAvailable != 2000 {
		t.Fatalf("edited = %+v", f)
	}
	// Renaming onto another row's colour would split one stock in two.
	rr = doJSON(router, http.MethodPatch, "/filament-inventory/"+typo, token, map[string]any{
		"material": "PLA", "colour": "Red", "grams_available": 1,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("rename onto Red = %d", rr.Code)
	}
	if rr = doJSON(router, http.MethodDelete, "/filament-inventory/"+typo, token, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rr.Code)
	}
	if rr = doJSON(router, http.MethodDelete, "/filament-inventory/"+typo, token, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d", rr.Code)
	}
}
