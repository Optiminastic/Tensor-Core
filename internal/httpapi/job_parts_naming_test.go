package httpapi

// An order for a product that prints three things becomes three jobs, named
// for what each one prints.

import (
	"context"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
)

// seedComboProduct gives a product three parts, each with a file and a mapped
// field - which is what partRolesForRenderSKU requires before it will make a
// job for a role.
func seedComboProduct(t *testing.T, store *db.Store, code, sku string, roles []string) {
	t.Helper()
	ctx := context.Background()
	productID, variantID := seedProductWithVariant(t, store, code, sku)
	for _, role := range roles {
		seedDesign(t, store, variantID, role, code+"_"+role)
		if _, err := store.Pool.Exec(ctx, `
			INSERT INTO product_field_maps
			       (id, product_id, role, property_key, scad_variable, value_type, required, position)
			VALUES ($1, $2, $3, 'name', 'TEXT', 'string', true, 0)`,
			uuid.New(), productID, role); err != nil {
			t.Fatalf("map %s: %v", role, err)
		}
	}
}

func jobNumbersForOrder(t *testing.T, store *db.Store, orderID uuid.UUID) []string {
	t.Helper()
	rows, err := store.Pool.Query(context.Background(),
		`SELECT job_number FROM production_jobs WHERE order_id = $1`, orderID)
	if err != nil {
		t.Fatalf("read jobs: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func TestIntegrationAComboBecomesOneJobPerPartNamedForIt(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedComboProduct(t, store, "SC", "SC-PNK", []string{"dnp", "rose", "keychain"})

	minter := newTokenMinter(t)
	s := NewServer(config.Settings{Environment: "development", AuthAudience: "tensor-core"},
		store, auth.NewGuards(minter.verifier, ""), nil)

	orderID := seedOrder(t, store, 16456, []map[string]any{
		{"product_id": "SC", "sku": "SC-PNK", "product_name": "Soulmate COMBO",
			"quantity": 1, "material": "PLA"},
	})
	if _, err := s.CreateJobsForOrder(ctx, orderID); err != nil {
		t.Fatalf("create jobs: %v", err)
	}

	got := jobNumbersForOrder(t, store, orderID)
	if len(got) != 3 {
		t.Fatalf("got %d jobs (%v), want one per part", len(got), got)
	}
	// The order's own digits, then the part. -2 and -3 said nothing about
	// which of three an operator was holding.
	for _, want := range []string{"-DNP", "-KEYCHAIN", "-ROSE"} {
		found := false
		for _, n := range got {
			if len(n) > len(want) && n[len(n)-len(want):] == want {
				found = true
			}
		}
		if !found {
			t.Errorf("no job named for %s; got %v", want, got)
		}
	}
}

func TestIntegrationAProductThatPrintsOneThingKeepsItsPlainNumber(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// THE REST OF THE SHOP. Renaming these would change every number on every
	// screen, and in every operator's head, to say something they know.
	seedComboProduct(t, store, "TNP", "TNP-RED", []string{"body"})

	minter := newTokenMinter(t)
	s := NewServer(config.Settings{Environment: "development", AuthAudience: "tensor-core"},
		store, auth.NewGuards(minter.verifier, ""), nil)

	orderID := seedOrder(t, store, 16999, []map[string]any{
		{"product_id": "TNP", "sku": "TNP-RED", "product_name": "Triple Name Plank",
			"quantity": 1, "material": "PLA"},
	})
	if _, err := s.CreateJobsForOrder(ctx, orderID); err != nil {
		t.Fatalf("create jobs: %v", err)
	}

	got := jobNumbersForOrder(t, store, orderID)
	if len(got) != 1 {
		t.Fatalf("got %d jobs (%v), want 1", len(got), got)
	}
	for _, bad := range []string{"-BODY", "-DNP"} {
		if len(got[0]) > len(bad) && got[0][len(got[0])-len(bad):] == bad {
			t.Errorf("a single-part product was renamed: %s", got[0])
		}
	}
}
