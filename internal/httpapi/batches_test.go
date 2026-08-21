package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// seedFileAsset inserts a file_assets row with the given bounding box and returns
// its id (no object-storage bytes; only the metadata the planner reads).
// givePrintFile makes a job batchable the way the product does: attach a print
// file and clear the validation flag.
//
// Both steps are needed. A job created from an order whose SKU has no approved
// design is stamped issue_reason = no_approved_design (see applyMatch), and
// ListBatchableJobs excludes any flagged job - by design, so a job with an
// unresolved problem is never printed. SetProductionJobPrintFile only clears
// the flag when it is exactly 'stl_missing', because an STL upload is not the
// remedy for a missing design. Clearing the rest is a deliberate human act
// (PATCH issue_reason, Admin/Project Lead only), which is what this stands in
// for.
func givePrintFile(t *testing.T, store *db.Store, jobID, fileID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Q.SetProductionJobPrintFile(ctx, gen.SetProductionJobPrintFileParams{
		ID: jobID, PrintFileID: &fileID,
	}); err != nil {
		t.Fatalf("set print file: %v", err)
	}
	if _, err := store.Q.UpdateProductionJobFields(ctx, gen.UpdateProductionJobFieldsParams{
		ID: jobID, SetIssueReason: true, IssueReason: nil,
	}); err != nil {
		t.Fatalf("clear issue reason: %v", err)
	}
}

func seedFileAsset(t *testing.T, store *db.Store, x, y, z float64) uuid.UUID {
	t.Helper()
	f, err := store.Q.InsertFileAsset(context.Background(), gen.InsertFileAssetParams{
		ID: uuid.New(), Filename: "model.stl", ContentType: "model/stl", SizeBytes: 10,
		StorageKey: "files/x.stl", UploadedBy: "usr_admin", BboxXMm: &x, BboxYMm: &y, BboxZMm: &z,
	})
	if err != nil {
		t.Fatalf("insert file asset: %v", err)
	}
	return f.ID
}

// seedFleetMachineWithProfile inserts a physical fleet machine linked to a
// new machine_profiles row of the given family/status - the pair
// assignMachineForBatch actually schedules over (unlike seedMachine, which
// only seeds the profile half). Returns the profile id, since that's what
// batches.machine_id and PATCH /machines/:id both address.
func seedFleetMachineWithProfile(t *testing.T, store *db.Store, code, family, status string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	p, err := store.Q.InsertMachineProfileFull(ctx, gen.InsertMachineProfileFullParams{
		ID: uuid.New(), Name: "Bambu " + code, IsActive: true, Family: family,
		NozzleMm: 0.4, Flow: "standard", LayerHeightMinMm: 0.08, LayerHeightMaxMm: 0.28,
		SupportedFilaments: []byte("[]"), Status: status, IsDefault: false,
	})
	if err != nil {
		t.Fatalf("insert machine profile: %v", err)
	}
	m, err := store.Q.InsertFleetMachine(ctx, gen.InsertFleetMachineParams{
		ID: uuid.New(), MachineID: code, Name: "Bambu " + code + " unit", Status: "idle", Filaments: []byte("[]"),
	})
	if err != nil {
		t.Fatalf("insert fleet machine: %v", err)
	}
	if _, err := store.Q.SetFleetMachineProfile(ctx, gen.SetFleetMachineProfileParams{ID: m.ID, MachineProfileID: &p.ID}); err != nil {
		t.Fatalf("link fleet machine to profile: %v", err)
	}
	return p.ID
}

// seedDraftBatch inserts a batch with one job on it (batch status and the
// job's machine_family are the only fields reassignBatchesForOfflineMachine
// reads) - a minimal fixture, bypassing the order/design pipeline entirely
// since this is only exercising the scheduler, not job creation.
func seedDraftBatch(t *testing.T, store *db.Store, batchNumber, status string, machineID *uuid.UUID, jobFamily string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	b, err := store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: batchNumber, MachineID: machineID,
		Status: status, MaterialShortage: false,
	})
	if err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	if _, err := store.Q.InsertProductionJob(ctx, gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: batchNumber + "-J1", BatchID: &b.ID, Description: "Test job",
		Quantity: 1, Status: production.StatusQueued, AssemblyStatus: production.AssemblyPending,
		QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
		PersonalisationStatus: production.PersonalisationNotRequired, Colours: []byte("[]"),
		MachineFamily: &jobFamily,
	}); err != nil {
		t.Fatalf("insert production job: %v", err)
	}
	return b.ID
}

func seedMachine(t *testing.T, store *db.Store, name string) uuid.UUID {
	t.Helper()
	m, err := store.Q.InsertMachine(context.Background(), gen.InsertMachineParams{
		ID: uuid.New(), Name: name, MachineHourCost: 0, IsActive: true,
	})
	if err != nil {
		t.Fatalf("insert machine: %v", err)
	}
	return m.ID
}

type batchView struct {
	ID              string  `json:"id"`
	Status          string  `json:"status"`
	MachineID       *string `json:"machine_id"`
	PackingStrategy *string `json:"packing_strategy"`
}

