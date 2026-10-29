package httpapi

// Filing a design file under a heading.
//
// The property worth pinning is what this does NOT do. An earlier version of
// this control assigned the file to a product, which meant tidying the list
// and changing what prints were the same gesture. So: the label is stored,
// the label comes back, and variant_designs is exactly as it was.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
)

func sectionOf(t *testing.T, router http.Handler, token, key string) string {
	t.Helper()
	rr := doJSON(router, http.MethodGet, "/registry/templates", token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list templates = %d %s", rr.Code, rr.Body.String())
	}
	var rows []struct {
		Key     string `json:"key"`
		Section string `json:"section"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, r := range rows {
		if r.Key == key {
			return r.Section
		}
	}
	t.Fatalf("%s is not in the list: %s", key, rr.Body.String())
	return ""
}

func TestIntegrationFilingATemplateChangesNothingElse(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx,
		`TRUNCATE products, design_templates, design_template_sections CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.ConfigRead.Key(), auth.ConfigManage.Key()})

	seedTemplate(t, store, "pwdh")

	// A product printing from it, so there is something that could be broken.
	productID, variantID := uuid.New(), uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO products (id, code, name) VALUES ($1, 'PWDH', 'Lotus Diya Holder')`,
		productID); err != nil {
		t.Fatalf("product: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO product_variants (id, product_id, name, sku)
		 VALUES ($1, $2, 'Purple', 'PWDH-P')`, variantID, productID); err != nil {
		t.Fatalf("variant: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO variant_designs (id, variant_id, role, template_key)
		 VALUES ($1, $2, 'body', 'pwdh')`, uuid.New(), variantID); err != nil {
		t.Fatalf("design: %v", err)
	}

	if got := sectionOf(t, router, token, "pwdh"); got != "" {
		t.Errorf("section = %q before filing, want empty", got)
	}

	// File it somewhere its use does NOT imply, so the label cannot be
	// mistaken for the derived answer.
	rr := doJSON(router, http.MethodPatch, "/registry/templates/pwdh/section", token,
		map[string]any{"section": "other"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("file = %d %s", rr.Code, rr.Body.String())
	}
	if got := sectionOf(t, router, token, "pwdh"); got != "other" {
		t.Errorf("section = %q, want other", got)
	}

	// THE POINT OF THE TEST. Filing is a label; what prints is untouched.
	var designs int
	if err := store.Pool.QueryRow(ctx,
		`SELECT count(*) FROM variant_designs
		 WHERE status = 'active' AND lower(template_key) = 'pwdh' AND role = 'body'`,
	).Scan(&designs); err != nil {
		t.Fatalf("count designs: %v", err)
	}
	if designs != 1 {
		t.Errorf("PWDH has %d active body designs after filing, want the 1 it started with",
			designs)
	}

	// Empty unfiles it, back to whatever its use implies.
	if rr := doJSON(router, http.MethodPatch, "/registry/templates/pwdh/section", token,
		map[string]any{"section": ""}); rr.Code != http.StatusNoContent {
		t.Fatalf("unfile = %d %s", rr.Code, rr.Body.String())
	}
	if got := sectionOf(t, router, token, "pwdh"); got != "" {
		t.Errorf("section = %q after unfiling, want empty", got)
	}

	// A heading that is not one of the three is refused, naming them.
	if rr := doJSON(router, http.MethodPatch, "/registry/templates/pwdh/section", token,
		map[string]any{"section": "printing"}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("nonsense section = %d %s, want 422", rr.Code, rr.Body.String())
	}
}
