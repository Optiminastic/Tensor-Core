package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwt"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// mintRoles issues a token with explicit roles AND permissions, so the role-gated
// PATCH can be exercised (the shared minter always claims ADMIN).
func mintRoles(t *testing.T, m *tokenMinter, roles, permissions []string) string {
	t.Helper()
	tok, _ := jwt.NewBuilder().
		Subject("usr_op").Issuer("http://issuer").Audience([]string{"tensor-core"}).
		Expiration(time.Now().Add(15*time.Minute)).
		Claim("email", "op@b.com").
		Claim("roles", roles).
		Claim("permissions", permissions).
		Build()
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA, m.priv))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return string(signed)
}

// seedOrder inserts an order with the given line items and returns its id.
func seedOrder(t *testing.T, store *db.Store, shopifyID int64, items []map[string]any) uuid.UUID {
	t.Helper()
	lineItems, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal line items: %v", err)
	}
	id := uuid.New()
	if _, err := store.Q.InsertOrder(context.Background(), gen.InsertOrderParams{
		ID: id, ShopifyOrderID: shopifyID, OrderNumber: "1001", FinancialStatus: "paid",
		TotalPrice: 1499, Currency: "INR", LineItems: lineItems, Status: "queued",
	}); err != nil {
		t.Fatalf("insert order: %v", err)
	}
	return id
}

type jobView struct {
	ID                    string  `json:"id"`
	Status                string  `json:"status"`
	QcStatus              string  `json:"qc_status"`
	PersonalisationStatus string  `json:"personalisation_status"`
	ReprintOfJobID        *string `json:"reprint_of_job_id"`
}

func TestIntegrationProductionAuth(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	// Anonymous -> 401.
	if rr := doJSON(router, http.MethodGet, "/production-jobs", "", nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("anon GET /production-jobs = %d, want 401", rr.Code)
	}
	// Missing production:read -> 403.
	if rr := doJSON(router, http.MethodGet, "/production-jobs", minter.mint(t, []string{"design:read"}), nil); rr.Code != http.StatusForbidden {
		t.Errorf("no-perm GET /production-jobs = %d, want 403", rr.Code)
	}
	// With production:read -> 200.
	if rr := doJSON(router, http.MethodGet, "/production-jobs", minter.mint(t, []string{"production:read"}), nil); rr.Code != http.StatusOK {
		t.Errorf("production:read GET /production-jobs = %d, want 200", rr.Code)
	}
	// Orders are gated on order:read.
	if rr := doJSON(router, http.MethodGet, "/orders", minter.mint(t, []string{"production:read"}), nil); rr.Code != http.StatusForbidden {
		t.Errorf("no order:read GET /orders = %d, want 403", rr.Code)
	}
	if rr := doJSON(router, http.MethodGet, "/orders", minter.mint(t, []string{"order:read"}), nil); rr.Code != http.StatusOK {
		t.Errorf("order:read GET /orders = %d, want 200", rr.Code)
	}
}

