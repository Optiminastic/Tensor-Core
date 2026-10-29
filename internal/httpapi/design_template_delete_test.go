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
	token := minter.mint(t, []string{auth.ConfigRead.Key(), auth.ConfigManage.Key()})

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
	token := minter.mint(t, []string{auth.ConfigManage.Key()})

	// Compiled into the binary: there is no row to retire, and a delete that
	// quietly did nothing would look like it had worked.
	rr := doJSON(router, http.MethodDelete, "/registry/templates/dnp_two_heart", token, nil)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("delete built-in = %d %s, want 422", rr.Code, rr.Body.String())
	}
}
