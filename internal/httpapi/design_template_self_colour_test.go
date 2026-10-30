package httpapi

// A design file that states its own colours.
//
// The rule these pin, and the only one that matters: a flagged file's pieces
// come out of the file, and an UNFLAGGED file is untouched. Every product
// printing today is unflagged, so the second half is what keeps the plank
// range on a bed.
//
// Note the TRUNCATE in each test. design_template_self_colour is keyed on
// template_key with no foreign key - deliberately, so an embedded template can
// be flagged - which means `TRUNCATE products CASCADE` does not reach it and a
// flag set here would leak into the next test in the package.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func truncateSelfColour(t *testing.T, store *db.Store) {
	t.Helper()
	if _, err := store.Pool.Exec(context.Background(),
		`TRUNCATE design_template_self_colour`); err != nil {
		t.Fatalf("truncate self colour: %v", err)
	}
}

func TestIntegrationAnUnflaggedFileKeepsTheConfiguredScheme(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	truncateSelfColour(t, store)
	s := registryServer(t, store)

	// THE SHAPE EVERY PRODUCT PRINTING TODAY HAS. No flag anywhere, so the
	// answer must be exactly what it was before any of this existed: the
	// configured scheme, and the plank's two parts when there is none.
	if parts := s.selfColouredParts(ctx, "dnp_two_heart"); parts != nil {
		t.Errorf("an unflagged template returned %v, want nil - the configured "+
			"scheme must win untouched", parts)
	}
	if parts := s.selfColouredParts(ctx, ""); parts != nil {
		t.Errorf("an empty template key returned %v, want nil", parts)
	}
}

func TestIntegrationFlaggingRefusesAFileThatDeclaresNoColours(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	truncateSelfColour(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.ConfigRead.Key(), auth.ConfigManage.Key()})

	// Flagging a file with nothing to read would leave it falling back to the
	// configured scheme for ever, looking on screen exactly like a flag that
	// does nothing. Refused with the convention spelled out instead.
	rr := doJSON(router, http.MethodPatch, "/registry/templates/dnp_two_heart/self-colour",
		token, map[string]any{"self_coloured": true})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("flagging a plank template = %d %s, want 422", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "_COLOR") {
		t.Errorf("the refusal does not say what a declaration looks like: %s", body)
	}
}

func TestIntegrationTheFlagIsNotSetWithoutSayingSo(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	truncateSelfColour(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.ConfigManage.Key()})

	// Absent is not false. A body that forgot the field would otherwise clear
	// a flag somebody set, and a flagged file silently losing its flag prints
	// the wrong colours on every order after it.
	rr := doJSON(router, http.MethodPatch, "/registry/templates/pwdh/self-colour",
		token, map[string]any{})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("a body with no answer = %d %s, want 422", rr.Code, rr.Body.String())
	}
}

func TestIntegrationTheTemplatesListSaysWhichFilesPaintThemselves(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	truncateSelfColour(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.ConfigRead.Key()})

	// Set directly: the route refuses a file it cannot read, and the renderer
	// is not configured in tests. What is under test here is that the flag
	// reaches the screen.
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO design_template_self_colour (template_key, updated_by)
		 VALUES ('dnp_two_heart', 'test')`); err != nil {
		t.Fatalf("flag: %v", err)
	}

	rr := doJSON(router, http.MethodGet, "/registry/templates", token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rr.Code, rr.Body.String())
	}
	var rows []struct {
		Key          string `json:"key"`
		SelfColoured bool   `json:"self_coloured"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, r := range rows {
		if r.Key == "dnp_two_heart" && !r.SelfColoured {
			t.Error("a flagged file is not reported as stating its own colours")
		}
		if r.Key != "dnp_two_heart" && r.SelfColoured {
			t.Errorf("%s is reported as stating its own colours and is not", r.Key)
		}
	}
}

func TestIntegrationAJobsPartsAreUnchangedForAnUnflaggedProduct(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	truncateSelfColour(t, store)
	s := registryServer(t, store)

	// A job for a product that is not in the registry at all - the plank's
	// position. It must still get the plank's two parts: a white base and the
	// customer's colour on the lettering.
	sku := "T3DPS-DNP-9"
	parts := s.colourPartsForJob(ctx, gen.ProductionJob{Sku: &sku})
	if len(parts) != 2 {
		t.Fatalf("colourPartsForJob gave %d parts, want the plank's 2: %v", len(parts), parts)
	}
	if parts[0].Hex != BasePlateColour || !parts[1].FollowsCustomer() {
		t.Errorf("the plank's parts changed: %v", parts)
	}
}