func fromOrderJobs(t *testing.T, router http.Handler, minter *tokenMinter, orderID uuid.UUID) []jobView {
	t.Helper()
	create := minter.mint(t, []string{"production:create", "production:read"})
	rr := doJSON(router, http.MethodPost, "/production-jobs/from-order/"+orderID.String(), create, nil)
	if rr.Code != http.StatusCreated {
		t.Fatalf("from-order = %d body=%s", rr.Code, rr.Body.String())
	}
	var jobs []jobView
	_ = json.Unmarshal(rr.Body.Bytes(), &jobs)
	return jobs
}

func TestIntegrationFilamentAndDecrement(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	// Authorization: managing filament needs filament:manage.
	if rr := doJSON(router, http.MethodPost, "/filament-inventory", minter.mint(t, []string{"filament:read"}),
		map[string]any{"material": "PLA", "grams_available": 1000}); rr.Code != http.StatusForbidden {
		t.Errorf("filament POST without manage = %d, want 403", rr.Code)
	}

	manage := minter.mint(t, []string{"filament:manage", "filament:read"})
	if rr := doJSON(router, http.MethodPost, "/filament-inventory", manage,
		map[string]any{"material": "PLA", "grams_available": 1000, "reorder_level_grams": 100}); rr.Code != http.StatusCreated {
		t.Fatalf("filament POST = %d body=%s", rr.Code, rr.Body.String())
	}

	// Fail a PLA job wasting 150 g; stock should drop to 850.
	orderID := seedOrder(t, store, 7001, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA"},
	})
	jobs := fromOrderJobs(t, router, minter, orderID)
	jobID := jobs[0].ID
	admin := minter.mint(t, []string{"production:update"})
	doJSON(router, http.MethodPatch, "/production-jobs/"+jobID, admin, map[string]any{"status": "in_production"})
	failTok := minter.mint(t, []string{"production:fail"})
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/fail", failTok,
		map[string]any{"reason": "warping", "filament_wasted_grams": 150}); rr.Code != http.StatusOK {
		t.Fatalf("fail = %d body=%s", rr.Code, rr.Body.String())
	}

	rr := doJSON(router, http.MethodGet, "/filament-inventory", manage, nil)
	var rows []filamentResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &rows)
	if len(rows) != 1 || rows[0].GramsAvailable != 850 {
		t.Errorf("filament after fail = %+v, want one row at 850 g", rows)
	}
}

func TestIntegrationMachineOps(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	machineID := seedMachine(t, store, "Bambu-01").String()
	read := minter.mint(t, []string{"machine:read"})

	// Nothing online yet -> suggested is null.
	rr := doJSON(router, http.MethodGet, "/machines/suggested", read, nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "null" {
		t.Errorf("suggested (none online) = %d %q, want 200 null", rr.Code, rr.Body.String())
	}

	// Bring it online (needs machine:manage).
	if rr := doJSON(router, http.MethodPatch, "/machines/"+machineID, read, map[string]any{"status": "online"}); rr.Code != http.StatusForbidden {
		t.Errorf("status change without manage = %d, want 403", rr.Code)
	}
	manage := minter.mint(t, []string{"machine:read", "machine:manage"})
	if rr := doJSON(router, http.MethodPatch, "/machines/"+machineID, manage, map[string]any{"status": "online"}); rr.Code != http.StatusOK {
		t.Fatalf("status change = %d body=%s", rr.Code, rr.Body.String())
	}
	rr = doJSON(router, http.MethodGet, "/machines/suggested", read, nil)
	var suggested machineOpsResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &suggested)
	if suggested.ID != machineID {
		t.Errorf("suggested = %q, want %q", suggested.ID, machineID)
	}
}

