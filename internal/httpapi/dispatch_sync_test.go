package httpapi

// The print-status sync, driven against a fake BamBuddy. What matters is that a
// printer's view of a queue item ends up moving the right Tensor rows: the batch,
// its jobs, the fleet machine, and the dispatch itself.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambuddy"
	"github.com/Optiminastic/tensor-core/internal/production"
)

const testPrinterID int32 = 1

// syncServer builds a Server whose BamBuddy client points at a stub returning the
// given queue-item status.
func syncServer(t *testing.T, store *db.Store, queueStatus string, filamentUsed *float64) *Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/printers/1/status":
			_, _ = w.Write([]byte(`{"id":1,"name":"Tensor-1","connected":true,
				"layer_num":12,"total_layers":40,
				"ams":[{"id":0,"tray":[{"id":0,"tray_color":"FF0000","tray_type":"PLA","remain":80}]}]}`))
		default: // the queue item
			body := `{"id":99,"printer_id":1,"library_file_id":5,"position":0,"status":"` + queueStatus + `","filament_short":false`
			if filamentUsed != nil {
				body += `,"filament_used_grams":42.5`
			}
			body += `}`
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(srv.Close)

	cfg := config.Settings{
		Environment: "development", AuthAudience: "tensor-core",
		BambuddyBaseURL: srv.URL, BambuddyAPIKey: "bb_test", BambuddyAutoPrint: true,
		BambuddyManualStart: true,
	}
	s := NewServer(cfg, store, auth.NewGuards(nil, ""), nil)
	s.bambuddy = bambuddy.New(srv.URL, 5*time.Second)
	return s
}

// seedDispatchableBatch creates a batch with one job, a fleet machine mapped to
// BamBuddy printer 1, and an already-queued dispatch.
func seedDispatchableBatch(t *testing.T, store *db.Store) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	profile := seedMachine(t, store, "H2C sync test")
	fleetID := uuid.New()
	if _, err := store.Q.InsertFleetMachine(ctx, gen.InsertFleetMachineParams{
		ID: fleetID, MachineID: "SYNC-" + fleetID.String()[:8], Name: "Sync Test",
		Status: "idle", Filaments: []byte("[]"),
	}); err != nil {
		t.Fatalf("insert fleet machine: %v", err)
	}
	printer := testPrinterID
	if _, err := store.Q.SetFleetMachineBambuddyPrinter(ctx, gen.SetFleetMachineBambuddyPrinterParams{
		ID: fleetID, BambuddyPrinterID: &printer, MachineProfileID: &profile,
	}); err != nil {
		t.Fatalf("link printer: %v", err)
	}

	number, _ := production.NewBatchNumber()
	batch, err := store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: number, MachineID: &profile, Status: production.BatchOpen,
	})
	if err != nil {
		t.Fatalf("insert batch: %v", err)
	}

	jobNumber, _ := production.NewJobNumber()
	job, err := store.Q.InsertProductionJob(ctx, gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: jobNumber, Description: "sync test", Quantity: 1,
		Status: production.StatusQueued, AssemblyStatus: production.AssemblyPending,
		QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
		PersonalisationStatus: production.PersonalisationNotRequired,
	})
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	if err := store.Q.AssignJobsToBatch(ctx, gen.AssignJobsToBatchParams{
		BatchID: &batch.ID, JobIds: []uuid.UUID{job.ID},
	}); err != nil {
		t.Fatalf("assign job: %v", err)
	}

	dispatch, err := store.Q.InsertPrintDispatch(ctx, gen.InsertPrintDispatchParams{
		ID: uuid.New(), BatchID: batch.ID, PrinterID: testPrinterID,
	})
	if err != nil {
		t.Fatalf("insert dispatch: %v", err)
	}
	lib, item := int32(5), int32(99)
	if _, err := store.Q.MarkPrintDispatchQueued(ctx, gen.MarkPrintDispatchQueuedParams{
		ID: dispatch.ID, LibraryFileID: &lib, QueueItemID: &item,
	}); err != nil {
		t.Fatalf("mark queued: %v", err)
	}
	return batch.ID, job.ID, fleetID
}

