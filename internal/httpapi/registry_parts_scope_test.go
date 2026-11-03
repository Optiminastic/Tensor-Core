package httpapi

// How a part says its files are organised: one for every colourway, or one
// per child SKU.
//
// The panel shows ONE of two pickers off the back of this. It used to show
// both at once for every part, with nothing saying which was meant to win, so
// a product configured per SKU still displayed an empty product-wide "Prints
// from" above its rows reading "Nothing prints this" - which was not true and
// was the first thing anybody read.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
)

func partsOf(t *testing.T, store *db.Store, code string) []productPartResponse {
	t.Helper()
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key(), auth.RegistryManage.Key()})

	rr := doJSON(router, http.MethodGet, "/registry/products/"+code+"/parts", token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list parts: %d %s", rr.Code, rr.Body.String())
	}
	var out []productPartResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func seedDesign(t *testing.T, store *db.Store, variantID uuid.UUID, role, key string) {
	t.Helper()
	if _, err := store.Pool.Exec(context.Background(), `
		INSERT INTO variant_designs (id, variant_id, role, template_key, version, status)
		VALUES ($1, $2, $3, $4, 1, 'active')`,
		uuid.New(), variantID, role, key); err != nil {
		t.Fatalf("seed design: %v", err)
	}
}

func TestIntegrationAProductWithNoNamedPartsStillShowsItsSKUs(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// THE REGRESSION, caught on live data rather than here. The merge that
	// folds named parts over derived ones returned early when nothing had
	// been named, so per_variant kept Go's zero value - false - and every
	// product that predates named parts came back "one shared file". The
	// panel would then hide the per-SKU rows that are the ONLY thing
	// configuring PDHR and PWDH.
	productID, variantA := seedProductWithVariant(t, store, "PDHR", "PDHR-G")
	variantB := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO product_variants (id, product_id, name, sku) VALUES ($1, $2, $3, $3)`,
		variantB, productID, "PDHR-P"); err != nil {
		t.Fatalf("second variant: %v", err)
	}
	// Each colourway its own drawing, which is what makes this per-SKU.
	seedDesign(t, store, variantA, "body", "pdhr-g")
	seedDesign(t, store, variantB, "body", "pdhr-p")

	parts := partsOf(t, store, "PDHR")
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1", len(parts))
	}
	if !parts[0].PerVariant {
		t.Error("the part reads as one shared file, so the panel would hide the" +
			" per-SKU rows that are the only thing configuring it")
	}
}

func TestIntegrationOneSharedFileReadsAsOneSharedFile(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// The other half, and what a plank needs: twenty SKUs that are one shape
	// in twenty filaments agree on their file, so the panel asks once.
	productID, variantA := seedProductWithVariant(t, store, "TNP", "TNP-RED")
	variantB := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO product_variants (id, product_id, name, sku) VALUES ($1, $2, $3, $3)`,
		variantB, productID, "TNP-BLUE"); err != nil {
		t.Fatalf("second variant: %v", err)
	}
	seedDesign(t, store, variantA, "body", "tnp")
	seedDesign(t, store, variantB, "body", "tnp")

	parts := partsOf(t, store, "TNP")
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1", len(parts))
	}
	if parts[0].PerVariant {
		t.Error("a product whose SKUs agree on one file should ask for it once," +
			" not once per colourway")
	}
}

func TestIntegrationNamingAPartOverridesTheInference(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	productID, variant := seedProductWithVariant(t, store, "SC", "SC-PNK")
	seedDesign(t, store, variant, "body", "sc_plank")

	// Inferred as one shared file, because the one SKU agrees with itself.
	if parts := partsOf(t, store, "SC"); parts[0].PerVariant {
		t.Fatal("expected the inference to say one shared file")
	}

	// Somebody says otherwise, and that is what counts from then on.
	if _, err := store.Pool.Exec(ctx, `
		INSERT INTO product_parts (id, product_id, role, per_variant)
		VALUES ($1, $2, 'body', true)`, uuid.New(), productID); err != nil {
		t.Fatalf("name the part: %v", err)
	}
	parts := partsOf(t, store, "SC")
	if !parts[0].PerVariant {
		t.Error("the stored decision was ignored in favour of the guess")
	}
	if parts[0].ID == "" {
		t.Error("a named part came back with no id, so it could not be renamed or removed")
	}
}

