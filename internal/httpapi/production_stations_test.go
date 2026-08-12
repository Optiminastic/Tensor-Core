package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
)

// completeJob drives a fresh job to completed via two PATCHes as an admin.
func completeJob(t *testing.T, router http.Handler, minter *tokenMinter, jobID string) {
	t.Helper()
	admin := minter.mint(t, []string{"production:update"})
	for _, status := range []string{"in_production", "completed"} {
		rr := doJSON(router, http.MethodPatch, "/production-jobs/"+jobID, admin, map[string]any{"status": status})
		if rr.Code != http.StatusOK {
			t.Fatalf("PATCH status=%s = %d body=%s", status, rr.Code, rr.Body.String())
		}
	}
}

func TestIntegrationStationGatingAndLifecycle(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	orderID := seedOrder(t, store, 11001, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA"},
	})
	jobID := fromOrderJobs(t, router, minter, orderID)[0].ID

	assemblyTok := minter.mint(t, []string{"assembly:submit"})
	finishTok := minter.mint(t, []string{"finishing:submit"})
	qcTok := minter.mint(t, []string{"qc:submit"})
	packTok := minter.mint(t, []string{"packaging:submit"})

	// Assembly before the job is completed -> 409.
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/assembly", assemblyTok,
		map[string]any{"parts_combined": true}); rr.Code != http.StatusConflict {
		t.Errorf("assembly before completed = %d, want 409", rr.Code)
	}

	completeJob(t, router, minter, jobID)

	// QC before assembly is resolved -> 409 (a non-personalised job still starts
	// with assembly_status = pending).
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/qc", qcTok,
		map[string]any{"decision": "pass"}); rr.Code != http.StatusConflict {
		t.Errorf("qc before assembly = %d, want 409", rr.Code)
	}

	// Assembly authorization: needs assembly:submit.
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/assembly", qcTok,
		map[string]any{"parts_combined": true}); rr.Code != http.StatusForbidden {
		t.Errorf("assembly without permission = %d, want 403", rr.Code)
	}
	// Record assembly.
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/assembly", assemblyTok,
		map[string]any{"parts_combined": true, "fit_check_ok": true}); rr.Code != http.StatusOK {
		t.Fatalf("assembly = %d body=%s", rr.Code, rr.Body.String())
	}

	// Finishing authorization: needs finishing:submit.
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/finishing", qcTok,
		map[string]any{"sanded": true}); rr.Code != http.StatusForbidden {
		t.Errorf("finishing without permission = %d, want 403", rr.Code)
	}
	// QC before finishing is resolved -> 409: assembly is done, finishing is not.
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/qc", qcTok,
		map[string]any{"decision": "pass"}); rr.Code != http.StatusConflict {
		t.Errorf("qc before finishing = %d, want 409", rr.Code)
	}
	// Record finishing.
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/finishing", finishTok,
		map[string]any{"supports_removed": true, "sanded": true, "surface_finish_ok": true}); rr.Code != http.StatusOK {
		t.Fatalf("finishing = %d body=%s", rr.Code, rr.Body.String())
	}

	// Packaging before QC passes -> 409.
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/packaging", packTok,
		map[string]any{"packaging_type": "box"}); rr.Code != http.StatusConflict {
		t.Errorf("packaging before qc = %d, want 409", rr.Code)
	}

	// QC pass.
	rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/qc", qcTok, map[string]any{"decision": "pass"})
	if rr.Code != http.StatusOK {
		t.Fatalf("qc pass = %d body=%s", rr.Code, rr.Body.String())
	}
	var qc struct {
		Job        jobView  `json:"job"`
		ReprintJob *jobView `json:"reprint_job"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &qc)
	if qc.Job.QcStatus != "passed" || qc.ReprintJob != nil {
		t.Errorf("qc pass = %+v, want passed with no reprint", qc)
	}

	// Packaging now succeeds.
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/packaging", packTok,
		map[string]any{"packaging_type": "box", "fragile": true}); rr.Code != http.StatusOK {
		t.Fatalf("packaging = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestIntegrationQcFailCreatesReprint(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	orderID := seedOrder(t, store, 12001, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA"},
	})
	jobID := fromOrderJobs(t, router, minter, orderID)[0].ID
	completeJob(t, router, minter, jobID)

	assemblyTok := minter.mint(t, []string{"assembly:submit"})
	doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/assembly/skip", assemblyTok, nil)
	finishTok := minter.mint(t, []string{"finishing:submit"})
	doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/finishing/skip", finishTok, nil)

	qcTok := minter.mint(t, []string{"qc:submit"})
	rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/qc", qcTok, map[string]any{"decision": "fail"})
	if rr.Code != http.StatusOK {
		t.Fatalf("qc fail = %d body=%s", rr.Code, rr.Body.String())
	}
	var qc struct {
		Job        jobView  `json:"job"`
		ReprintJob *jobView `json:"reprint_job"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &qc)
	if qc.Job.QcStatus != "failed" {
		t.Errorf("qc status = %q, want failed", qc.Job.QcStatus)
	}
	if qc.ReprintJob == nil || qc.ReprintJob.Status != "queued" || qc.ReprintJob.ReprintOfJobID == nil || *qc.ReprintJob.ReprintOfJobID != jobID {
		t.Errorf("reprint = %+v, want queued and linked to %s", qc.ReprintJob, jobID)
	}
}

