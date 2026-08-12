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
	"github.com/Optiminastic/tensor-core/internal/pricing"
)

// addDesignPart declares one named part on a design (its recipe).
func addDesignPart(t *testing.T, store *db.Store, designID uuid.UUID, role string, index, qty int32) {
	t.Helper()
	if _, err := store.Q.InsertDesignPart(context.Background(), gen.InsertDesignPartParams{
		ID: uuid.New(), DesignID: designID, Role: role, PartIndex: index, Quantity: qty,
	}); err != nil {
		t.Fatalf("insert design part %q: %v", role, err)
	}
}

// passPartQc drives one part-job from queued to QC-passed (in_production ->
// completed -> assembly skipped -> QC pass), the path a real part takes.
func passPartQc(t *testing.T, router http.Handler, minter *tokenMinter, jobID string) {
	t.Helper()
	admin := minter.mint(t, []string{"production:update"})
	for _, st := range []string{"in_production", "completed"} {
		if rr := doJSON(router, http.MethodPatch, "/production-jobs/"+jobID, admin,
			map[string]any{"status": st}); rr.Code != http.StatusOK {
			t.Fatalf("patch %s = %d body=%s", st, rr.Code, rr.Body.String())
		}
	}
	asm := minter.mint(t, []string{"assembly:submit"})
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/assembly/skip", asm, nil); rr.Code != http.StatusOK {
		t.Fatalf("skip assembly = %d body=%s", rr.Code, rr.Body.String())
	}
	qc := minter.mint(t, []string{"qc:submit"})
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/qc", qc,
		map[string]any{"decision": "pass"}); rr.Code != http.StatusOK {
		t.Fatalf("qc pass = %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestIntegrationMultiPartAssemblyGating proves the unit gate: a good part that
// passes QC is held while a sibling is still unbuilt, the unit cannot be assembled
// until every part is QC-passed, and once the last part passes the held parts are
// released and the unit assembles.
func TestIntegrationMultiPartAssemblyGating(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)
	ctx := context.Background()
	brand := string(pricing.BrandGifting)

	designID := seedPricedDesign(t, store, brand, verdictGreen)
	sku := "LAMP-2P-001"
	if _, err := store.Q.SetDesignSku(ctx, gen.SetDesignSkuParams{ID: designID, Sku: &sku}); err != nil {
		t.Fatalf("set sku: %v", err)
	}
	addDesignPart(t, store, designID, "body", 0, 1)
	addDesignPart(t, store, designID, "lid", 1, 1)

	orderID := seedOrder(t, store, 8200, []map[string]any{
		{"sku": sku, "product_name": "Trinket Box", "quantity": 1},
	})
	if jobs := fromOrderJobs(t, router, minter, orderID); len(jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(jobs))
	}

	groups, err := store.Q.ListAssemblyGroupsForOrder(ctx, &orderID)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %d err=%v, want 1", len(groups), err)
	}
	groupID := groups[0].ID
	slots, err := store.Q.ListAssemblyGroupParts(ctx, groupID)
	if err != nil {
		t.Fatalf("list slots: %v", err)
	}
	jobByRole := map[string]uuid.UUID{}
	for _, sl := range slots {
		jobByRole[sl.PartRole] = *sl.JobID
	}

	// Pass QC on the body first: it must be held (awaiting its sibling), and the unit
	// must report awaiting_parts and refuse assembly.
	passPartQc(t, router, minter, jobByRole["body"].String())
	body, err := store.Q.GetProductionJobByID(ctx, jobByRole["body"])
	if err != nil {
		t.Fatalf("get body: %v", err)
	}
	if !body.Held {
		t.Error("body should be held while the lid is still unbuilt")
	}
	g, err := store.Q.GetAssemblyGroup(ctx, groupID)
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	if g.Status != "awaiting_parts" {
		t.Errorf("group status = %q, want awaiting_parts", g.Status)
	}
	reader := minter.mint(t, []string{"assembly:submit"})
	if rr := doJSON(router, http.MethodPost, "/assembly-groups/"+groupID.String()+"/assemble", reader, nil); rr.Code != http.StatusConflict {
		t.Errorf("assemble with a missing part = %d, want 409", rr.Code)
	}

	// Pass QC on the lid: now every part is built, so the unit becomes
	// ready_to_assemble and both parts are released.
	passPartQc(t, router, minter, jobByRole["lid"].String())
	g, err = store.Q.GetAssemblyGroup(ctx, groupID)
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	if g.Status != "ready_to_assemble" {
		t.Errorf("group status = %q, want ready_to_assemble", g.Status)
	}
	for role, jobID := range jobByRole {
		j, err := store.Q.GetProductionJobByID(ctx, jobID)
		if err != nil {
			t.Fatalf("get %s: %v", role, err)
		}
		if j.Held {
			t.Errorf("%s should be released once the unit is ready", role)
		}
	}

	// The unit can now be assembled.
	if rr := doJSON(router, http.MethodPost, "/assembly-groups/"+groupID.String()+"/assemble", reader, nil); rr.Code != http.StatusOK {
		t.Fatalf("assemble = %d body=%s", rr.Code, rr.Body.String())
	}
	g, err = store.Q.GetAssemblyGroup(ctx, groupID)
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	if g.Status != "assembled" {
		t.Errorf("group status = %q, want assembled", g.Status)
	}
}

