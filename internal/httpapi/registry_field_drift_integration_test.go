package httpapi

// The wiring, end to end: a reworded storefront label reaching the readiness
// panel, and a mapping proposed from a file and a product's own orders.
//
// The unit tests settle the rules. These settle that the rules are actually
// consulted - that the per-product order window reads the right lines, that
// matchesProduct still decides, and that the sentence reaches the panel the
// operator reads. Every one of those is a seam the unit tests cannot see.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
)

// seedMapRow writes one mapping row with a chosen property key.
//
// Its own seeder rather than seedFieldMap's, which hardcodes the key as
// "name" and required as true - and the key and the requiredness are the two
// things every test here varies.
func seedMapRow(
	t *testing.T, store *db.Store, productID uuid.UUID, key string, required bool,
) {
	t.Helper()
	if _, err := store.Pool.Exec(context.Background(), `
		INSERT INTO product_field_maps
		       (id, product_id, role, property_key, scad_variable,
		        value_type, required, position)
		VALUES ($1, $2, 'body', $3, 'TEXT', 'string', $4, 0)`,
		uuid.New(), productID, key, required); err != nil {
		t.Fatalf("seed field map: %v", err)
	}
}

// testServerWithRenderer is testServer with a renderer attached.
//
// The suggester has to read the file to know what it declares, and a server
// with no renderer answers 503 - correctly, but it means this test would
// prove only that the refusal works. NewRenderer never runs OpenSCAD here:
// Source reads the template compiled into the binary, so an empty binary
// path and asset directory are enough.
func testServerWithRenderer(t *testing.T, store *db.Store, guards *auth.Guards) http.Handler {
	t.Helper()
	srv := NewServer(config.Settings{
		Environment:  "development",
		AuthAudience: "tensor-core",
		CORSOrigins:  []string{"http://localhost:3001"},
	}, store, guards, nil)
	srv.EnableModelGeneration(personalise.NewRenderer("", "", 0), nil)
	return srv.Router()
}

// shopifyID hands out a distinct shopify_order_id per seeded order. The
// column is NOT NULL and unique, and a fixed value would make the second
// order in a test collide with the first.
var shopifyID atomic.Int64

// seedOrderWithProperty writes one order carrying one line of this product
// with one answered property.
func seedOrderWithProperty(
	t *testing.T, store *db.Store, number, sku, label, value string,
) {
	t.Helper()
	line := map[string]any{
		"sku": sku, "product_name": "Seeded Product", "quantity": 1, "unit": "pcs",
		"properties": []map[string]string{{"name": label, "value": value}},
	}
	lines, err := json.Marshal([]any{line})
	if err != nil {
		t.Fatalf("marshal line: %v", err)
	}
	if _, err := store.Pool.Exec(context.Background(), `
		INSERT INTO orders (id, shopify_order_id, order_number, source, line_items,
		                    placed_at, financial_status, total_price, currency, status)
		VALUES ($1, $2, $3, 'seed', $4, now(), 'paid', 0, 'INR', 'imported')`,
		uuid.New(), shopifyID.Add(1), number, lines); err != nil {
		t.Fatalf("seed order: %v", err)
	}
}

