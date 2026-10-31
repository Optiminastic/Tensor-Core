package httpapi

// Removing a design file, and the one case that must never succeed.
//
// A template key is referenced by variant_designs as TEXT, not by a foreign
// key, so the database will let the last file a product renders from
// disappear without a word. What follows is not an error on the screen that
// did it: it is every order for that product holding, hours later, with a
// message about a file nobody can find. This check is the only thing between
// a tidy-up and a stopped line, so it is tested rather than trusted.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
)

// seedTemplate uploads a row for a key and returns nothing useful - the key
// is the handle everything else uses.
func seedTemplate(t *testing.T, store *db.Store, key string) {
	t.Helper()
	ctx := context.Background()
	fileID := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO file_assets (id, filename, content_type, size_bytes, storage_key, uploaded_by)
		 VALUES ($1, $2, 'text/plain', 1024, $3, 'test')`,
		fileID, key+".scad", "templates/"+key+"/x.scad"); err != nil {
		t.Fatalf("file asset: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO design_templates (id, template_key, file_id, version, uploaded_by)
		 VALUES ($1, $2, $3, 1, 'test')`,
		uuid.New(), key, fileID); err != nil {
		t.Fatalf("design template: %v", err)
	}
}

func TestIntegrationDeletingATemplateInUseIsRefused(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products, design_templates CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.ConfigRead.Key(), auth.RegistryManage.Key()})

	seedTemplate(t, store, "pwdh")
	seedTemplate(t, store, "spare")

	productID, variantID := uuid.New(), uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO products (id, code, name) VALUES ($1, 'PWDH', 'Lotus Diya Holder')`,
		productID); err != nil {
		t.Fatalf("product: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO product_variants (id, product_id, name, sku)
		 VALUES ($1, $2, 'Purple', 'PWDH-P')`,
		variantID, productID); err != nil {
		t.Fatalf("variant: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO variant_designs (id, variant_id, role, template_key)
		 VALUES ($1, $2, 'body', 'pwdh')`,
		uuid.New(), variantID); err != nil {
		t.Fatalf("design: %v", err)
	}

	rr := doJSON(router, http.MethodDelete, "/registry/templates/pwdh", token, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("delete in-use = %d %s, want 409", rr.Code, rr.Body.String())
	}
	// The refusal has to name the product. "This file is in use" sends
	// somebody looking through every product in the registry.
	if body := rr.Body.String(); !strings.Contains(body, "PWDH") {
		t.Errorf("refusal does not name the product: %s", body)
	}

	// And it really is still there: a refusal that had already retired the row
	// would be the worst of both.
	if rr := doJSON(router, http.MethodGet, "/registry/templates", token, nil); !strings.Contains(
		rr.Body.String(), `"pwdh"`) {
		t.Errorf("pwdh left the list despite the refusal: %s", rr.Body.String())
	}

	// A file nothing renders from goes.
	if rr := doJSON(router, http.MethodDelete, "/registry/templates/spare", token, nil); rr.Code !=
		http.StatusNoContent {
		t.Fatalf("delete unused = %d %s, want 204", rr.Code, rr.Body.String())
	}
	if rr := doJSON(router, http.MethodGet, "/registry/templates", token, nil); strings.Contains(
		rr.Body.String(), `"spare"`) {
		t.Errorf("spare is still listed after being removed: %s", rr.Body.String())
	}
}

func TestIntegrationABuiltInTemplateCannotBeDeleted(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryManage.Key()})

	// Compiled into the binary: there is no row to retire, and a delete that
	// quietly did nothing would look like it had worked.
	rr := doJSON(router, http.MethodDelete, "/registry/templates/dnp_two_heart", token, nil)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("delete built-in = %d %s, want 422", rr.Code, rr.Body.String())
	}
}

func TestIntegrationAFileCanBeFreedAndThenRemoved(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products, design_templates CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.ConfigRead.Key(), auth.RegistryManage.Key()})

	seedTemplate(t, store, "pdhr")
	productID, variantID := uuid.New(), uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO products (id, code, name) VALUES ($1, 'PDHR', 'Royal')`,
		productID); err != nil {
		t.Fatalf("product: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO product_variants (id, product_id, name, sku)
		 VALUES ($1, $2, 'Purple', 'PDHR-P')`, variantID, productID); err != nil {
		t.Fatalf("variant: %v", err)
	}
	if rr := doJSON(router, http.MethodPut, "/registry/products/PDHR/design", token,
		map[string]any{"role": "body", "template_key": "pdhr"}); rr.Code != http.StatusNoContent {
		t.Fatalf("assign = %d %s", rr.Code, rr.Body.String())
	}

	// Refused while it renders something - the guard that matters.
	if rr := doJSON(router, http.MethodDelete, "/registry/templates/pdhr", token, nil); rr.Code !=
		http.StatusConflict {
		t.Fatalf("delete while in use = %d %s, want 409", rr.Code, rr.Body.String())
	}

	// THE WAY OUT. Before this existed the refusal above was a dead end: it
	// said "point those at another file first" and template_key was required,
	// so there was no way to stop a product printing from a file short of
	// deleting the product. An empty key is the state every freshly imported
	// product is already in.
	if rr := doJSON(router, http.MethodPut, "/registry/products/PDHR/design", token,
		map[string]any{"role": "body", "template_key": ""}); rr.Code != http.StatusNoContent {
		t.Fatalf("unassign = %d %s, want 204", rr.Code, rr.Body.String())
	}

	// Nothing renders from it now, so it goes.
	if rr := doJSON(router, http.MethodDelete, "/registry/templates/pdhr", token, nil); rr.Code !=
		http.StatusNoContent {
		t.Fatalf("delete after unassigning = %d %s, want 204", rr.Code, rr.Body.String())
	}

	// And the product is back to unconfigured rather than pointing at a file
	// that is gone - which is what readiness reports as "no design file".
	var active int
	if err := store.Pool.QueryRow(ctx,
		`SELECT count(*) FROM variant_designs WHERE variant_id = $1 AND status = 'active'`,
		variantID).Scan(&active); err != nil {
		t.Fatalf("count designs: %v", err)
	}
	if active != 0 {
		t.Errorf("%d active designs after unassigning, want none", active)
	}
}
