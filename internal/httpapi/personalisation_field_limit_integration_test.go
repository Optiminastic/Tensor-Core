package httpapi

// The limit a customer's text box gets, resolved against a real registry.
//
// The unit tests beside this pin the precedence; this pins that the LIVE path
// reads it, because the bug was never in the arithmetic. editableFields asked
// fieldMapsFor for the rows, fieldMapsFor answered with the SKU's own and
// nothing else, and the product's number was never consulted by anybody.

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// seedFieldMap writes one mapping row - the product's default when variantID
// is nil, a SKU's own when it is not.
func seedFieldMap(
	t *testing.T, store *db.Store, productID uuid.UUID, variantID *uuid.UUID, maxRunes *int32,
) {
	t.Helper()
	if _, err := store.Pool.Exec(context.Background(), `
		INSERT INTO product_field_maps
		       (id, product_id, variant_id, role, property_key, scad_variable,
		        value_type, required, max_runes, position)
		VALUES ($1, $2, $3, 'body', 'name', 'TEXT', 'string', true, $4, 0)`,
		uuid.New(), productID, variantID, maxRunes); err != nil {
		t.Fatalf("seed field map: %v", err)
	}
}

func limitForSKU(t *testing.T, store *db.Store, sku string) int {
	t.Helper()
	s := &Server{store: store}
	job := gen.ProductionJob{Sku: &sku, PartRole: designRoleBody}
	editable, limits := s.editableFields(context.Background(), job)
	if !editable["name"] {
		t.Fatal("the name field is not editable; the mapping did not resolve")
	}
	return limits["name"]
}

func TestIntegrationASKUWithNoLimitInheritsTheProducts(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// EXACTLY THE SHIPPED STATE. PDHR was configured for 20 at product level
	// and every child SKU carried its own mapping with the limit blank, so
	// fieldMapsFor returned the SKU's rows, the product's 20 was never read,
	// and the box enforced the built-in default of twelve. Customers whose
	// names ran to 13 and 19 characters could not retype them.
	productID, variantID := seedProductWithVariant(t, store, "PDHR", "PDHR-G")
	twenty := int32(20)
	seedFieldMap(t, store, productID, nil, &twenty)
	seedFieldMap(t, store, productID, &variantID, nil)

	if got := limitForSKU(t, store, "PDHR-G"); got != 20 {
		t.Errorf("the SKU's limit is %d, want the product's 20", got)
	}
}

func TestIntegrationASKUsOwnLimitStillWins(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// Inheriting must not become overriding: a colourway printed from a
	// smaller drawing keeps the number somebody set against it.
	productID, variantID := seedProductWithVariant(t, store, "PWDH", "PWDH-P")
	twenty, eight := int32(20), int32(8)
	seedFieldMap(t, store, productID, nil, &twenty)
	seedFieldMap(t, store, productID, &variantID, &eight)

	if got := limitForSKU(t, store, "PWDH-P"); got != 8 {
		t.Errorf("the SKU's limit is %d, want its own 8", got)
	}
}