// TestIntegrationBatchAutoCreateSplitsLargeQuantityOrder confirms the fix for
// the confirmed bug where a job's full quantity not fitting on one bed
// wrongly reported it unbatchable instead of splitting it across batches.
// Priority 1 (urgent) forces every fragment's batch to be created regardless
// of its individual bed utilisation, so the DB-commit split path (not just
// the pure planner logic already covered in internal/production) is
// actually exercised end to end.
func TestIntegrationBatchAutoCreateSplitsLargeQuantityOrder(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	orderID := seedOrder(t, store, 8101, []map[string]any{
		{"product_id": "SKU1", "product_name": "Keychain", "quantity": 20, "material": "PLA", "priority": 1},
	})
	jobs := fromOrderJobs(t, router, minter, orderID)
	if len(jobs) != 1 {
		t.Fatalf("from-order jobs = %d, want 1", len(jobs))
	}
	originalID := uuid.MustParse(jobs[0].ID)

	// 100x100mm on the 330x320 bed only fits a handful per bed, so quantity
	// 20 must split across multiple batches instead of being unbatchable.
	fileID := seedFileAsset(t, store, 100, 100, 20)
	givePrintFile(t, store, originalID, fileID)

	manage := minter.mint(t, []string{"batch:manage", "batch:read"})
	rr := doJSON(router, http.MethodPost, "/batches/auto-create", manage, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("auto-create = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp autoCreateResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Unbatchable) != 0 {
		t.Fatalf("unbatchable = %+v, want none (should split instead)", resp.Unbatchable)
	}
	if len(resp.Created) < 2 {
		t.Fatalf("created batches = %d (held=%d unbatchable=%d), want >1 (quantity 20 shouldn't fit on one bed)",
			len(resp.Created), len(resp.Held), len(resp.Unbatchable))
	}

	// The split group (the original row plus every split_of_job_id
	// fragment) must sum back to the original 20 units, and every row with
	// quantity left must be linked to a batch - nothing lost or orphaned.
	rows, err := store.Pool.Query(context.Background(),
		`SELECT quantity, batch_id FROM production_jobs WHERE id = $1 OR split_of_job_id = $1`, originalID)
	if err != nil {
		t.Fatalf("query split group: %v", err)
	}
	defer rows.Close()
	var total int32
	rowCount := 0
	for rows.Next() {
		var qty int32
		var batchID *uuid.UUID
		if err := rows.Scan(&qty, &batchID); err != nil {
			t.Fatalf("scan: %v", err)
		}
		rowCount++
		if qty > 0 && batchID == nil {
			t.Errorf("row with quantity %d has no batch_id (lost/orphaned)", qty)
		}
		total += qty
	}
	if total != 20 {
		t.Errorf("total quantity across the split group = %d, want 20", total)
	}
	if rowCount < 2 {
		t.Errorf("split group has %d row(s), want >1 (root + at least one split fragment)", rowCount)
	}
}

func TestIntegrationBatchAutoCreate(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	orderID := seedOrder(t, store, 8001, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA"},
		{"product_id": "SKU2", "product_name": "Block", "quantity": 1, "material": "PLA"},
	})
	jobs := fromOrderJobs(t, router, minter, orderID)
	// Give both jobs a small measurable print file.
	fileID := seedFileAsset(t, store, 50, 50, 20)
	for _, j := range jobs {
		givePrintFile(t, store, uuid.MustParse(j.ID), fileID)
	}

	manage := minter.mint(t, []string{"batch:manage", "batch:read"})
	rr := doJSON(router, http.MethodPost, "/batches/auto-create", manage, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("auto-create = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp autoCreateResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Created) != 1 || len(resp.Unbatchable) != 0 {
		t.Fatalf("auto-create created=%d unbatchable=%d, want 1/0", len(resp.Created), len(resp.Unbatchable))
	}
	if resp.Created[0].Status != "pending_approval" {
		t.Errorf("batch status = %q, want pending_approval", resp.Created[0].Status)
	}

	// The batch now has both jobs.
	batchID := resp.Created[0].ID
	rr = doJSON(router, http.MethodGet, "/batches/"+batchID, manage, nil)
	var detail batchResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &detail)
	if detail.JobsCount == nil || *detail.JobsCount != 2 {
		t.Errorf("jobs_count = %v, want 2", detail.JobsCount)
	}
}

func TestIntegrationBatchReassignedWhenMachineGoesOffline(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	// threshold=1000: isolates the offline-triggers-a-replan assertion below
	// from the threshold-gated per-job triggers, same reasoning as the
	// batch-completed/reprint-created trigger tests.
	s := testServerWithBatchQueue(t, store, guards, 1000)
	router := s.Router()
	ctx := context.Background()

	beforePlanBatches := countPlanBatchesJobs(t, store)

	profileA := seedFleetMachineWithProfile(t, store, "H2C-A", "H2C", "online")
	profileB := seedFleetMachineWithProfile(t, store, "H2C-B", "H2C", "online")

	// Draft, family H2C: an alternative (profileB) exists -> reassigned to it.
	withAlternative := seedDraftBatch(t, store, "BATCH-REASSIGN-1", production.BatchPendingApproval, &profileA, "H2C")
	// Draft, an unrelated family with no eligible machine at all -> cleared to unassigned.
	noAlternative := seedDraftBatch(t, store, "BATCH-REASSIGN-2", production.BatchPendingApproval, &profileA, "H2S")
	// Already approved (open): a human commitment, left alone even though its machine goes offline.
	approved := seedDraftBatch(t, store, "BATCH-REASSIGN-3", production.BatchOpen, &profileA, "H2C")

	manage := minter.mint(t, []string{"machine:manage", "machine:read"})
	if rr := doJSON(router, http.MethodPatch, "/machines/"+profileA.String(), manage, map[string]any{"status": "offline"}); rr.Code != http.StatusOK {
		t.Fatalf("patch machine offline = %d body=%s", rr.Code, rr.Body.String())
	}

	got, err := store.Q.GetBatchByID(ctx, withAlternative)
	if err != nil {
		t.Fatalf("get withAlternative: %v", err)
	}
	if got.MachineID == nil || *got.MachineID != profileB {
		t.Errorf("withAlternative machine_id = %v, want %s", got.MachineID, profileB)
	}

	got, err = store.Q.GetBatchByID(ctx, noAlternative)
	if err != nil {
		t.Fatalf("get noAlternative: %v", err)
	}
	if got.MachineID != nil {
		t.Errorf("noAlternative machine_id = %v, want nil (no eligible machine)", *got.MachineID)
	}

	got, err = store.Q.GetBatchByID(ctx, approved)
	if err != nil {
		t.Fatalf("get approved: %v", err)
	}
	if got.MachineID == nil || *got.MachineID != profileA {
		t.Errorf("approved (open) batch machine_id = %v, want unchanged %s", got.MachineID, profileA)
	}

	if got := countPlanBatchesJobs(t, store) - beforePlanBatches; got != 1 {
		t.Errorf("new plan_batches jobs after a machine with draft batches went offline = %d, want 1", got)
	}
}

