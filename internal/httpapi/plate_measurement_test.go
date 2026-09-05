package httpapi

// Reading a plate's real print time back from BambuBuddy.
//
// The number matters more than it looks: when a machine comes free is arithmetic
// on print time, and every batch in the database had none, so the scheduler was
// projecting availability from an approximation of an approximation.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// bambuQueueStub serves one canned /api/v1/queue/ response.
func bambuQueueStub(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/queue/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A queued bed gains the time and filament BambuBuddy measured when it sliced.
func TestIntegrationPlateMeasurementIsReadBackFromBambuBuddy(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	ctx := context.Background()

	machineID := seedMachine(t, store, "MEASURE-A2L-01")
	batchID := seedDraftBatch(t, store, "BATCH-MEASURE-1", production.BatchOpen, &machineID, "A2L")
	units := int32(6)
	if _, err := store.Pool.Exec(ctx,
		`UPDATE batches SET queue_item_id = 501, units_per_bed = $2 WHERE id = $1`,
		batchID, units); err != nil {
		t.Fatalf("mark the bed queued: %v", err)
	}

	stub := bambuQueueStub(t, `[
		{"id": 501, "status": "pending", "print_time_seconds": 7380,
		 "filament_used_grams": 214.5, "sliced_for_model": "A2L"},
		{"id": 999, "status": "pending", "print_time_seconds": 1200}
	]`)
	srv := testServerWithBambu(t, store, auth.NewGuards(minter.verifier, ""), stub.URL)

	out := srv.RecordPlateMeasurements(ctx)
	if out.Measured != 1 {
		t.Fatalf("measured %d beds, want 1 (awaiting %d, pending %d, missing %d)",
			out.Measured, out.Awaiting, out.Pending, out.Missing)
	}

	batch, err := store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		t.Fatalf("reload batch: %v", err)
	}
	if batch.TotalPrintTimeMinutes == nil || *batch.TotalPrintTimeMinutes != 123 {
		t.Errorf("print time = %v, want 123 minutes (7380s)", batch.TotalPrintTimeMinutes)
	}
	if !batch.PlateSlicedAt.Valid {
		t.Error("the bed is not marked as measured, so every pass would re-record it")
	}
	// Per unit, from the same total, so the two can never disagree: 123/6.
	eff := numFloat(t, batch.EffectiveTimePerUnitMinutes)
	if eff < 20.4 || eff > 20.6 {
		t.Errorf("effective per-unit time = %.2f, want about 20.5 (123 minutes over 6 planks)", eff)
	}
	if grams := numFloat(t, batch.TotalFilamentGrams); grams < 214 || grams > 215 {
		t.Errorf("filament = %.1fg, want 214.5", grams)
	}

	// Idempotent: a second pass has nothing left to do.
	if again := srv.RecordPlateMeasurements(ctx); again.Measured != 0 || again.Awaiting != 0 {
		t.Errorf("second pass measured %d of %d awaiting, want nothing left",
			again.Measured, again.Awaiting)
	}
}

// A bed BambuBuddy has queued but not yet sliced carries no time, and must not
// be written with a zero - "not measured" and "takes no time" are different
// things, and the second one tells the scheduler a machine is always free.
func TestIntegrationAnUnslicedQueueItemIsLeftAlone(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	ctx := context.Background()

	machineID := seedMachine(t, store, "MEASURE-A2L-02")
	batchID := seedDraftBatch(t, store, "BATCH-MEASURE-2", production.BatchOpen, &machineID, "A2L")
	if _, err := store.Pool.Exec(ctx,
		`UPDATE batches SET queue_item_id = 502 WHERE id = $1`, batchID); err != nil {
		t.Fatalf("mark the bed queued: %v", err)
	}

	stub := bambuQueueStub(t, `[{"id": 502, "status": "pending", "print_time_seconds": 0}]`)
	srv := testServerWithBambu(t, store, auth.NewGuards(minter.verifier, ""), stub.URL)

	out := srv.RecordPlateMeasurements(ctx)
	if out.Measured != 0 || out.Pending != 1 {
		t.Errorf("measured %d / pending %d, want 0 measured and 1 pending", out.Measured, out.Pending)
	}
	batch, err := store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		t.Fatalf("reload batch: %v", err)
	}
	if batch.TotalPrintTimeMinutes != nil {
		t.Errorf("print time = %v, want nil - an unsliced plate has no measurement",
			*batch.TotalPrintTimeMinutes)
	}
}

// A bed pointing at a queue item BambuBuddy no longer has is counted, not
// silently skipped: it means somebody removed the item, and the bed is stuck.
func TestIntegrationAVanishedQueueItemIsReported(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	ctx := context.Background()

	machineID := seedMachine(t, store, "MEASURE-A2L-03")
	batchID := seedDraftBatch(t, store, "BATCH-MEASURE-3", production.BatchOpen, &machineID, "A2L")
	if _, err := store.Pool.Exec(ctx,
		`UPDATE batches SET queue_item_id = 503 WHERE id = $1`, batchID); err != nil {
		t.Fatalf("mark the bed queued: %v", err)
	}

	stub := bambuQueueStub(t, `[]`)
	srv := testServerWithBambu(t, store, auth.NewGuards(minter.verifier, ""), stub.URL)

	if out := srv.RecordPlateMeasurements(ctx); out.Missing != 1 {
		t.Errorf("missing = %d, want 1", out.Missing)
	}
}

// testServerWithBambu builds a Server pointed at a stub BambuBuddy.
func testServerWithBambu(t *testing.T, store *db.Store, guards *auth.Guards, baseURL string) *Server {
	t.Helper()
	cfg := config.Settings{
		Environment: "development", AuthAudience: "tensor-core",
		CORSOrigins:      []string{"http://localhost:3001"},
		BambuBuddyURL:    baseURL,
		BambuBuddyAPIKey: "test-key",
	}
	return NewServer(cfg, store, guards, nil)
}

// numFloat reads a pgtype.Numeric as a float, failing the test if it is unset -
// a nil measurement is a different bug from a wrong one.
func numFloat(t *testing.T, n pgtype.Numeric) float64 {
	t.Helper()
	v := db.NumFloatPtr(n)
	if v == nil {
		t.Fatal("expected a measured value, got none")
	}
	return *v
}
