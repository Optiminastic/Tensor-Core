package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func orderFailureServer(t *testing.T, store *db.Store, guards *auth.Guards) *Server {
	t.Helper()
	return NewServer(config.Settings{
		Environment: "development", AuthAudience: "tensor-core",
		CORSOrigins: []string{"http://localhost:3001"},
	}, store, guards, nil)
}

func orderFailure(t *testing.T, store *db.Store, orderID uuid.UUID) *string {
	t.Helper()
	var reason *string
	if err := store.Pool.QueryRow(context.Background(),
		`SELECT job_creation_error FROM orders WHERE id = $1`, orderID).Scan(&reason); err != nil {
		t.Fatalf("read job_creation_error: %v", err)
	}
	return reason
}

// seedUnbuildableOrder makes an order whose line_items cannot be unmarshalled
// into []production.LineItem, so CreateJobsForOrder fails deterministically
// without needing a broken database or a stubbed Shopify.
func seedUnbuildableOrder(t *testing.T, store *db.Store, shopifyID int64) uuid.UUID {
	t.Helper()
	id := seedOrder(t, store, shopifyID, nil)
	if _, err := store.Pool.Exec(context.Background(),
		`UPDATE orders SET line_items = '{"not":"an array"}'::jsonb WHERE id = $1`, id); err != nil {
		t.Fatalf("corrupt line items: %v", err)
	}
	return id
}

// TestIntegrationJobCreationFinalAttemptMarksOrder covers the silent loss: a
// discarded create_jobs_from_order job used to leave the order with zero jobs,
// status still 'queued', and nothing recording why.
func TestIntegrationJobCreationFinalAttemptMarksOrder(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	s := orderFailureServer(t, store, guards)
	w := NewJobCreationWorker(s, nil)

	orderID := seedUnbuildableOrder(t, store, 15001)

	err := w.Work(context.Background(), &river.Job[production.CreateJobsArgs]{
		JobRow: &rivertype.JobRow{Attempt: 5, MaxAttempts: 5},
		Args:   production.CreateJobsArgs{OrderID: orderID},
	})
	if err == nil {
		t.Fatal("Work returned nil for an order whose jobs cannot be built")
	}
	if reason := orderFailure(t, store, orderID); reason == nil {
		t.Error("job_creation_error is null after the final attempt - the failure is invisible again")
	}
}

// TestIntegrationJobCreationNonFinalAttemptDoesNotMark: River will retry, so
// recording a failure now would be noise that a later success has to clean up.
func TestIntegrationJobCreationNonFinalAttemptDoesNotMark(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	s := orderFailureServer(t, store, guards)
	w := NewJobCreationWorker(s, nil)

	orderID := seedUnbuildableOrder(t, store, 15002)

	if err := w.Work(context.Background(), &river.Job[production.CreateJobsArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1, MaxAttempts: 5},
		Args:   production.CreateJobsArgs{OrderID: orderID},
	}); err == nil {
		t.Fatal("Work returned nil for an order whose jobs cannot be built")
	}
	if reason := orderFailure(t, store, orderID); reason != nil {
		t.Errorf("job_creation_error = %q on attempt 1 of 5, want null - River will retry", *reason)
	}
}

// TestIntegrationRetryClearsJobCreationFailure pins that the existing backfill
// endpoint is the retry mechanism - there is deliberately no separate retry
// route.
func TestIntegrationRetryClearsJobCreationFailure(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	s := orderFailureServer(t, store, guards)
	router := s.Router()

	orderID := seedUnbuildableOrder(t, store, 15003)
	if err := s.markOrderJobCreationFailed(context.Background(), orderID,
		errors.New("order line items are corrupt")); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	// Repair the order, then retry through the public endpoint.
	if _, err := store.Pool.Exec(context.Background(),
		`UPDATE orders SET line_items = '[{"product_id":"SKU1","product_name":"Cube","quantity":1}]'::jsonb
		 WHERE id = $1`, orderID); err != nil {
		t.Fatalf("repair line items: %v", err)
	}

	create := minter.mint(t, []string{"production:create", "production:read"})
	if rr := doJSON(router, http.MethodPost,
		"/production-jobs/from-order/"+orderID.String(), create, nil); rr.Code != http.StatusCreated {
		t.Fatalf("retry = %d body=%s", rr.Code, rr.Body.String())
	}
	if reason := orderFailure(t, store, orderID); reason != nil {
		t.Errorf("job_creation_error = %q after a successful retry, want null", *reason)
	}
}

// TestIntegrationListOrdersWithoutJobs covers what the marker column cannot:
// an order the worker never reached at all leaves no error to record.
func TestIntegrationListOrdersWithoutJobs(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	s := orderFailureServer(t, store, guards)
	router := s.Router()

	withJobs := seedOrder(t, store, 15004, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA"},
	})
	fromOrderJobs(t, router, minter, withJobs)
	without := seedOrder(t, store, 15005, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA"},
	})

	rows, err := store.Q.ListOrdersWithoutJobs(context.Background(), nil)
	if err != nil {
		t.Fatalf("list orders without jobs: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != without {
		t.Fatalf("orders without jobs = %d rows (%v), want exactly the one that produced none", len(rows), rows)
	}
}