func TestIntegrationRenamingAPartCarriesEverythingWithIt(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// THE ROLE IS A JOIN KEY, not a label. The same string addresses the
	// files, the field mapping, the colours and the part_role on every job
	// already made - so a rename that moved only the part row would leave all
	// four pointing at a name nothing refers to, and the next render would
	// hold the job complaining about a part that is no longer on any screen.
	productID, variant := seedProductWithVariant(t, store, "SC", "SC-PNK")
	seedDesign(t, store, variant, "rose", "sc_rose")
	seedFieldMap(t, store, productID, nil, nil)
	if _, err := store.Pool.Exec(ctx,
		`UPDATE product_field_maps SET role = 'rose' WHERE product_id = $1`,
		productID); err != nil {
		t.Fatalf("role the mapping: %v", err)
	}
	if _, err := store.Pool.Exec(ctx, `
		INSERT INTO design_colour_parts (id, product_id, role, part_name, colour_hex, position)
		VALUES ($1, $2, 'rose', 'petal', '#FF69B4', 0)`,
		uuid.New(), productID); err != nil {
		t.Fatalf("seed colour: %v", err)
	}
	jobID := uuid.New()
	if _, err := store.Pool.Exec(ctx, `
		INSERT INTO production_jobs (id, job_number, description, quantity, sku, part_role,
		                             status, assembly_status, qc_status, packaging_status,
		                             personalisation_status, colours)
		VALUES ($1, 'JOB-ROSE', 'rose', 1, 'SC-PNK', 'rose', 'queued', 'pending', 'pending',
		        'pending', 'pending', '[]')`, jobID); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	var partID uuid.UUID
	if err := store.Pool.QueryRow(ctx, `
		INSERT INTO product_parts (id, product_id, role, per_variant)
		VALUES ($1, $2, 'rose', true) RETURNING id`,
		uuid.New(), productID).Scan(&partID); err != nil {
		t.Fatalf("name the part: %v", err)
	}

	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key(), auth.RegistryManage.Key()})
	rr := doJSON(router, http.MethodPatch, "/registry/parts/"+partID.String(), token,
		map[string]any{"role": "flower"})
	if rr.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rr.Code, rr.Body.String())
	}

	for _, q := range []struct {
		what, sql string
	}{
		{"the file", `SELECT count(*) FROM variant_designs d JOIN product_variants v ON v.id = d.variant_id
		              WHERE v.product_id = $1 AND d.role = 'flower'`},
		{"the mapping", `SELECT count(*) FROM product_field_maps WHERE product_id = $1 AND role = 'flower'`},
		{"the colours", `SELECT count(*) FROM design_colour_parts WHERE product_id = $1 AND role = 'flower'`},
	} {
		var n int
		if err := store.Pool.QueryRow(ctx, q.sql, productID).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q.what, err)
		}
		if n != 1 {
			t.Errorf("%s did not follow the rename: %d rows on the new name", q.what, n)
		}
	}

	var jobRole string
	if err := store.Pool.QueryRow(ctx,
		`SELECT part_role FROM production_jobs WHERE id = $1`, jobID).Scan(&jobRole); err != nil {
		t.Fatalf("read the job: %v", err)
	}
	if jobRole != "flower" {
		t.Errorf("the job still prints part %q; its next render would find no template", jobRole)
	}
}

func TestIntegrationAPartWithWorkOnItCannotBeRemoved(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// Removing takes the files and the mapping with it, so a job that has not
	// printed would be left unable to render with nothing on the floor saying
	// why. Refused with a sentence naming what to do instead.
	productID, variant := seedProductWithVariant(t, store, "SC", "SC-PNK")
	seedDesign(t, store, variant, "rose", "sc_rose")
	if _, err := store.Pool.Exec(ctx, `
		INSERT INTO production_jobs (id, job_number, description, quantity, sku, part_role,
		                             status, assembly_status, qc_status, packaging_status,
		                             personalisation_status, colours)
		VALUES ($1, 'JOB-LIVE', 'rose', 1, 'SC-PNK', 'rose', 'queued', 'pending', 'pending',
		        'pending', 'pending', '[]')`, uuid.New()); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	var partID uuid.UUID
	if err := store.Pool.QueryRow(ctx, `
		INSERT INTO product_parts (id, product_id, role, per_variant)
		VALUES ($1, $2, 'rose', true) RETURNING id`,
		uuid.New(), productID).Scan(&partID); err != nil {
		t.Fatalf("name the part: %v", err)
	}

	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key(), auth.RegistryManage.Key()})
	rr := doJSON(router, http.MethodDelete, "/registry/parts/"+partID.String(), token, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("delete returned %d, want 409 - it would strand the job", rr.Code)
	}

	var n int
	if err := store.Pool.QueryRow(ctx,
		`SELECT count(*) FROM variant_designs d JOIN product_variants v ON v.id = d.variant_id
		 WHERE v.product_id = $1`, productID).Scan(&n); err != nil {
		t.Fatalf("count designs: %v", err)
	}
	if n != 1 {
		t.Errorf("the refusal still removed the part's file: %d left", n)
	}
}
