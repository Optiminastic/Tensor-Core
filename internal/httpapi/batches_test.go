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
	"github.com/Optiminastic/tensor-core/internal/production"
)

// seedFileAsset inserts a file_assets row with the given bounding box and returns
// its id (no object-storage bytes; only the metadata the planner reads).
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
	if _, err := store.Q.SetProductionJobPrintFile(context.Background(), gen.SetProductionJobPrintFileParams{
		ID: originalID, PrintFileID: &fileID,
	}); err != nil {
		t.Fatalf("set print file: %v", err)
	}

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
		t.Fatalf("created batches = %d, want >1 (quantity 20 shouldn't fit on one bed)", len(resp.Created))
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
		id := uuid.MustParse(j.ID)
		if _, err := store.Q.SetProductionJobPrintFile(context.Background(), gen.SetProductionJobPrintFileParams{
			ID: id, PrintFileID: &fileID,
		}); err != nil {
			t.Fatalf("set print file: %v", err)
		}
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