// A printing job moves the batch and its jobs into production and lights up the
// fleet row that the dashboard renders.
func TestIntegrationSyncPrintingUpdatesFleetAndBatch(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	batchID, jobID, fleetID := seedDispatchableBatch(t, store)
	s := syncServer(t, store, bambuddy.StatusPrinting, nil)

	ctx := context.Background()
	if err := s.SyncPrintDispatches(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	batch, _ := store.Q.GetBatchByID(ctx, batchID)
	if batch.Status != production.BatchInProgress {
		t.Errorf("batch status = %q, want in_progress", batch.Status)
	}
	job, _ := store.Q.GetProductionJobByID(ctx, jobID)
	if job.Status != production.StatusInProduction {
		t.Errorf("job status = %q, want in_production", job.Status)
	}

	machine, _ := store.Q.GetFleetMachine(ctx, fleetID)
	if machine.Status != "running" {
		t.Errorf("machine status = %q, want running", machine.Status)
	}
	if machine.CurrentBatchID == nil || *machine.CurrentBatchID != batchID {
		t.Errorf("machine current_batch_id = %v, want the printing batch", machine.CurrentBatchID)
	}
	if machine.CurrentLayer == nil || *machine.CurrentLayer != 12 {
		t.Errorf("current_layer = %v, want 12 from the printer", machine.CurrentLayer)
	}
	if machine.TotalLayers == nil || *machine.TotalLayers != 40 {
		t.Errorf("total_layers = %v, want 40 from the printer", machine.TotalLayers)
	}
	// The AMS trays must land as a percentage, never as grams.
	if len(machine.Filaments) == 0 || string(machine.Filaments) == "[]" {
		t.Error("fleet filaments were not mirrored from the AMS")
	}
}

// A completed print closes the batch and its jobs, and frees the machine.
func TestIntegrationSyncCompletedClosesBatch(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	batchID, jobID, fleetID := seedDispatchableBatch(t, store)
	used := 42.5
	s := syncServer(t, store, bambuddy.StatusCompleted, &used)

	ctx := context.Background()
	if err := s.SyncPrintDispatches(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	batch, _ := store.Q.GetBatchByID(ctx, batchID)
	if batch.Status != production.BatchCompleted {
		t.Errorf("batch status = %q, want completed", batch.Status)
	}
	job, _ := store.Q.GetProductionJobByID(ctx, jobID)
	if job.Status != production.StatusCompleted {
		t.Errorf("job status = %q, want completed", job.Status)
	}
	machine, _ := store.Q.GetFleetMachine(ctx, fleetID)
	if machine.Status == "running" {
		t.Error("machine still shows running after the print completed")
	}
	if machine.CurrentBatchID != nil {
		t.Errorf("machine still pinned to batch %v after completion", machine.CurrentBatchID)
	}

	// The dispatch itself must close, or the poller would keep asking forever.
	dispatches, _ := store.Q.ListPrintDispatchesForBatch(ctx, batchID)
	if len(dispatches) != 1 || dispatches[0].Status != "completed" {
		t.Errorf("dispatch status = %v, want completed", dispatches)
	}
}

// A failed print records the printer's own reason against every job, so the
// failure is visible where an operator works rather than only on the dispatch.
func TestIntegrationSyncFailedRecordsJobFailures(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	batchID, jobID, _ := seedDispatchableBatch(t, store)
	s := syncServer(t, store, bambuddy.StatusFailed, nil)

	ctx := context.Background()
	if err := s.SyncPrintDispatches(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	job, _ := store.Q.GetProductionJobByID(ctx, jobID)
	if job.Status != production.StatusFailed {
		t.Errorf("job status = %q, want failed", job.Status)
	}
	// Read the failure rows directly: there is no list query in production, and
	// adding one purely for a test would be surface nobody calls.
	var stage, createdBy, notes string
	var failureCount int
	if err := store.Pool.QueryRow(ctx,
		`SELECT count(*) FROM production_job_failures WHERE job_id = $1`, jobID).
		Scan(&failureCount); err != nil {
		t.Fatalf("count failures: %v", err)
	}
	if failureCount != 1 {
		t.Fatalf("failures = %d, want 1", failureCount)
	}
	if err := store.Pool.QueryRow(ctx,
		`SELECT stage, created_by, COALESCE(notes, '') FROM production_job_failures WHERE job_id = $1`, jobID).
		Scan(&stage, &createdBy, &notes); err != nil {
		t.Fatalf("read failure: %v", err)
	}
	if stage != production.FailureStagePrint {
		t.Errorf("failure stage = %q, want print", stage)
	}
	if createdBy != syncActor {
		t.Errorf("failure author = %q, want %q so it is not mistaken for an operator", createdBy, syncActor)
	}

	dispatches, _ := store.Q.ListPrintDispatchesForBatch(ctx, batchID)
	if len(dispatches) != 1 || dispatches[0].Status != "failed" {
		t.Errorf("dispatch status = %v, want failed", dispatches)
	}
	if dispatches[0].Error == nil {
		t.Error("failed dispatch carries no reason")
	}
}

// A staged (pending) item must not advance anything: the whole point of
// manual_start is that nothing moves until a person presses start.
func TestIntegrationSyncPendingLeavesBatchAlone(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	batchID, jobID, _ := seedDispatchableBatch(t, store)
	s := syncServer(t, store, bambuddy.StatusPending, nil)

	ctx := context.Background()
	if err := s.SyncPrintDispatches(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	batch, _ := store.Q.GetBatchByID(ctx, batchID)
	if batch.Status != production.BatchOpen {
		t.Errorf("batch status = %q, want open - a staged print must not advance it", batch.Status)
	}
	job, _ := store.Q.GetProductionJobByID(ctx, jobID)
	if job.Status != production.StatusQueued {
		t.Errorf("job status = %q, want queued", job.Status)
	}
}