func TestIntegrationFromOrderAndLifecycle(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	orderID := seedOrder(t, store, 5001, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 2, "material": "PLA"},
		{
			"product_id": "SKU2", "product_name": "Nameplate", "quantity": 1,
			"personalisation_required": true, "personalisation_name": "Ada",
			"personalisation_font": "Serif", "personalisation_colour": "Gold",
			"personalisation_variant": "L",
		},
	})

	create := minter.mint(t, []string{"production:create", "production:read"})
	rr := doJSON(router, http.MethodPost, "/production-jobs/from-order/"+orderID.String(), create, nil)
	if rr.Code != http.StatusCreated {
		t.Fatalf("from-order = %d body=%s", rr.Code, rr.Body.String())
	}
	var jobs []jobView
	_ = json.Unmarshal(rr.Body.Bytes(), &jobs)
	if len(jobs) != 2 {
		t.Fatalf("from-order created %d jobs, want 2", len(jobs))
	}
	// Auto-validation: the plain line is not_required, the fully-specified line is validated.
	byPers := map[string]int{}
	for _, j := range jobs {
		byPers[j.PersonalisationStatus]++
	}
	if byPers["not_required"] != 1 || byPers["validated"] != 1 {
		t.Errorf("personalisation statuses = %v, want one not_required and one validated", byPers)
	}

	// Re-running from-order is a conflict.
	if rr := doJSON(router, http.MethodPost, "/production-jobs/from-order/"+orderID.String(), create, nil); rr.Code != http.StatusConflict {
		t.Errorf("second from-order = %d, want 409", rr.Code)
	}

	jobID := jobs[0].ID
	admin := minter.mint(t, []string{"production:update"}) // roles=[ADMIN] from the shared minter

	// Direct PATCH to failed is rejected.
	if rr := doJSON(router, http.MethodPatch, "/production-jobs/"+jobID, admin, map[string]any{"status": "failed"}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("PATCH status=failed = %d, want 422", rr.Code)
	}
	// Assigning to a non-existent batch is a 404 (batch assignment is live in Phase 2).
	if rr := doJSON(router, http.MethodPatch, "/production-jobs/"+jobID, admin, map[string]any{"batch_id": uuid.New().String()}); rr.Code != http.StatusNotFound {
		t.Errorf("PATCH batch_id (missing) = %d, want 404", rr.Code)
	}
	// Move to in_production.
	if rr := doJSON(router, http.MethodPatch, "/production-jobs/"+jobID, admin, map[string]any{"status": "in_production"}); rr.Code != http.StatusOK {
		t.Fatalf("PATCH status=in_production = %d body=%s", rr.Code, rr.Body.String())
	}

	// Fail it: a reprint job is created, the original is failed.
	failTok := minter.mint(t, []string{"production:fail"})
	rr = doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/fail", failTok, map[string]any{
		"reason": "warping", "filament_wasted_grams": 12.5,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("fail = %d body=%s", rr.Code, rr.Body.String())
	}
	var failResp struct {
		FailedJob  jobView `json:"failed_job"`
		ReprintJob jobView `json:"reprint_job"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &failResp)
	if failResp.FailedJob.Status != "failed" {
		t.Errorf("failed job status = %q, want failed", failResp.FailedJob.Status)
	}
	if failResp.ReprintJob.Status != "queued" || failResp.ReprintJob.ReprintOfJobID == nil || *failResp.ReprintJob.ReprintOfJobID != jobID {
		t.Errorf("reprint job = %+v, want queued and linked to %s", failResp.ReprintJob, jobID)
	}

	// Failing a job that is not in production is a conflict.
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+failResp.ReprintJob.ID+"/fail", failTok, map[string]any{"reason": "other"}); rr.Code != http.StatusConflict {
		t.Errorf("fail queued job = %d, want 409", rr.Code)
	}
}

func TestIntegrationPatchFieldGate(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	orderID := seedOrder(t, store, 6001, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA"},
	})
	create := minter.mint(t, []string{"production:create", "production:read"})
	rr := doJSON(router, http.MethodPost, "/production-jobs/from-order/"+orderID.String(), create, nil)
	var jobs []jobView
	_ = json.Unmarshal(rr.Body.Bytes(), &jobs)
	if len(jobs) != 1 {
		t.Fatalf("expected one job, got %d", len(jobs))
	}
	jobID := jobs[0].ID

	// An operator may move the status but not touch qc_status.
	operator := mintRoles(t, minter, []string{"OPERATOR"}, []string{"production:update"})
	if rr := doJSON(router, http.MethodPatch, "/production-jobs/"+jobID, operator, map[string]any{"qc_status": "passed"}); rr.Code != http.StatusForbidden {
		t.Errorf("operator PATCH qc_status = %d, want 403", rr.Code)
	}
	if rr := doJSON(router, http.MethodPatch, "/production-jobs/"+jobID, operator, map[string]any{"status": "in_production"}); rr.Code != http.StatusOK {
		t.Errorf("operator PATCH status = %d, want 200", rr.Code)
	}

	// The QC/packaging role has production:update by mint but no PATCH fields at all.
	qcRole := mintRoles(t, minter, []string{"PACKAGING_QC"}, []string{"production:update"})
	if rr := doJSON(router, http.MethodPatch, "/production-jobs/"+jobID, qcRole, map[string]any{"status": "completed"}); rr.Code != http.StatusForbidden {
		t.Errorf("packaging_qc PATCH status = %d, want 403", rr.Code)
	}
}