func TestIntegrationDispatch(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	orderID := seedOrder(t, store, 13001, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA"},
	})

	// Authorization: creating a dispatch needs dispatch:manage.
	if rr := doJSON(router, http.MethodPost, "/dispatch-orders", minter.mint(t, []string{"dispatch:read"}),
		map[string]any{"order_id": orderID.String()}); rr.Code != http.StatusForbidden {
		t.Errorf("dispatch create without manage = %d, want 403", rr.Code)
	}
	// A missing order is a 404.
	manage := minter.mint(t, []string{"dispatch:manage", "dispatch:read"})
	if rr := doJSON(router, http.MethodPost, "/dispatch-orders", manage,
		map[string]any{"order_id": uuid.New().String()}); rr.Code != http.StatusNotFound {
		t.Errorf("dispatch create missing order = %d, want 404", rr.Code)
	}

	rr := doJSON(router, http.MethodPost, "/dispatch-orders", manage, map[string]any{
		"order_id": orderID.String(), "carrier": "Delhivery",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("dispatch create = %d body=%s", rr.Code, rr.Body.String())
	}
	var created dispatchResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	if created.Status != "pending" {
		t.Errorf("new dispatch status = %q, want pending", created.Status)
	}

	rr = doJSON(router, http.MethodPost, "/dispatch-orders/"+created.ID+"/dispatch", manage, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("mark dispatched = %d body=%s", rr.Code, rr.Body.String())
	}
	var done dispatchResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &done)
	if done.Status != "dispatched" || done.DispatchedAt == nil {
		t.Errorf("dispatched = %+v, want dispatched with a timestamp", done)
	}
}

// TestIntegrationStationDoubleSubmitIsIdempotent covers the "same action
// clicked twice" and "two operators on one product" cases: the prior-stage
// gate lives in the UPDATE's WHERE clause, so the second call matches no row.
// The assertion that matters is the row count - a 409 alone would not prove
// the check row wasn't written before the guard ran.
func TestIntegrationStationDoubleSubmitIsIdempotent(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	orderID := seedOrder(t, store, 13001, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA"},
	})
	jobID := fromOrderJobs(t, router, minter, orderID)[0].ID
	completeJob(t, router, minter, jobID)

	assemblyTok := minter.mint(t, []string{"assembly:submit"})
	body := map[string]any{"parts_combined": true, "fit_check_ok": true}

	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/assembly", assemblyTok, body); rr.Code != http.StatusOK {
		t.Fatalf("first assembly = %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/assembly", assemblyTok, body); rr.Code != http.StatusConflict {
		t.Errorf("second assembly = %d, want 409", rr.Code)
	}

	var checks int
	if err := store.Pool.QueryRow(t.Context(),
		`SELECT count(*) FROM production_job_assembly_checks WHERE job_id = $1`, jobID).Scan(&checks); err != nil {
		t.Fatalf("count assembly checks: %v", err)
	}
	if checks != 1 {
		t.Errorf("assembly check rows = %d, want exactly 1", checks)
	}
}

