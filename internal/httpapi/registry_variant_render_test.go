package httpapi

// A SKU configured ENTIRELY through its own override still renders.
//
// Three questions decide whether an arriving order becomes a job at all, and
// all three used to be answered from ListAllProductFieldMaps - one read of
// every mapping a product has, product-level and per-SKU alike:
//
//   - registryRendersSKU: does Tensor build this product itself?
//   - partRolesForSKU:    how many jobs does one order line become?
//   - and through them, rendersProduct, which job creation calls
//
// Scoping that read to the product's own rows is the obvious tidy-up and it is
// a trap. A SKU whose only mapping is its own would then answer "no mapping",
// which means no roles, which means NO JOB CREATED - and rendersProduct false,
// so the line falls through to the hardcoded plank path instead. Silently, on
// a paid order, with nothing on any screen to say why the job never appeared.
//
// So the product-level read is scoped and these two questions are asked per
// SKU instead, through the same variant-first fieldMapsFor the renderer uses.
// This test is what keeps them that way.

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
)

// registryServer is a Server against the test database, for the internal
// resolvers that have no HTTP route of their own.
func registryServer(t *testing.T, store *db.Store) *Server {
	t.Helper()
	minter := newTokenMinter(t)
	return NewServer(config.Settings{
		Environment: "development", AuthAudience: "tensor-core",
	}, store, auth.NewGuards(minter.verifier, ""), nil)
}

func TestIntegrationASKUMappedOnlyByItselfStillMakesAJob(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s := registryServer(t, store)

	productID, variantID := seedProductWithVariant(t, store, "PWDH", "PWDH-P")

	// A design file for the SKU, and a mapping that belongs ONLY to it. No
	// product-level mapping exists at all - which is the whole point: this is
	// a product configured per SKU from the start.
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO variant_designs (id, variant_id, role, template_key)
		 VALUES ($1, $2, 'body', 'pwdh_purple')`,
		uuid.New(), variantID); err != nil {
		t.Fatalf("variant design: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO product_field_maps
		   (id, product_id, variant_id, property_key, scad_variable, value_type, role,
		    required, position)
		 VALUES ($1, $2, $3, 'name', 'TEXT', 'string', 'body', true, 0)`,
		uuid.New(), productID, variantID); err != nil {
		t.Fatalf("variant map: %v", err)
	}

	// 1. Does Tensor build it? Answered per SKU, so yes.
	if !s.registryRendersSKU(ctx, "PWDH-P") {
		t.Error("registryRendersSKU says no for a SKU whose only mapping is its own, " +
			"so job creation would route this order to the plank path")
	}
	// Case-insensitively, because an order's SKU arrives however the
	// storefront wrote it.
	if !s.registryRendersSKU(ctx, "pwdh-p") {
		t.Error("registryRendersSKU is case-sensitive about the SKU")
	}

	// 2. How many jobs does one line become? One, for the body.
	roles := s.partRolesForSKU(ctx, "PWDH-P")
	if len(roles) != 1 || roles[0] != "body" {
		t.Errorf("partRolesForSKU = %v, want exactly [body] - an empty list is a line "+
			"that becomes no job", roles)
	}

	// 3. And the whole question job creation actually asks.
	if !s.rendersProduct(ctx, "PWDH-P", "Personalized Wall Diya Holder - Lotus") {
		t.Error("rendersProduct says no, so buildJobsForOrder would look for a " +
			"designer's file that is never coming")
	}
}

func TestIntegrationTheProductsOwnMappingStillAnswersForItsSKUs(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s := registryServer(t, store)

	// The shape every product printing today has: one file and one mapping
	// for the whole product, inherited by every SKU. This must keep answering
	// exactly as it did - it is how the plank range reaches a bed.
	productID, variantID := seedProductWithVariant(t, store, "DNPX", "DNPX-BLU")
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO variant_designs (id, variant_id, role, template_key)
		 VALUES ($1, $2, 'body', 'dnp_two_heart')`,
		uuid.New(), variantID); err != nil {
		t.Fatalf("variant design: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO product_field_maps
		   (id, product_id, property_key, scad_variable, value_type, role, required, position)
		 VALUES ($1, $2, 'name', 'NAME_L', 'string', 'body', true, 0)`,
		uuid.New(), productID); err != nil {
		t.Fatalf("product map: %v", err)
	}

	if !s.registryRendersSKU(ctx, "DNPX-BLU") {
		t.Error("a SKU inheriting its product's mapping no longer renders")
	}
	if roles := s.partRolesForSKU(ctx, "DNPX-BLU"); len(roles) != 1 || roles[0] != "body" {
		t.Errorf("partRolesForSKU = %v, want [body]", roles)
	}
}

func TestIntegrationThePreviewRoleStillMakesNoJob(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s := registryServer(t, store)

	productID, variantID := seedProductWithVariant(t, store, "PWDH", "PWDH-R")
	for _, role := range []string{"body", "preview"} {
		if _, err := store.Pool.Exec(ctx,
			`INSERT INTO variant_designs (id, variant_id, role, template_key)
			 VALUES ($1, $2, $3, 'pwdh_red')`,
			uuid.New(), variantID, role); err != nil {
			t.Fatalf("design %s: %v", role, err)
		}
		// Mapped per SKU, INCLUDING the preview - the case the explicit
		// refusal in partRolesForRender exists for. The assembled model is
		// drawn for the customer and never put on a bed; a job for it would
		// fuse a diya cup to a plate at ninety degrees.
		if _, err := store.Pool.Exec(ctx,
			`INSERT INTO product_field_maps
			   (id, product_id, variant_id, property_key, scad_variable, value_type, role,
			    required, position)
			 VALUES ($1, $2, $3, 'name', 'TEXT', 'string', $4, true, 0)`,
			uuid.New(), productID, variantID, role); err != nil {
			t.Fatalf("map %s: %v", role, err)
		}
	}

	roles := s.partRolesForSKU(ctx, "PWDH-R")
	if len(roles) != 1 || roles[0] != "body" {
		t.Errorf("partRolesForSKU = %v, want [body] only - the preview must never "+
			"become a job, however it is mapped", roles)
	}
}