func TestIntegrationNoReplanWhenOfflineMachineHasNoDraftBatches(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	s := testServerWithBatchQueue(t, store, guards, 1000)
	router := s.Router()
	beforePlanBatches := countPlanBatchesJobs(t, store)

	profileA := seedFleetMachineWithProfile(t, store, "H2C-C", "H2C", "online")

	manage := minter.mint(t, []string{"machine:manage", "machine:read"})
	if rr := doJSON(router, http.MethodPatch, "/machines/"+profileA.String(), manage, map[string]any{"status": "offline"}); rr.Code != http.StatusOK {
		t.Fatalf("patch machine offline = %d body=%s", rr.Code, rr.Body.String())
	}

	if got := countPlanBatchesJobs(t, store) - beforePlanBatches; got != 0 {
		t.Errorf("new plan_batches jobs after a machine with zero draft batches went offline = %d, want 0", got)
	}
}

func TestIntegrationBatchIdPatchAndGuard(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	// Creating a batch needs batch:manage.
	if rr := doJSON(router, http.MethodPost, "/batches", minter.mint(t, []string{"batch:read"}), map[string]any{}); rr.Code != http.StatusForbidden {
		t.Errorf("create batch without manage = %d, want 403", rr.Code)
	}
	manage := minter.mint(t, []string{"batch:manage", "batch:read"})
	rr := doJSON(router, http.MethodPost, "/batches", manage, map[string]any{})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create batch = %d body=%s", rr.Code, rr.Body.String())
	}
	var batch batchView
	_ = json.Unmarshal(rr.Body.Bytes(), &batch)

	orderID := seedOrder(t, store, 9001, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA"},
	})
	jobs := fromOrderJobs(t, router, minter, orderID)
	jobID := jobs[0].ID
	admin := minter.mint(t, []string{"production:update"})

	// Assign to the real batch.
	if rr := doJSON(router, http.MethodPatch, "/production-jobs/"+jobID, admin, map[string]any{"batch_id": batch.ID}); rr.Code != http.StatusOK {
		t.Fatalf("assign batch = %d body=%s", rr.Code, rr.Body.String())
	}
	// Assigning to a non-existent batch is rejected.
	if rr := doJSON(router, http.MethodPatch, "/production-jobs/"+jobID, admin, map[string]any{"batch_id": uuid.New().String()}); rr.Code != http.StatusNotFound {
		t.Errorf("assign missing batch = %d, want 404", rr.Code)
	}
}

// seedJobOnBatch inserts a production job on the given batch with the given
// status - the minimal shape CompleteProductionJobsForBatch cares about.
func seedJobOnBatch(t *testing.T, store *db.Store, batchID uuid.UUID, jobNumber, status string) uuid.UUID {
	t.Helper()
	j, err := store.Q.InsertProductionJob(context.Background(), gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: jobNumber, BatchID: &batchID, Description: "Test job",
		Quantity: 1, Status: status, AssemblyStatus: production.AssemblyPending,
		QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
		PersonalisationStatus: production.PersonalisationNotRequired, Colours: []byte("[]"),
	})
	if err != nil {
		t.Fatalf("insert production job: %v", err)
	}
	return j.ID
}

// jobConfig is the subset of a production job's fields the batch job-
// membership tests below actually vary.
type jobConfig struct {
	batchID       *uuid.UUID
	material      string
	leftNozzleMm  float64
	machineFamily string
	printFileID   *uuid.UUID
	held          bool
}

// seedConfiguredJob inserts a job with real material/nozzle/machine-family
// values (unlike seedJobOnBatch/seedDraftBatch, which leave those unset) -
// queued, personalisation not_required, no issue, matching
// ListUnassignedCompatibleJobs's eligibility bar exactly.
func seedConfiguredJob(t *testing.T, store *db.Store, jobNumber string, cfg jobConfig) uuid.UUID {
	t.Helper()
	j, err := store.Q.InsertProductionJob(context.Background(), gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: jobNumber, BatchID: cfg.batchID, Description: "Test job",
		Quantity: 1, Status: production.StatusQueued, AssemblyStatus: production.AssemblyPending,
		QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
		PersonalisationStatus: production.PersonalisationNotRequired, Colours: []byte("[]"),
		Material: &cfg.material, LeftNozzleMm: &cfg.leftNozzleMm, MachineFamily: &cfg.machineFamily,
		PrintFileID: cfg.printFileID, Held: cfg.held,
	})
	if err != nil {
		t.Fatalf("insert configured job: %v", err)
	}
	return j.ID
}