// TestIntegrationQcFailDoesNotDoubleReprint is the expensive version of the
// same race: before the guard moved into the UPDATE, two QC fails on one job
// produced two urgent reprints of the same units.
func TestIntegrationQcFailDoesNotDoubleReprint(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	orderID := seedOrder(t, store, 13002, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 3, "material": "PLA"},
	})
	jobID := fromOrderJobs(t, router, minter, orderID)[0].ID
	completeJob(t, router, minter, jobID)

	assemblyTok := minter.mint(t, []string{"assembly:submit"})
	doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/assembly/skip", assemblyTok, nil)
	finishTok := minter.mint(t, []string{"finishing:submit"})
	doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/finishing/skip", finishTok, nil)

	qcTok := minter.mint(t, []string{"qc:submit"})
	fail := map[string]any{"decision": "fail"}
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/qc", qcTok, fail); rr.Code != http.StatusOK {
		t.Fatalf("first qc fail = %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/qc", qcTok, fail); rr.Code != http.StatusConflict {
		t.Errorf("second qc fail = %d, want 409", rr.Code)
	}

	var reprints int
	if err := store.Pool.QueryRow(t.Context(),
		`SELECT count(*) FROM production_jobs WHERE reprint_of_job_id = $1`, jobID).Scan(&reprints); err != nil {
		t.Fatalf("count reprints: %v", err)
	}
	if reprints != 1 {
		t.Errorf("reprint jobs = %d, want exactly 1", reprints)
	}
}

// TestIntegrationReprintCarriesPlannerFields guards the fix to the reprint
// builder: a reprint that loses colours/machine_family/nozzles/quality/bbox
// lands in a degenerate planner bucket and cannot be placed on a bed, which
// silently defeats its urgent priority.
func TestIntegrationReprintCarriesPlannerFields(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	orderID := seedOrder(t, store, 13003, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 2, "material": "PLA"},
	})
	jobID := fromOrderJobs(t, router, minter, orderID)[0].ID
	completeJob(t, router, minter, jobID)

	src := uuid.MustParse(jobID)
	if _, err := store.Pool.Exec(t.Context(),
		`UPDATE production_jobs SET machine_family = 'H2S', quality_mm = 0.2, left_nozzle_mm = 0.4,
		 bbox_x_mm = 40, bbox_y_mm = 30, bbox_z_mm = 20 WHERE id = $1`, src); err != nil {
		t.Fatalf("seed planner fields: %v", err)
	}

	failTok := minter.mint(t, []string{"production:fail"})
	rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/fail", failTok,
		map[string]any{"reason": "warping"})
	if rr.Code != http.StatusOK {
		t.Fatalf("fail a printed job = %d body=%s (a printed job must be failable - "+
			"restricting /fail to in_production made it unreachable)", rr.Code, rr.Body.String())
	}

	var family, quality, nozzle, bbox any
	if err := store.Pool.QueryRow(t.Context(),
		`SELECT machine_family, quality_mm, left_nozzle_mm, bbox_x_mm
		 FROM production_jobs WHERE reprint_of_job_id = $1`, src).
		Scan(&family, &quality, &nozzle, &bbox); err != nil {
		t.Fatalf("load reprint: %v", err)
	}
	if family == nil || quality == nil || nozzle == nil || bbox == nil {
		t.Errorf("reprint dropped planner fields: family=%v quality=%v nozzle=%v bbox=%v",
			family, quality, nozzle, bbox)
	}
}
