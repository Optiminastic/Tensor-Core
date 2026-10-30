package httpapi

// Changing your mind has to change the DRAWING, not only the palette.
//
// A colourway is expressed two ways in this registry. A Lotus is one file
// plus a scheme per variant, so re-resolving the SCHEME was enough and that
// is what variantForColourway was written for. A Royal holder is a file per
// child SKU, each already in its own colours and declaring them itself - so
// for that product THE FILE IS THE COLOUR.
//
// With the scheme following the customer and the file not, a customer who
// bought the purple holder and chose RED saw the purple one, under a
// countdown saying this is exactly how it will be made. The printed model
// resolved its file the same way, which is the half that mattered: they could
// have approved red and been sent purple, with the order, the picture and the
// model all agreeing it was purple.

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func TestIntegrationChangingColourChangesTheFileNotJustThePalette(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s := registryServer(t, store)

	productID := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO products (id, code, name) VALUES ($1, 'PDHR', 'Royal')`,
		productID); err != nil {
		t.Fatalf("product: %v", err)
	}
	for _, v := range []struct{ sku, name, body, preview string }{
		{"PDHR-P", "Purple", "pdhr-p", "pdhr-p-per"},
		{"PDHR-R", "Red", "pdhr-r", "pdhr-r-per"},
	} {
		variantID := uuid.New()
		if _, err := store.Pool.Exec(ctx,
			`INSERT INTO product_variants (id, product_id, name, sku) VALUES ($1, $2, $3, $4)`,
			variantID, productID, v.name, v.sku); err != nil {
			t.Fatalf("variant %s: %v", v.sku, err)
		}
		for role, key := range map[string]string{"body": v.body, "preview": v.preview} {
			if _, err := store.Pool.Exec(ctx,
				`INSERT INTO variant_designs (id, variant_id, role, template_key)
				 VALUES ($1, $2, $3, $4)`,
				uuid.New(), variantID, role, key); err != nil {
				t.Fatalf("design %s/%s: %v", v.sku, role, err)
			}
		}
	}

	bought := "PDHR-P"
	job := gen.ProductionJob{Sku: &bought}

	// Nobody has changed anything: the colourway they bought is the one they
	// get, and nothing about this may move.
	if got := s.skuForColourway(ctx, productID, bought, job); got != "PDHR-P" {
		t.Errorf("unchanged job resolves to %q, want PDHR-P", got)
	}
	if key, ok := s.previewTemplateFor(ctx, productID, bought, job); !ok || key != "pdhr-p-per" {
		t.Errorf("preview file = %q (found=%v), want pdhr-p-per", key, ok)
	}

	// They chose RED on the link.
	red := "Red"
	job.PersonalisationColour = &red

	if got := s.skuForColourway(ctx, productID, bought, job); got != "PDHR-R" {
		t.Errorf("after choosing red the files come from %q, want PDHR-R", got)
	}
	key, ok := s.previewTemplateFor(ctx, productID, bought, job)
	if !ok || key != "pdhr-r-per" {
		t.Errorf("preview file = %q (found=%v), want pdhr-r-per - the customer saved red "+
			"and was shown the colourway they rejected", key, ok)
	}
	// The printed file follows the same rule, which is the half that reaches
	// a printer.
	if got, ok := s.templateForVariant(ctx, productID,
		s.skuForColourway(ctx, productID, bought, job), designRoleBody); !ok || got != "pdhr-r" {
		t.Errorf("printed file = %q (found=%v), want pdhr-r - they would approve red "+
			"and be sent purple", got, ok)
	}
}

func TestIntegrationAColourThisProductDoesNotSellKeepsTheBoughtFile(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s := registryServer(t, store)
	productID, _ := seedProductWithVariant(t, store, "PDHR", "PDHR-P")

	bought := "PDHR-P"
	teal := "Teal"
	job := gen.ProductionJob{Sku: &bought, PersonalisationColour: &teal}

	// The picker no longer offers a colour this product has no variant for,
	// but an older job may carry one. The bought colourway's drawing is a
	// better answer than none.
	if got := s.skuForColourway(ctx, productID, bought, job); got != "PDHR-P" {
		t.Errorf("got %q, want the bought SKU kept when the colour is not a variant", got)
	}
}

func TestIntegrationBuyingRedAndChoosingPurplePrintsThePurpleFile(t *testing.T) {
	// THE CASE THE SHOP ASKED ABOUT, in its own words: somebody orders
	// PDHR-R, picks purple on the link, and presses Save. What is printed
	// must be the purple drawing with the name they left in the box - not the
	// red drawing they stopped wanting.
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s := registryServer(t, store)

	productID := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO products (id, code, name) VALUES ($1, 'PDHR', 'Royal')`,
		productID); err != nil {
		t.Fatalf("product: %v", err)
	}
	for _, v := range []struct{ sku, name, body, preview string }{
		{"PDHR-P", "Purple", "pdhr-p", "pdhr-p-per"},
		{"PDHR-R", "Red", "pdhr-r", "pdhr-r-per"},
	} {
		variantID := uuid.New()
		if _, err := store.Pool.Exec(ctx,
			`INSERT INTO product_variants (id, product_id, name, sku) VALUES ($1, $2, $3, $4)`,
			variantID, productID, v.name, v.sku); err != nil {
			t.Fatalf("variant %s: %v", v.sku, err)
		}
		for role, key := range map[string]string{"body": v.body, "preview": v.preview} {
			if _, err := store.Pool.Exec(ctx,
				`INSERT INTO variant_designs (id, variant_id, role, template_key)
				 VALUES ($1, $2, $3, $4)`,
				uuid.New(), variantID, role, key); err != nil {
				t.Fatalf("design %s/%s: %v", v.sku, role, err)
			}
		}
	}

	boughtRed := "PDHR-R"
	chosePurple := "Purple"
	job := gen.ProductionJob{Sku: &boughtRed, PersonalisationColour: &chosePurple}

	if got := s.skuForColourway(ctx, productID, boughtRed, job); got != "PDHR-P" {
		t.Fatalf("files come from %q, want PDHR-P", got)
	}
	printed, ok := s.templateForVariant(ctx, productID,
		s.skuForColourway(ctx, productID, boughtRed, job), designRoleBody)
	if !ok || printed != "pdhr-p" {
		t.Errorf("printed file = %q (found=%v), want pdhr-p", printed, ok)
	}
	shown, ok := s.previewTemplateFor(ctx, productID, boughtRed, job)
	if !ok || shown != "pdhr-p-per" {
		t.Errorf("preview file = %q (found=%v), want pdhr-p-per", shown, ok)
	}

	// And the shortcut that would have kept the red drawing refuses to run.
	unchanged := gen.ProductionJob{Sku: &boughtRed}
	if !s.recolouringWouldChangeTheFile(ctx, unchanged, "Purple") {
		t.Error("the recolour shortcut would repaint the red drawing purple")
	}
	// A colour that resolves to the same file is still a plain recolour.
	if s.recolouringWouldChangeTheFile(ctx, unchanged, "Red") {
		t.Error("choosing the colour already on the order refused the fast path")
	}
}