// TestIntegrationHeldJobsNeverReachBatching covers the four queries that gate
// the batchable pool.
//
// A hold is an operator saying "not this one, not yet". It used to be enforced
// only at ApproveBatchFor - by which point the held job had already been planned
// onto a bed, so its hold blocked approval of every unrelated job planned beside
// it, and the planner rebuilt that same doomed bed every cycle.
//
// The two jobs here are identical apart from the hold, which is the whole point:
// the held one must vanish from the pool while its twin batches normally. A
// regression that dropped the predicate from only one of the four queries would
// still show up, because each is asserted separately.
func TestIntegrationHeldJobsNeverReachBatching(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	cfg := jobConfig{material: "PLA Basics", leftNozzleMm: 0.4, machineFamily: "H2C"}
	freeID := seedConfiguredJob(t, store, "JOB-FREE", cfg)

	heldCfg := cfg
	heldCfg.held = true
	heldID := seedConfiguredJob(t, store, "JOB-HELD", heldCfg)

	contains := func(ids []uuid.UUID, want uuid.UUID) bool {
		for _, id := range ids {
			if id == want {
				return true
			}
		}
		return false
	}

	t.Run("ListBatchableJobs", func(t *testing.T) {
		rows, err := store.Q.ListBatchableJobs(ctx)
		if err != nil {
			t.Fatalf("ListBatchableJobs: %v", err)
		}
		ids := make([]uuid.UUID, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		if contains(ids, heldID) {
			t.Error("a held job is in the batchable pool; it will be planned onto a bed and then block that bed's approval")
		}
		if !contains(ids, freeID) {
			t.Error("the unheld job is missing from the pool; the hold predicate must not exclude anything else")
		}
	})

	t.Run("ListReplannableJobs", func(t *testing.T) {
		rows, err := store.Q.ListReplannableJobs(ctx)
		if err != nil {
			t.Fatalf("ListReplannableJobs: %v", err)
		}
		ids := make([]uuid.UUID, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		if contains(ids, heldID) {
			t.Error("a held job is replannable; this is the query AutoCreateBatches actually plans from")
		}
		if !contains(ids, freeID) {
			t.Error("the unheld job is not replannable")
		}
	})

	t.Run("CountBatchableJobs matches ListBatchableJobs", func(t *testing.T) {
		rows, err := store.Q.ListBatchableJobs(ctx)
		if err != nil {
			t.Fatalf("ListBatchableJobs: %v", err)
		}
		count, err := store.Q.CountBatchableJobs(ctx)
		if err != nil {
			t.Fatalf("CountBatchableJobs: %v", err)
		}
		// These two are kept deliberately in sync - the count drives the
		// replan trigger threshold, so a count that disagrees with the list
		// either fires pointless replans or suppresses needed ones.
		if int(count) != len(rows) {
			t.Errorf("CountBatchableJobs = %d, ListBatchableJobs returned %d rows; the two WHERE clauses have drifted",
				count, len(rows))
		}
	})

	t.Run("ListUnassignedCompatibleJobs", func(t *testing.T) {
		material, nozzle, family := "PLA Basics", 0.4, "H2C"
		rows, err := store.Q.ListUnassignedCompatibleJobs(ctx, gen.ListUnassignedCompatibleJobsParams{
			Material: &material, LeftNozzleMm: &nozzle, MachineFamily: &family,
		})
		if err != nil {
			t.Fatalf("ListUnassignedCompatibleJobs: %v", err)
		}
		for _, r := range rows {
			if r.ID == heldID {
				t.Error("a held job is offered for manual addition to a Draft; that reintroduces the approval block by another route")
			}
		}
	})
}

func TestIntegrationBatchCompatibleJobsAndAdd(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)
	ctx := context.Background()

	fileID := seedFileAsset(t, store, 50, 50, 20)
	b, err := store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: "BATCH-ADD-1", Status: production.BatchPendingApproval, MaterialShortage: false,
	})
	if err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	bID := &b.ID
	seedConfiguredJob(t, store, "BATCH-ADD-1-J1", jobConfig{
		batchID: bID, material: "PLA", leftNozzleMm: 0.4, machineFamily: "H2C", printFileID: &fileID,
	})
	compatible := seedConfiguredJob(t, store, "BATCH-ADD-1-J2", jobConfig{
		material: "PLA", leftNozzleMm: 0.4, machineFamily: "H2C", printFileID: &fileID,
	})
	incompatible := seedConfiguredJob(t, store, "BATCH-ADD-1-J3", jobConfig{
		material: "PETG", leftNozzleMm: 0.4, machineFamily: "H2C", printFileID: &fileID,
	})
	otherBatch, err := store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: "BATCH-ADD-2", Status: production.BatchPendingApproval, MaterialShortage: false,
	})
	if err != nil {
		t.Fatalf("insert other batch: %v", err)
	}
	alreadyAssigned := seedConfiguredJob(t, store, "BATCH-ADD-1-J4", jobConfig{
		batchID: &otherBatch.ID, material: "PLA", leftNozzleMm: 0.4, machineFamily: "H2C", printFileID: &fileID,
	})

	manage := minter.mint(t, []string{"batch:manage", "batch:read"})

	// Only the compatible, unassigned job is offered.
	rr := doJSON(router, http.MethodGet, "/batches/"+b.ID.String()+"/compatible-jobs", manage, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("compatible-jobs = %d body=%s", rr.Code, rr.Body.String())
	}
	var jobs []jobView
	_ = json.Unmarshal(rr.Body.Bytes(), &jobs)
	if len(jobs) != 1 || jobs[0].ID != compatible.String() {
		t.Fatalf("compatible-jobs = %+v, want only %s", jobs, compatible)
	}

	// Rejects an incompatible job.
	if rr := doJSON(router, http.MethodPost, "/batches/"+b.ID.String()+"/jobs", manage,
		map[string]any{"job_ids": []string{incompatible.String()}}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("add incompatible = %d, want 422", rr.Code)
	}
	// Rejects a job already assigned elsewhere.
	if rr := doJSON(router, http.MethodPost, "/batches/"+b.ID.String()+"/jobs", manage,
		map[string]any{"job_ids": []string{alreadyAssigned.String()}}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("add already-assigned = %d, want 422", rr.Code)
	}

	// Accepts the compatible job: membership is committed before the plate
	// re-merge, which needs object storage - unconfigured in this test
	// environment (see testServer/NewServer, storage is always nil here, the
	// same known gap approveBatch/previewBatch already have). So the request
	// itself 503s on the re-merge step, but the membership change it already
	// made is real and durable - assert that directly against the DB rather
	// than asserting on the (unreachable-here) 200 response body.
	rr = doJSON(router, http.MethodPost, "/batches/"+b.ID.String()+"/jobs", manage,
		map[string]any{"job_ids": []string{compatible.String()}})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("add compatible = %d body=%s, want 503 (no storage configured in tests)", rr.Code, rr.Body.String())
	}
	job, err := store.Q.GetProductionJobByID(ctx, compatible)
	if err != nil || job.BatchID == nil || *job.BatchID != b.ID {
		t.Errorf("job batch_id after add = %v, want %s", job.BatchID, b.ID)
	}
}

