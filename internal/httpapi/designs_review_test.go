package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// seedPricedDesign inserts a priced design with a pricing row of the given verdict
// (bypassing the slice worker), and returns its id.
func seedPricedDesign(t *testing.T, store *db.Store, brand, verdict string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	if _, err := store.Q.InsertDesign(ctx, gen.InsertDesignParams{
		ID: id, BrandSlug: brand, Name: "Cube", CreatedBy: "usr_designer", Status: designPriced,
		StlKey: "designs/x.stl", Material: "PLA", Finish: "none", UnitsPerBed: 1, Quality: "standard",
		InfillPct: 15,
	}); err != nil {
		t.Fatalf("insert design: %v", err)
	}
	rec := int32(1199)
	cpr := 0.20
	if err := store.Q.UpsertDesignPricing(ctx, gen.UpsertDesignPricingParams{
		DesignID: id, DesignCp: 240, Breakdown: []byte("{}"), Verdict: verdict, CpPct: 0.24,
		RecommendedSp: &rec, RawSp: 1079, CpPctAtRecommended: &cpr, PassesNormal: true, SurvivesStress: true,
		SpWarnings: []byte("[]"), Reasons: []byte("[]"), Suggestions: []byte("[]"),
	}); err != nil {
		t.Fatalf("upsert pricing: %v", err)
	}
	return id
}

type designView struct {
	Status string `json:"status"`
}

func statusOf(rr interface{ Bytes() []byte }) string {
	var d designView
	_ = json.Unmarshal(rr.Bytes(), &d)
	return d.Status
}

func TestIntegrationDesignReviewFlow(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	designer := minter.mint(t, []string{"design:submit", "design:read"})
	lead := minter.mint(t, []string{"design:approve", "design:reject", "design:read"})

	id := seedPricedDesign(t, store, "gifting", "green").String()

	// A lead cannot submit (no design:submit) -> 403.
	if rr := doJSON(router, http.MethodPost, "/designs/"+id+"/submit", lead, map[string]any{}); rr.Code != http.StatusForbidden {
		t.Errorf("lead submit = %d, want 403", rr.Code)
	}
	// Designer submits a green design.
	rr := doJSON(router, http.MethodPost, "/designs/"+id+"/submit", designer, map[string]any{"message": "please review"})
	if rr.Code != http.StatusOK || statusOf(rr.Body) != "submitted" {
		t.Fatalf("submit = %d status=%q, want 200 submitted; body=%s", rr.Code, statusOf(rr.Body), rr.Body.String())
	}

	// A designer cannot approve (no design:approve) -> 403.
	if rr := doJSON(router, http.MethodPost, "/designs/"+id+"/approve", designer, map[string]any{}); rr.Code != http.StatusForbidden {
		t.Errorf("designer approve = %d, want 403", rr.Code)
	}
	// Reject requires a comment.
	if rr := doJSON(router, http.MethodPost, "/designs/"+id+"/reject", lead, map[string]any{}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("reject without comment = %d, want 422", rr.Code)
	}
	// Lead sends it back with a comment.
	rr = doJSON(router, http.MethodPost, "/designs/"+id+"/reject", lead, map[string]any{"comment": "hollow the base"})
	if rr.Code != http.StatusOK || statusOf(rr.Body) != "changes_requested" {
		t.Fatalf("reject = %d status=%q, want 200 changes_requested", rr.Code, statusOf(rr.Body))
	}
	// Designer re-submits.
	rr = doJSON(router, http.MethodPost, "/designs/"+id+"/submit", designer, map[string]any{})
	if rr.Code != http.StatusOK || statusOf(rr.Body) != "submitted" {
		t.Fatalf("re-submit = %d status=%q, want 200 submitted", rr.Code, statusOf(rr.Body))
	}
	// Lead approves.
	rr = doJSON(router, http.MethodPost, "/designs/"+id+"/approve", lead, map[string]any{})
	if rr.Code != http.StatusOK || statusOf(rr.Body) != "approved" {
		t.Fatalf("approve = %d status=%q, want 200 approved", rr.Code, statusOf(rr.Body))
	}
	// Approving again (not submitted) -> 409.
	if rr := doJSON(router, http.MethodPost, "/designs/"+id+"/approve", lead, map[string]any{}); rr.Code != http.StatusConflict {
		t.Errorf("re-approve = %d, want 409", rr.Code)
	}

	// The review thread carries submit, reject, submit, approve (>= 4 events).
	rr = doJSON(router, http.MethodGet, "/designs/"+id+"/reviews", lead, nil)
	var reviews []reviewResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &reviews)
	if len(reviews) < 4 {
		t.Errorf("review thread has %d entries, want >= 4", len(reviews))
	}
}

func TestIntegrationDesignSubmitBlocksRed(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	designer := minter.mint(t, []string{"design:submit", "design:read"})
	red := seedPricedDesign(t, store, "gifting", "red").String()
	if rr := doJSON(router, http.MethodPost, "/designs/"+red+"/submit", designer, map[string]any{}); rr.Code != http.StatusConflict {
		t.Errorf("submit red = %d, want 409", rr.Code)
	}
}

func TestIntegrationPublishRequiresApproved(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	publisher := minter.mint(t, []string{"shopify:publish", "design:read"})
	// A priced (un-approved) design cannot be published.
	id := seedPricedDesign(t, store, "gifting", "green").String()
	rr := doJSON(router, http.MethodPost, "/designs/"+id+"/publish-shopify", publisher, map[string]any{"title": "Cube", "price": 1199})
	if rr.Code != http.StatusConflict {
		t.Errorf("publish un-approved = %d, want 409", rr.Code)
	}
}

func TestIntegrationDesignComments(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	reader := minter.mint(t, []string{"design:read"})
	id := seedPricedDesign(t, store, "gifting", "green").String()

	if rr := doJSON(router, http.MethodPost, "/designs/"+id+"/comments", reader, map[string]any{"body": "looks good"}); rr.Code != http.StatusCreated {
		t.Fatalf("comment = %d, want 201", rr.Code)
	}
	rr := doJSON(router, http.MethodGet, "/designs/"+id+"/reviews", reader, nil)
	var reviews []reviewResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &reviews)
	if len(reviews) != 1 || reviews[0].Kind != "comment" {
		t.Errorf("reviews = %+v, want one comment", reviews)
	}
}
