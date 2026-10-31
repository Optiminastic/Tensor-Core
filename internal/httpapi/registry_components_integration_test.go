package httpapi

// Components a product is made of, inherited by every SKU that declares none.
//
// resolveBom's unit tests pin the rule; these pin that the rule actually
// reaches the screen - that an inherited line arrives with its quantity, its
// cost and its count, and that a SKU with a list of its own sees only that.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
)

type componentVariant struct {
	SKU       *string `json:"sku"`
	PartCount int64   `json:"part_count"`
	PartsCost *float64
	Parts     []struct {
		ItemName string  `json:"item_name"`
		Quantity float64 `json:"quantity"`
	} `json:"parts"`
}

func TestIntegrationProductComponentsAreInherited(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products, inventory_items CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	// config:read as well, or every figure below is withheld by design.
	token := minter.mint(t, []string{
		auth.RegistryRead.Key(), auth.ConfigRead.Key(), auth.RegistryManage.Key(),
	})

	productID := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO products (id, code, name) VALUES ($1, 'DNP', 'Dual Name Plank')`,
		productID); err != nil {
		t.Fatalf("product: %v", err)
	}
	// A printed component - the shape this whole change exists for - and a
	// bought one beside it.
	heart, box := uuid.New(), uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO inventory_items (id, name, unit, unit_price, source, template_key)
		 VALUES ($1, 'Red heart', 'piece', 4.00, 'printed', 'dnp_heart_joinery')`,
		heart); err != nil {
		t.Fatalf("heart: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO inventory_items (id, name, unit, unit_price) VALUES ($1, 'Gift box', 'piece', 10.00)`,
		box); err != nil {
		t.Fatalf("box: %v", err)
	}

	plain, special := uuid.New(), uuid.New()
	for id, sku := range map[uuid.UUID]string{plain: "DNP-BLU", special: "DNP-RED"} {
		if _, err := store.Pool.Exec(ctx,
			`INSERT INTO product_variants (id, product_id, name, sku) VALUES ($1, $2, $3, $4)`,
			id, productID, sku, sku); err != nil {
			t.Fatalf("variant %s: %v", sku, err)
		}
	}

	// The product's list: a heart and a box.
	put := doJSON(router, http.MethodPut, "/registry/products/DNP/bom", token, map[string]any{
		"lines": []map[string]any{
			{"item_id": heart.String(), "quantity": 1},
			{"item_id": box.String(), "quantity": 1},
		},
	})
	if put.Code != http.StatusOK {
		t.Fatalf("put product bom = %d %s", put.Code, put.Body.String())
	}

	read := func() map[string]componentVariant {
		rr := doJSON(router, http.MethodGet, "/registry/products/DNP", token, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("product = %d %s", rr.Code, rr.Body.String())
		}
		var body struct {
			Variants []componentVariant `json:"variants"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("read product: %v", err)
		}
		out := map[string]componentVariant{}
		for _, v := range body.Variants {
			out[deref(v.SKU)] = v
		}
		return out
	}

	both := read()
	for _, sku := range []string{"DNP-BLU", "DNP-RED"} {
		v := both[sku]
		if len(v.Parts) != 2 {
			t.Fatalf("%s has %d parts, want the product's two", sku, len(v.Parts))
		}
		// The count has to move with the list, or the SKUs disclosure says
		// "0" beside two rows of parts.
		if v.PartCount != 2 {
			t.Errorf("%s part_count = %d, want 2", sku, v.PartCount)
		}
	}

	// ONE SKU DECLARES ITS OWN, and that replaces the product's list whole.
	// The failure this guards is the merge: an upgraded box arriving beside
	// the standard one, which slices, prices wrong and packs two.
	rr := doJSON(router, http.MethodPut,
		"/registry/variants/"+special.String()+"/bom", token, map[string]any{
			"lines": []map[string]any{{"item_id": heart.String(), "quantity": 2}},
		})
	if rr.Code != http.StatusOK {
		t.Fatalf("put variant bom = %d %s", rr.Code, rr.Body.String())
	}

	after := read()
	own := after["DNP-RED"]
	if len(own.Parts) != 1 {
		t.Fatalf("DNP-RED has %d parts, want only its own one: %+v", len(own.Parts), own.Parts)
	}
	if own.Parts[0].ItemName != "Red heart" || own.Parts[0].Quantity != 2 {
		t.Errorf("DNP-RED part = %+v, want 2 x Red heart", own.Parts[0])
	}
	// Its sibling is untouched: an override is one SKU's business.
	if len(after["DNP-BLU"].Parts) != 2 {
		t.Errorf("DNP-BLU has %d parts, want the product's two it still inherits",
			len(after["DNP-BLU"].Parts))
	}
}

// A component the shop makes must say what makes it. The constraint refuses
// the half-written row, and the handler refuses it by name first, so the
// person configuring it reads a sentence rather than a violation.
func TestIntegrationAPrintedComponentNeedsADesignFile(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products, inventory_items CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key(), auth.RegistryManage.Key()})

	id := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO inventory_items (id, name, unit) VALUES ($1, 'Red heart', 'piece')`,
		id); err != nil {
		t.Fatalf("item: %v", err)
	}
	base := "/registry/components/" + id.String()

	if rr := doJSON(router, http.MethodPatch, base, token,
		map[string]any{"source": "printed"}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("printed with no file = %d, want 422", rr.Code)
	}
	if rr := doJSON(router, http.MethodPatch, base, token, map[string]any{
		"source": "printed", "template_key": "no_such_template",
	}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("printed with an unknown template = %d, want 422", rr.Code)
	}
	// A bought part has nothing to print, so naming a file is a mistake worth
	// refusing rather than a column quietly ignored.
	if rr := doJSON(router, http.MethodPatch, base, token, map[string]any{
		"source": "bought", "template_key": "dnp_heart_joinery",
	}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("bought with a file = %d, want 422", rr.Code)
	}
	if rr := doJSON(router, http.MethodPatch, base, token,
		map[string]any{"source": "bought"}); rr.Code != http.StatusOK {
		t.Errorf("bought = %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

// Uploading the mesh a printed component is made from.
//
// The one path that had no way in: a part nobody personalises is a fixed mesh,
// and /registry/templates refuses anything but .scad - so without this a
// component could only ever point at a template it does not need.
func TestIntegrationUploadingAComponentModelMarksItPrinted(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products, inventory_items CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key(), auth.RegistryManage.Key()})

	id := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO inventory_items (id, name, unit) VALUES ($1, 'Red heart', 'piece')`,
		id); err != nil {
		t.Fatalf("item: %v", err)
	}
	path := "/registry/components/" + id.String() + "/model"

	// The .scad that RENDERS a part is not the part. Refused by name, because
	// it is the obvious mistake and the message is the only thing that tells
	// somebody which file was wanted.
	bad := uploadRequest(t, path, "red_heart.scad", "text/plain", 64)
	bad.Header.Set("Authorization", "Bearer "+token)
	if rr := serve(router, bad); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("a .scad = %d, want 422: %s", rr.Code, rr.Body.String())
	}

	// The test server has no object storage, so a well-formed upload stops
	// there - the same convention the damage upload tests follow. What this
	// asserts is that it got PAST validation: a 422 here would mean the mesh
	// itself was refused.
	good := uploadRequest(t, path, "red_heart.stl", "model/stl", 2048)
	good.Header.Set("Authorization", "Bearer "+token)
	if rr := serve(router, good); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("a valid .stl = %d, want 503 (accepted, then no storage): %s",
			rr.Code, rr.Body.String())
	}

	// And nothing was written on the way: a part whose model failed to store
	// must not be left marked printed with no file, which is the state the
	// CHECK exists to make impossible.
	var source string
	if err := store.Pool.QueryRow(ctx,
		`SELECT source FROM inventory_items WHERE id = $1`, id).Scan(&source); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if source != "bought" {
		t.Errorf("source = %q after a failed upload, want it unchanged", source)
	}
}

// A plate belongs to a SKU as well as a machine class.
//
// Nine files for a three-colourway product, not three: the filament is baked
// into a sliced file, so the purple plate is not the red one.
func TestIntegrationPlatesAreAddressedBySKUAndMachine(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products, inventory_items CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key(), auth.RegistryManage.Key()})

	id := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO inventory_items (id, name, unit, source, template_key)
		 VALUES ($1, 'Diya cup', 'piece', 'printed', 'dnp_heart_joinery')`,
		id); err != nil {
		t.Fatalf("item: %v", err)
	}
	base := "/registry/components/" + id.String() + "/plates"

	// A machine class that does not exist is refused by name, listing the ones
	// that do - the operator is the person who can pick the right one.
	bad := uploadRequest(t, base+"/H9Z", "plate.gcode", "text/plain", 64)
	bad.Header.Set("Authorization", "Bearer "+token)
	if rr := serve(router, bad); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown machine = %d, want 422: %s", rr.Code, rr.Body.String())
	}

	// The .scad is not a plate either.
	wrong := uploadRequest(t, base+"/H2C", "cup.scad", "text/plain", 64)
	wrong.Header.Set("Authorization", "Bearer "+token)
	if rr := serve(router, wrong); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("a .scad = %d, want 422: %s", rr.Code, rr.Body.String())
	}

	// A well-formed one reaches storage, which this harness does not have -
	// the same convention the damage upload tests follow. What it proves is
	// that the SKU and the machine both parsed.
	ok := uploadRequest(t, base+"/H2C", "pdhr-p-h2c.gcode", "text/plain", 2048)
	ok.Header.Set("Authorization", "Bearer "+token)
	if rr := serve(router, ok); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("a valid plate = %d, want 503 (accepted, then no storage): %s",
			rr.Code, rr.Body.String())
	}

	// Rows written directly, since the upload cannot complete here: what
	// matters is that nine coexist rather than overwriting each other.
	for _, sku := range []string{"PDHR-P", "PDHR-R", "PDHR-G"} {
		for _, family := range []string{"H2C", "A2L", "P2S"} {
			fileID := uuid.New()
			if _, err := store.Pool.Exec(ctx,
				`INSERT INTO file_assets (id, filename, content_type, size_bytes, storage_key, uploaded_by)
				 VALUES ($1, $2, 'text/plain', 1, $3, 'test')`,
				fileID, sku+"-"+family+".gcode", sku+"/"+family); err != nil {
				t.Fatalf("file %s %s: %v", sku, family, err)
			}
			if _, err := store.Pool.Exec(ctx,
				`INSERT INTO component_plates (id, inventory_item_id, sku, machine_family, file_id, uploaded_by)
				 VALUES ($1, $2, $3, $4, $5, 'test')`,
				uuid.New(), id, sku, family, fileID); err != nil {
				t.Fatalf("plate %s %s: %v", sku, family, err)
			}
		}
	}

	rr := doJSON(router, http.MethodGet, base, token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Items []struct {
			SKU           string `json:"sku"`
			MachineFamily string `json:"machine_family"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("read plates: %v", err)
	}
	if len(body.Items) != 9 {
		t.Fatalf("plates = %d, want 9 - three SKUs on three machines", len(body.Items))
	}
	seen := map[string]bool{}
	for _, p := range body.Items {
		seen[p.SKU+"/"+p.MachineFamily] = true
	}
	if len(seen) != 9 {
		t.Errorf("distinct SKU/machine pairs = %d, want 9: %v", len(seen), seen)
	}
}

// A component whose part does not change with the colourway keeps ONE set of
// plates for every SKU.
//
// The plank's red heart is the same heart on all twenty variants: per SKU that
// is sixty files where three will do, and fifty-seven of them copies of each
// other. A shared plate is stored under no SKU at all, which the same table
// and the same index already allow.
func TestIntegrationASharedPlateServesEverySKU(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products, inventory_items CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key(), auth.RegistryManage.Key()})

	id := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO inventory_items (id, name, unit, source, template_key)
		 VALUES ($1, 'Red heart', 'piece', 'printed', 'dnp_heart_joinery')`,
		id); err != nil {
		t.Fatalf("item: %v", err)
	}
	base := "/registry/components/" + id.String()

	// Declared shared, and the column says so rather than it being inferred
	// from the absence of per-SKU rows - which would read as "not configured
	// yet" on a component nobody had finished.
	rr := doJSON(router, http.MethodPatch, base, token, map[string]any{
		"source": "printed", "template_key": "dnp_heart_joinery", "plate_scope": "shared",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("set scope = %d %s", rr.Code, rr.Body.String())
	}
	var set struct {
		PlateScope string `json:"plate_scope"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &set)
	if set.PlateScope != "shared" {
		t.Fatalf("plate_scope = %q, want shared", set.PlateScope)
	}

	// Three rows under no SKU, one per machine class.
	for _, family := range []string{"H2C", "A2L", "P2S"} {
		fileID := uuid.New()
		if _, err := store.Pool.Exec(ctx,
			`INSERT INTO file_assets (id, filename, content_type, size_bytes, storage_key, uploaded_by)
			 VALUES ($1, $2, 'text/plain', 1, $3, 'test')`,
			fileID, "heart-"+family+".gcode", "heart/"+family); err != nil {
			t.Fatalf("file %s: %v", family, err)
		}
		if _, err := store.Pool.Exec(ctx,
			`INSERT INTO component_plates (id, inventory_item_id, sku, machine_family, file_id, uploaded_by)
			 VALUES ($1, $2, '', $3, $4, 'test')`,
			uuid.New(), id, family, fileID); err != nil {
			t.Fatalf("plate %s: %v", family, err)
		}
	}

	list := doJSON(router, http.MethodGet, base+"/plates", token, nil)
	var body struct {
		Items []struct {
			SKU           string `json:"sku"`
			MachineFamily string `json:"machine_family"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &body); err != nil {
		t.Fatalf("read plates: %v", err)
	}
	if len(body.Items) != 3 {
		t.Fatalf("plates = %d, want 3 - one per machine, shared by every SKU", len(body.Items))
	}
	for _, p := range body.Items {
		if p.SKU != "" {
			t.Errorf("plate for %s carries sku %q, want none", p.MachineFamily, p.SKU)
		}
	}
}