func TestIntegrationBatchRemoveJob(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)
	ctx := context.Background()

	fileID := seedFileAsset(t, store, 50, 50, 20)
	b, err := store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: "BATCH-REMOVE-1", Status: production.BatchPendingApproval, MaterialShortage: false,
	})
	if err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	// A single job, so removing it empties the batch entirely - the one
	// recomputeBatchPlate path that needs no object storage (there's nothing
	// left to merge), so it's fully exercisable in this test environment
	// (see TestIntegrationBatchCompatibleJobsAndAdd for why the non-empty,
	// storage-touching re-merge path can't be asserted on here).
	toRemove := seedConfiguredJob(t, store, "BATCH-REMOVE-1-J1", jobConfig{
		batchID: &b.ID, material: "PLA", leftNozzleMm: 0.4, machineFamily: "H2C", printFileID: &fileID,
	})

	manage := minter.mint(t, []string{"batch:manage", "batch:read"})
	rr := doJSON(router, http.MethodDelete, "/batches/"+b.ID.String()+"/jobs/"+toRemove.String(), manage, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("remove job = %d body=%s", rr.Code, rr.Body.String())
	}
	var updated batchResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &updated)
	if updated.UnitsPerBed != nil {
		t.Errorf("units_per_bed after emptying batch = %v, want nil", updated.UnitsPerBed)
	}
	if updated.PreviewFileID != nil {
		t.Errorf("preview_file_id after emptying batch = %v, want nil", updated.PreviewFileID)
	}
	job, err := store.Q.GetProductionJobByID(ctx, toRemove)
	if err != nil || job.BatchID != nil {
		t.Errorf("job batch_id after remove = %v, want nil", job.BatchID)
	}

	// Removing a job that isn't on this batch 404s.
	if rr := doJSON(router, http.MethodDelete, "/batches/"+b.ID.String()+"/jobs/"+toRemove.String(), manage, nil); rr.Code != http.StatusNotFound {
		t.Errorf("remove already-removed job = %d, want 404", rr.Code)
	}
}

func TestIntegrationBatchJobEditingGuards(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)
	ctx := context.Background()

	fileID := seedFileAsset(t, store, 50, 50, 20)
	manage := minter.mint(t, []string{"batch:manage", "batch:read"})

	// A non-Draft batch rejects both add and remove.
	approved, err := store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: "BATCH-GUARD-1", Status: production.BatchOpen, MaterialShortage: false,
	})
	if err != nil {
		t.Fatalf("insert approved batch: %v", err)
	}
	onApproved := seedConfiguredJob(t, store, "BATCH-GUARD-1-J1", jobConfig{
		batchID: &approved.ID, material: "PLA", leftNozzleMm: 0.4, machineFamily: "H2C", printFileID: &fileID,
	})
	unassigned := seedConfiguredJob(t, store, "BATCH-GUARD-1-J2", jobConfig{
		material: "PLA", leftNozzleMm: 0.4, machineFamily: "H2C", printFileID: &fileID,
	})
	if rr := doJSON(router, http.MethodPost, "/batches/"+approved.ID.String()+"/jobs", manage,
		map[string]any{"job_ids": []string{unassigned.String()}}); rr.Code != http.StatusConflict {
		t.Errorf("add to non-draft batch = %d, want 409", rr.Code)
	}
	if rr := doJSON(router, http.MethodDelete, "/batches/"+approved.ID.String()+"/jobs/"+onApproved.String(), manage, nil); rr.Code != http.StatusConflict {
		t.Errorf("remove from non-draft batch = %d, want 409", rr.Code)
	}

	// A Draft batch already at/above the 80% target rejects further adds.
	full, err := store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: "BATCH-GUARD-2", Status: production.BatchPendingApproval, MaterialShortage: false,
	})
	if err != nil {
		t.Fatalf("insert full batch: %v", err)
	}
	seedConfiguredJob(t, store, "BATCH-GUARD-2-J1", jobConfig{
		batchID: &full.ID, material: "PLA", leftNozzleMm: 0.4, machineFamily: "H2C", printFileID: &fileID,
	})
	fullPercent := 85.0
	if _, err := store.Q.UpdateBatchDerivedMetrics(ctx, gen.UpdateBatchDerivedMetricsParams{
		ID: full.ID, BedUtilizationPercent: &fullPercent,
	}); err != nil {
		t.Fatalf("set full batch utilisation: %v", err)
	}
	anotherUnassigned := seedConfiguredJob(t, store, "BATCH-GUARD-2-J2", jobConfig{
		material: "PLA", leftNozzleMm: 0.4, machineFamily: "H2C", printFileID: &fileID,
	})
	if rr := doJSON(router, http.MethodPost, "/batches/"+full.ID.String()+"/jobs", manage,
		map[string]any{"job_ids": []string{anotherUnassigned.String()}}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("add to full batch = %d, want 422", rr.Code)
	}
}