// TestIntegrationMultiPartFanOutAndSelectiveReprint is the core of the multi-part
// feature: a product declared as body x1 + leg x2 explodes into one assembly group
// with three individually-identified part slots, and failing one leg reprints only
// that leg - the slot keeps its stable part_uid and repoints to the reprint, while
// every other part is untouched.
func TestIntegrationMultiPartFanOutAndSelectiveReprint(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)
	ctx := context.Background()
	brand := string(pricing.BrandGifting)

	// A catalogued design that is built from three printed parts: a body and two
	// identical legs.
	designID := seedPricedDesign(t, store, brand, verdictGreen)
	sku := "LAMP-3P-001"
	if _, err := store.Q.SetDesignSku(ctx, gen.SetDesignSkuParams{ID: designID, Sku: &sku}); err != nil {
		t.Fatalf("set sku: %v", err)
	}
	addDesignPart(t, store, designID, "body", 0, 1)
	addDesignPart(t, store, designID, "leg", 1, 2)

	// One ordered unit of that SKU.
	orderID := seedOrder(t, store, 8100, []map[string]any{
		{"sku": sku, "product_name": "Moon Lamp", "quantity": 1},
	})

	jobs := fromOrderJobs(t, router, minter, orderID)
	if len(jobs) != 3 {
		t.Fatalf("multi-part order created %d jobs, want 3 (body + 2 legs)", len(jobs))
	}

	// Exactly one assembly group (one physical unit), still printing.
	groups, err := store.Q.ListAssemblyGroupsForOrder(ctx, &orderID)
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("assembly groups = %d, want 1", len(groups))
	}
	if groups[0].Status != "printing" {
		t.Errorf("group status = %q, want printing", groups[0].Status)
	}

	// Three part slots: body#1, leg#1, leg#2 - each with a distinct, PART-prefixed uid.
	slots, err := store.Q.ListAssemblyGroupParts(ctx, groups[0].ID)
	if err != nil {
		t.Fatalf("list slots: %v", err)
	}
	if len(slots) != 3 {
		t.Fatalf("part slots = %d, want 3", len(slots))
	}
	uids := map[string]struct{}{}
	var legOneJob uuid.UUID
	var legOneUID string
	seenRoles := map[string]int{}
	for _, sl := range slots {
		if sl.JobID == nil {
			t.Fatalf("slot %s/%d has no job", sl.PartRole, sl.PartInstance)
		}
		if _, dup := uids[sl.PartUid]; dup {
			t.Fatalf("duplicate part_uid across slots: %q", sl.PartUid)
		}
		uids[sl.PartUid] = struct{}{}
		seenRoles[sl.PartRole]++
		if sl.PartRole == "leg" && sl.PartInstance == 1 {
			legOneJob = *sl.JobID
			legOneUID = sl.PartUid
		}
	}
	if seenRoles["body"] != 1 || seenRoles["leg"] != 2 {
		t.Fatalf("slot roles = %v, want body:1 leg:2", seenRoles)
	}
	if legOneJob == uuid.Nil {
		t.Fatal("could not find the leg #1 slot/job")
	}

	// Selective reprint: take leg #1 into production and fail it.
	admin := minter.mint(t, []string{"production:update"})
	if rr := doJSON(router, http.MethodPatch, "/production-jobs/"+legOneJob.String(), admin,
		map[string]any{"status": "in_production"}); rr.Code != http.StatusOK {
		t.Fatalf("patch in_production = %d body=%s", rr.Code, rr.Body.String())
	}
	failTok := minter.mint(t, []string{"production:fail"})
	rr := doJSON(router, http.MethodPost, "/production-jobs/"+legOneJob.String()+"/fail", failTok,
		map[string]any{"reason": "warping"})
	if rr.Code != http.StatusOK {
		t.Fatalf("fail = %d body=%s", rr.Code, rr.Body.String())
	}
	var failResp struct {
		FailedJob  jobView `json:"failed_job"`
		ReprintJob jobView `json:"reprint_job"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &failResp)
	reprintID := uuid.MustParse(failResp.ReprintJob.ID)

	// The leg #1 slot now points at the reprint, but keeps its original part_uid -
	// the physical part is the same slot across attempts.
	slots2, err := store.Q.ListAssemblyGroupParts(ctx, groups[0].ID)
	if err != nil {
		t.Fatalf("list slots after reprint: %v", err)
	}
	for _, sl := range slots2 {
		switch {
		case sl.PartRole == "leg" && sl.PartInstance == 1:
			if sl.JobID == nil || *sl.JobID != reprintID {
				t.Errorf("leg #1 slot job = %v, want reprint %s", sl.JobID, reprintID)
			}
			if sl.PartUid != legOneUID {
				t.Errorf("leg #1 part_uid changed on reprint: %q -> %q", legOneUID, sl.PartUid)
			}
		default:
			// Body and leg #2 must be untouched: still their original jobs, not the reprint.
			if sl.JobID == nil || *sl.JobID == reprintID || *sl.JobID == legOneJob {
				t.Errorf("slot %s/%d should be untouched, got job %v", sl.PartRole, sl.PartInstance, sl.JobID)
			}
		}
	}
}
