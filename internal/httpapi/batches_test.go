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