func TestIntegrationBatchCompletedAutoCompletesItsJobs(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)
	ctx := context.Background()

	b, err := store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: "BATCH-COMPLETE-1", Status: production.BatchOpen, MaterialShortage: false,
	})
	if err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	queuedID := seedJobOnBatch(t, store, b.ID, "BATCH-COMPLETE-1-J1", production.StatusQueued)
	inProductionID := seedJobOnBatch(t, store, b.ID, "BATCH-COMPLETE-1-J2", production.StatusInProduction)
	failedID := seedJobOnBatch(t, store, b.ID, "BATCH-COMPLETE-1-J3", production.StatusFailed)

	manage := minter.mint(t, []string{"batch:manage", "batch:read"})
	if rr := doJSON(router, http.MethodPatch, "/batches/"+b.ID.String(), manage, map[string]any{"status": "completed"}); rr.Code != http.StatusOK {
		t.Fatalf("patch batch completed = %d body=%s", rr.Code, rr.Body.String())
	}

	for _, tc := range []struct {
		name string
		id   uuid.UUID
		want string
	}{
		{"queued -> completed", queuedID, production.StatusCompleted},
		{"in_production -> completed", inProductionID, production.StatusCompleted},
		{"failed stays failed", failedID, production.StatusFailed},
	} {
		job, err := store.Q.GetProductionJobByID(ctx, tc.id)
		if err != nil {
			t.Fatalf("%s: get job: %v", tc.name, err)
		}
		if job.Status != tc.want {
			t.Errorf("%s: status = %q, want %q", tc.name, job.Status, tc.want)
		}
	}
}

// TestIntegrationApprovalKeepsMeasuredPlateTime locks the ordering rule between
// the plate-slice worker and approval.
//
// The plate slice (SliceBatchArgs) replaces batches.total_print_time_minutes -
// which is otherwise batchTimeFromJobs' fast estimate, the summed per-unit
// times of the bed scaled by the shared-overhead correction - with a
// measurement of the actual merged bed. Approval then recomputes the batch's
// derived metrics, and recomputing the time unconditionally would overwrite
// that measurement, so every batch would reach the machine scheduler on an
// estimate and the plate slice would have been for nothing.
//
// Still meaningful now that slicing happens AT approval rather than on the
// Draft: a batch re-approved after a slice already landed, or one measured by
// any other path, must keep the measurement rather than being reset to the
// estimate.
//
// The batch's job here has no per-job estimate, so batchTimeFromJobs yields
// nil. That makes the failure unambiguous: nil means approval clobbered the
// measurement, 999 means it kept it.
func TestIntegrationApprovalKeepsMeasuredPlateTime(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	machineID := seedMachine(t, store, "H2C-01")
	batchID := seedDraftBatch(t, store, "BATCH-PLATE-SLICED", production.BatchPendingApproval, &machineID, "H2C")

	// A cached preview file stands in for the merged plate, so mergedPlateFor
	// reuses it instead of trying to merge real STL bytes from object storage
	// (which this fixture has none of).
	fileID := seedFileAsset(t, store, 50, 50, 20)
	if _, err := store.Q.SetBatchPreviewFile(ctx, gen.SetBatchPreviewFileParams{
		ID: batchID, PreviewFileID: &fileID,
	}); err != nil {
		t.Fatalf("set preview file: %v", err)
	}

	// Stand in for the plate-slice worker having measured this bed.
	measured := int32(999)
	if _, err := store.Q.SetBatchPlateSliceResult(ctx, gen.SetBatchPlateSliceResultParams{
		ID: batchID, TotalPrintTimeMinutes: &measured,
	}); err != nil {
		t.Fatalf("record plate slice result: %v", err)
	}

	cfg := config.Settings{Environment: "development", AuthAudience: "tensor-core"}
	srv := NewServer(cfg, store, auth.NewGuards(newTokenMinter(t).verifier, ""), nil)

	approved, err := srv.ApproveBatchFor(ctx, batchID, &machineID, "usr_admin")
	if err != nil {
		t.Fatalf("ApproveBatchFor: %v", err)
	}
	if approved.TotalPrintTimeMinutes == nil {
		t.Fatalf("total_print_time_minutes after approval = nil, want %d: approval overwrote the measured plate time with batchTimeFromJobs' approximation", measured)
	}
	if *approved.TotalPrintTimeMinutes != measured {
		t.Errorf("total_print_time_minutes after approval = %d, want %d (the measured plate time)",
			*approved.TotalPrintTimeMinutes, measured)
	}
	if !approved.PlateSlicedAt.Valid {
		t.Error("plate_sliced_at was cleared by approval; it marks the time as measured rather than estimated")
	}
}