func readinessProblems(t *testing.T, store *db.Store, code string) []skuReadiness {
	t.Helper()
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key()})

	rr := doJSON(router, http.MethodGet, "/registry/products/"+code+"/readiness", token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("readiness: %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Items []skuReadiness `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Items
}

// A storefront label reworded after the mapping was written. This is the
// silent failure the whole check exists for: the field is OPTIONAL, so no
// order is held, the model renders without the name, and OpenSCAD exits 0.
func TestIntegrationARewordedLabelReachesTheReadinessPanel(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	for _, table := range []string{"products", "orders"} {
		if _, err := store.Pool.Exec(ctx, `TRUNCATE `+table+` CASCADE`); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}

	productID, variantID := seedProductWithVariant(t, store, "DRIFT", "DRIFT-A")
	seedDesign(t, store, variantID, designRoleBody, "pdhr-g")
	// Mapped against the OLD wording, as it would have been when written.
	seedMapRow(t, store, productID, "first name", false)
	// And the orders now carry the new one.
	seedOrderWithProperty(t, store, "T-9001", "DRIFT-A", "Your First Name", "Asha")

	items := readinessProblems(t, store, "DRIFT")
	if len(items) != 1 {
		t.Fatalf("got %d SKUs, want 1", len(items))
	}

	said := strings.Join(items[0].Problems, " | ")
	if !strings.Contains(said, `"first name"`) {
		t.Fatalf("the panel does not name the stale field: %s", said)
	}
	// The pairing is the point: it names what to re-map TO, not just that
	// something is wrong.
	if !strings.Contains(said, "Your First Name") {
		t.Fatalf("the panel does not name the new wording: %s", said)
	}
	if items[0].Ready {
		t.Error("the SKU still reads as ready to take an order")
	}
}

// A mapping that still matches must stay silent, or the panel is noise on
// every product and nobody reads the one that matters.
func TestIntegrationAMatchingMappingAddsNoProblem(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	for _, table := range []string{"products", "orders"} {
		if _, err := store.Pool.Exec(ctx, `TRUNCATE `+table+` CASCADE`); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}

	productID, variantID := seedProductWithVariant(t, store, "DRIFT", "DRIFT-A")
	seedDesign(t, store, variantID, designRoleBody, "pdhr-g")
	seedMapRow(t, store, productID, "your first name", false)
	seedOrderWithProperty(t, store, "T-9002", "DRIFT-A", "Your First Name", "Asha")

	for _, problem := range readinessProblems(t, store, "DRIFT")[0].Problems {
		if strings.Contains(problem, "stopped arriving") ||
			strings.Contains(problem, "No recent order carried") {
			t.Fatalf("spoke about a correct mapping: %s", problem)
		}
	}
}

// ANOTHER PRODUCT'S ORDERS MUST NOT ANSWER FOR THIS ONE. The window is
// narrowed in SQL and then filtered by matchesProduct in Go, and if the Go
// half were dropped a busy product's labels would appear under every product
// whose name happened to share a prefix.
func TestIntegrationAnotherProductsOrdersDoNotCount(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	for _, table := range []string{"products", "orders"} {
		if _, err := store.Pool.Exec(ctx, `TRUNCATE `+table+` CASCADE`); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}

	productID, variantID := seedProductWithVariant(t, store, "DRIFT", "DRIFT-A")
	seedDesign(t, store, variantID, designRoleBody, "pdhr-g")
	seedMapRow(t, store, productID, "first name", false)
	// A line of a DIFFERENT SKU and a different product name, carrying a
	// label that would otherwise read as the rename.
	seedOrderWithProperty(t, store, "T-9003", "OTHER-A", "Your First Name", "Asha")

	items := readinessProblems(t, store, "DRIFT")
	said := strings.Join(items[0].Problems, " | ")
	if strings.Contains(said, "Your First Name") {
		t.Fatalf("another product's label was read as this one's: %s", said)
	}
	// With no evidence of its own, this product gets no verdict either way -
	// which is what keeps a red line off every product being set up.
	if strings.Contains(said, "stopped arriving") {
		t.Fatalf("judged a product with no orders of its own: %s", said)
	}
}

// The suggester over the real seam: a file read through the renderer, a
// product's own orders, and one HTTP call.
func TestIntegrationAMappingIsProposedFromTheFileAndTheOrders(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	for _, table := range []string{"products", "orders"} {
		if _, err := store.Pool.Exec(ctx, `TRUNCATE `+table+` CASCADE`); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}

	_, variantID := seedProductWithVariant(t, store, "SUGG", "SUGG-A")
	// An embedded template, so this does not depend on anything uploaded.
	seedDesign(t, store, variantID, designRoleBody, "dnp_heart_joinery")
	seedOrderWithProperty(t, store, "T-9004", "SUGG-A", "STEP 4-First Name-:", "Asha")

	minter := newTokenMinter(t)
	router := testServerWithRenderer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key()})

	rr := doJSON(router, http.MethodGet,
		"/registry/products/SUGG/field-suggestions?role="+designRoleBody, token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("suggestions: %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		TemplateKey string                    `json:"template_key"`
		Items       []fieldSuggestionResponse `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.TemplateKey != "dnp_heart_joinery" {
		t.Fatalf("read the wrong file: %q", body.TemplateKey)
	}

	// The joinery file calls its two names name1 and name2, and the order
	// carries a first name - so name1 is the pairing, and name2 has nothing
	// to pair with.
	by := byVariable(body.Items)
	row, ok := by["name1"]
	if !ok {
		t.Fatalf("name1 was not proposed; got %+v", body.Items)
	}
	if row.PropertyKey != "step 4 first name" {
		t.Fatalf("name1 proposed %q", row.PropertyKey)
	}
	if row.Sample != "Asha" {
		t.Fatalf("no real answer shown: %q", row.Sample)
	}
	if _, wrong := by["name2"]; wrong {
		t.Error("name2 was paired with something, with only a first name on file")
	}
	// PART is the renderer's own. Offering it would let a mapping draw the
	// same piece on every pass - the 3MF then holds overlapping copies of the
	// model in different colours, and it slices and prints.
	if _, wrong := by["PART"]; wrong {
		t.Error("PART was offered for mapping")
	}
}

// A part with no file yet cannot be mapped, and saying so names the step
// somebody has to take - rather than returning an empty list that reads as
// "this design declares no variables".
func TestIntegrationSuggestionsRefuseAPartWithNoFile(t *testing.T) {
	store := setupStore(t)
	if _, err := store.Pool.Exec(context.Background(),
		`TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedProductWithVariant(t, store, "NOFILE", "NOFILE-A")

	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key()})

	rr := doJSON(router, http.MethodGet,
		"/registry/products/NOFILE/field-suggestions", token, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "fields panel") {
		t.Errorf("the refusal does not say where to go: %s", rr.Body.String())
	}
}
