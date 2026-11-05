package httpapi

// Whether a brand-new SKU asks its customer to approve a preview.
//
// The column defaults to true, which was right while the registry held
// nothing but planks. It is wrong for a product somebody is still setting up:
// its SKUs went live asking customers to approve a preview file that had not
// been uploaded yet.
//
// "Default it to false" is the obvious fix and it breaks the other half. A new
// DNP colourway arriving from a Shopify sync would land switched off, and
// nobody would see that its customers had quietly stopped being asked. So the
// value is INHERITED from the product's existing SKUs, and only a product with
// none - one being created right now - starts off.

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func newProduct(t *testing.T, store *db.Store, code string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := store.Pool.Exec(context.Background(),
		`INSERT INTO products (id, code, name) VALUES ($1, $2, $2)`, id, code); err != nil {
		t.Fatalf("product: %v", err)
	}
	return id
}

func addVariant(t *testing.T, store *db.Store, productID uuid.UUID, sku string) gen.ProductVariant {
	t.Helper()
	v, err := store.Q.InsertVariant(context.Background(), gen.InsertVariantParams{
		ID: uuid.New(), ProductID: productID, Sku: &sku, Name: sku, Status: "active",
	})
	if err != nil {
		t.Fatalf("insert variant %s: %v", sku, err)
	}
	return v
}

func TestIntegrationANewProductsSKUsDoNotAskTheCustomer(t *testing.T) {
	store := setupStore(t)
	if _, err := store.Pool.Exec(context.Background(), `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// Nothing is configured yet - no preview file, no mapping - so asking a
	// customer to approve it would show them a page for a product that cannot
	// render. Every SKU of the new product agrees, including the second.
	productID := newProduct(t, store, "NEWPROD")
	for _, sku := range []string{"NEWPROD-A", "NEWPROD-B"} {
		if v := addVariant(t, store, productID, sku); v.PersonalisationLinkEnabled {
			t.Errorf("%s asks its customer; a product being set up must start quiet", sku)
		}
	}
}

func TestIntegrationANewColourwayInheritsItsSiblings(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// THE HALF A PLAIN DEFAULT WOULD BREAK. DNP is live and asks its
	// customers; a colourway added by tomorrow's Shopify sync must ask them
	// too, or it silently stops and nothing on any screen says so.
	productID := newProduct(t, store, "DNP")
	first := addVariant(t, store, productID, "DNP-WHITE")
	if _, err := store.Pool.Exec(ctx,
		`UPDATE product_variants SET personalisation_link_enabled = true WHERE id = $1`,
		first.ID); err != nil {
		t.Fatalf("switch the product on: %v", err)
	}

	if v := addVariant(t, store, productID, "DNP-SKYBLUE"); !v.PersonalisationLinkEnabled {
		t.Error("the new colourway does not ask its customer, but its siblings do" +
			" - a live product would quietly stop asking")
	}
}

func TestIntegrationProductsDoNotInheritFromEachOther(t *testing.T) {
	store := setupStore(t)
	if _, err := store.Pool.Exec(context.Background(), `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// Scoped to the product, not the table. With the subselect unscoped, one
	// live product anywhere in the registry would switch on every new SKU of
	// every product being created.
	live := newProduct(t, store, "LIVE")
	asking := addVariant(t, store, live, "LIVE-A")
	if _, err := store.Pool.Exec(context.Background(),
		`UPDATE product_variants SET personalisation_link_enabled = true WHERE id = $1`,
		asking.ID); err != nil {
		t.Fatalf("switch on: %v", err)
	}

	fresh := newProduct(t, store, "FRESH")
	if v := addVariant(t, store, fresh, "FRESH-A"); v.PersonalisationLinkEnabled {
		t.Error("a new product inherited another product's setting")
	}
}

func TestIntegrationAReSyncDoesNotFlipTheToggle(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// THE SHOPIFY IMPORT PATH, which runs on every sync. A re-import is a
	// statement that the shop still sells this SKU - not a decision about
	// whether to ask its customer - so an operator who switched the link OFF
	// must find it off after the next sync.
	productID := newProduct(t, store, "SYNCED")
	sku := "SYNCED-A"
	upsert := func() {
		if err := store.Q.UpsertVariantBySKU(ctx, gen.UpsertVariantBySKUParams{
			ID: uuid.New(), ProductID: productID, Sku: &sku, Name: sku,
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	upsert()

	// Somebody finishes configuring it and turns the link on.
	if _, err := store.Pool.Exec(ctx,
		`UPDATE product_variants SET personalisation_link_enabled = true WHERE lower(sku) = lower($1)`,
		sku); err != nil {
		t.Fatalf("switch on: %v", err)
	}

	upsert() // tomorrow's sync

	var asks bool
	if err := store.Pool.QueryRow(ctx,
		`SELECT personalisation_link_enabled FROM product_variants WHERE lower(sku) = lower($1)`,
		sku).Scan(&asks); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !asks {
		t.Error("a re-sync switched the customer link off; an operator's toggle" +
			" must survive every import")
	}
}
