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
