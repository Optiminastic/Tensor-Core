package httpapi

// What a design file is used FOR, on the tab that lists the files.
//
// The rule worth pinning is that ONE FILE CAN HOLD TWO ROLES AT ONCE. The
// lotus holder's .scad draws both the plate that prints and the assembled
// picture the customer approves, so PWDH links it twice - and a listing that
// collapsed a file to a single use would have to pick one of those and would
// be wrong about the other, on the screen built to answer exactly that
// question.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
)

type templateListRow struct {
	Key    string `json:"key"`
	UsedBy []struct {
		ProductCode string `json:"product_code"`
		Role        string `json:"role"`
		SKUs        int64  `json:"skus"`
	} `json:"used_by"`
}

func TestIntegrationTemplateListSaysWhatEachFileIsFor(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.ConfigRead.Key(), auth.ConfigManage.Key()})

	// The uploaded file itself. A key reaches the Designs tab by being
	// uploaded or embedded, and setProductDesign refuses a key that is
	// neither - so a test that linked a key with no file behind it would be
	// describing a state the API cannot produce.
	fileID := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO file_assets (id, filename, content_type, size_bytes, storage_key, uploaded_by)
		 VALUES ($1, 'lotus_diya_holder.scad', 'text/plain', 207807, 'templates/pwdh/x.scad', 'test')`,
		fileID); err != nil {
		t.Fatalf("file asset: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO design_templates (id, template_key, file_id, version, uploaded_by)
		 VALUES ($1, 'pwdh', $2, 1, 'test')`,
		uuid.New(), fileID); err != nil {
		t.Fatalf("design template: %v", err)
	}

	productID := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO products (id, code, name) VALUES ($1, 'PWDH', 'Lotus Diya Holder')`,
		productID); err != nil {
		t.Fatalf("product: %v", err)
	}
	// Two colourways, so the count is a count of something.
	for _, sku := range []string{"PWDH-P", "PWDH-R"} {
		variantID := uuid.New()
		if _, err := store.Pool.Exec(ctx,
			`INSERT INTO product_variants (id, product_id, name, sku) VALUES ($1, $2, $3, $3)`,
			variantID, productID, sku); err != nil {
			t.Fatalf("variant %s: %v", sku, err)
		}
		for _, role := range []string{"body", "preview"} {
			if _, err := store.Pool.Exec(ctx,
				`INSERT INTO variant_designs (id, variant_id, role, template_key)
				 VALUES ($1, $2, $3, 'pwdh')`,
				uuid.New(), variantID, role); err != nil {
				t.Fatalf("design %s/%s: %v", sku, role, err)
			}
		}
	}

	rr := doJSON(router, http.MethodGet, "/registry/templates", token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list templates = %d %s", rr.Code, rr.Body.String())
	}
	var rows []templateListRow
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}

	byRole := map[string]int64{}
	found := false
	for _, row := range rows {
		if row.Key != "pwdh" {
			continue
		}
		found = true
		for _, use := range row.UsedBy {
			if use.ProductCode != "PWDH" {
				t.Errorf("used_by names %q, want PWDH", use.ProductCode)
			}
			byRole[use.Role] = use.SKUs
		}
	}
	if !found {
		t.Fatalf("pwdh is not in the list at all: %s", rr.Body.String())
	}
	if byRole["body"] != 2 {
		t.Errorf("body use = %d SKUs, want 2", byRole["body"])
	}
	// The one that matters: the preview use must survive beside the printing
	// one rather than being collapsed into it.
	if byRole["preview"] != 2 {
		t.Errorf("preview use = %d SKUs, want 2", byRole["preview"])
	}

	// A built-in template nothing links to reports no uses rather than being
	// left out - a file uploaded and never linked is the thing this column
	// exists to make visible.
	for _, row := range rows {
		if row.Key == "dnp_two_heart" && len(row.UsedBy) != 0 {
			t.Errorf("dnp_two_heart claims %d uses, want none", len(row.UsedBy))
		}
	}
}