// TestIntegrationReplanGrowsDraftAndSparesLocked is the core of the batching
// loop: a Draft is a proposal that keeps improving, and a Locked batch is a
// commitment that never moves.
//
// Before this, ListBatchableJobs filtered `batch_id IS NULL`, so the moment a
// job landed in a Draft it was invisible to every later planner run - a bed
// that formed at 5% utilisation stayed at 5% forever no matter how much
// compatible work arrived. The assertion that the Draft ends up holding BOTH
// jobs is the whole feature.
//
// The second half is the guard rail: the same run must not disturb an approved
// batch. If dissolve-and-reform ever reaches past pending_approval, a plate
// whose filament is already reserved and which a machine is about to print
// would silently change contents.
func TestIntegrationReplanGrowsDraftAndSparesLocked(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)
	ctx := context.Background()
	manage := minter.mint(t, []string{"batch:manage", "batch:read"})

	// A locked batch that must survive untouched, seeded directly so it is
	// already approved before the planner ever runs.
	machineID := seedMachine(t, store, "H2C-01")
	lockedID := seedDraftBatch(t, store, "BATCH-LOCKED-KEEP", production.BatchOpen, &machineID, "H2C")
	lockedBefore, err := store.Q.GetBatchByID(ctx, lockedID)
	if err != nil {
		t.Fatalf("load locked batch: %v", err)
	}
	lockedJobsBefore, err := store.Q.ListJobsForBatch(ctx, &lockedID)
	if err != nil {
		t.Fatalf("load locked batch jobs: %v", err)
	}

	// First job in, first plan: one thin Draft.
	fileID := seedFileAsset(t, store, 50, 50, 20)
	first := seedOrder(t, store, 9101, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA"},
	})
	for _, j := range fromOrderJobs(t, router, minter, first) {
		givePrintFile(t, store, uuid.MustParse(j.ID), fileID)
	}
	if rr := doJSON(router, http.MethodPost, "/batches/auto-create", manage, nil); rr.Code != http.StatusOK {
		t.Fatalf("first auto-create = %d body=%s", rr.Code, rr.Body.String())
	}
	draftsAfterFirst := draftBatches(t, store)
	if len(draftsAfterFirst) != 1 {
		t.Fatalf("drafts after first plan = %d, want 1", len(draftsAfterFirst))
	}

	// A compatible job arrives, and the planner runs again.
	second := seedOrder(t, store, 9102, []map[string]any{
		{"product_id": "SKU2", "product_name": "Block", "quantity": 1, "material": "PLA"},
	})
	for _, j := range fromOrderJobs(t, router, minter, second) {
		givePrintFile(t, store, uuid.MustParse(j.ID), fileID)
	}
	if rr := doJSON(router, http.MethodPost, "/batches/auto-create", manage, nil); rr.Code != http.StatusOK {
		t.Fatalf("second auto-create = %d body=%s", rr.Code, rr.Body.String())
	}

	// The two compatible jobs must now share one bed, not sit on two.
	drafts := draftBatches(t, store)
	if len(drafts) != 1 {
		t.Fatalf("drafts after re-plan = %d, want 1 - the new job should have joined the existing bed, not started its own", len(drafts))
	}
	grown, err := store.Q.ListJobsForBatch(ctx, &drafts[0])
	if err != nil {
		t.Fatalf("load draft jobs: %v", err)
	}
	if len(grown) != 2 {
		t.Errorf("draft holds %d jobs, want 2 - the draft did not absorb the newly-arrived compatible job", len(grown))
	}

	// The locked batch is byte-identical: same row, same membership.
	lockedAfter, err := store.Q.GetBatchByID(ctx, lockedID)
	if err != nil {
		t.Fatalf("locked batch disappeared during re-plan: %v", err)
	}
	if lockedAfter.BatchNumber != lockedBefore.BatchNumber || lockedAfter.Status != lockedBefore.Status {
		t.Errorf("locked batch changed: %q/%q -> %q/%q",
			lockedBefore.BatchNumber, lockedBefore.Status, lockedAfter.BatchNumber, lockedAfter.Status)
	}
	lockedJobsAfter, err := store.Q.ListJobsForBatch(ctx, &lockedID)
	if err != nil {
		t.Fatalf("load locked batch jobs after: %v", err)
	}
	if len(lockedJobsAfter) != len(lockedJobsBefore) {
		t.Errorf("locked batch membership changed: %d jobs -> %d", len(lockedJobsBefore), len(lockedJobsAfter))
	}
}

// draftBatches returns every pending_approval batch id.
func draftBatches(t *testing.T, store *db.Store) []uuid.UUID {
	t.Helper()
	ids, err := store.Q.ListDraftBatchIDs(context.Background())
	if err != nil {
		t.Fatalf("list draft batches: %v", err)
	}
	return ids
}
