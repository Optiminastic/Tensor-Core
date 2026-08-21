package httpapi

// The batch plate-slice lifecycle at the database seam (migration 0041). The
// slice itself needs Bambu Studio and object storage, so what is asserted here is
// the state machine the worker drives: claimed -> done, or claimed -> failed with
// a reason, and never "silently no artefact".

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func seedBatchRow(t *testing.T, store *db.Store) uuid.UUID {
	t.Helper()
	number, err := production.NewBatchNumber()
	if err != nil {
		t.Fatalf("batch number: %v", err)
	}
	b, err := store.Q.InsertBatch(context.Background(), gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: number, Status: production.BatchOpen,
	})
	if err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	return b.ID
}

func TestIntegrationBatchSliceRecordsGcode(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	id := seedBatchRow(t, store)

	// A fresh batch has no printable artefact yet.
	before, err := store.Q.GetBatchByID(ctx, id)
	if err != nil {
		t.Fatalf("get batch: %v", err)
	}
	if before.GcodeKey != nil {
		t.Fatalf("new batch already has a gcode_key %v", before.GcodeKey)
	}

	if err := store.Q.MarkBatchSlicing(ctx, id); err != nil {
		t.Fatalf("mark slicing: %v", err)
	}
	claimed, err := store.Q.GetBatchByID(ctx, id)
	if err != nil {
		t.Fatalf("get batch: %v", err)
	}
	if claimed.SliceStatus == nil || *claimed.SliceStatus != "running" {
		t.Errorf("slice_status = %v, want running", claimed.SliceStatus)
	}

	key := "gcode/batches/" + id.String() + ".gcode.3mf"
	done, err := store.Q.SetBatchGcode(ctx, gen.SetBatchGcodeParams{ID: id, GcodeKey: &key})
	if err != nil {
		t.Fatalf("set gcode: %v", err)
	}
	if done.GcodeKey == nil || *done.GcodeKey != key {
		t.Errorf("gcode_key = %v, want %s", done.GcodeKey, key)
	}
	if done.SliceStatus == nil || *done.SliceStatus != "done" {
		t.Errorf("slice_status = %v, want done", done.SliceStatus)
	}
	if done.SlicedAt.Time.IsZero() {
		t.Error("sliced_at not stamped")
	}
	if done.SliceError != nil {
		t.Errorf("slice_error = %v, want nil on success", done.SliceError)
	}
}

// A failed plate slice must leave a reason. Without one the batch is
// indistinguishable from one that was simply never sliced.
func TestIntegrationBatchSliceRecordsFailureReason(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	id := seedBatchRow(t, store)

	if err := store.Q.MarkBatchSlicing(ctx, id); err != nil {
		t.Fatalf("mark slicing: %v", err)
	}
	reason := "slice produced no G-code: return_code -104: outside printable area"
	if err := store.Q.FailBatchSlice(ctx, gen.FailBatchSliceParams{ID: id, SliceError: &reason}); err != nil {
		t.Fatalf("fail slice: %v", err)
	}

	failed, err := store.Q.GetBatchByID(ctx, id)
	if err != nil {
		t.Fatalf("get batch: %v", err)
	}
	if failed.SliceStatus == nil || *failed.SliceStatus != "failed" {
		t.Errorf("slice_status = %v, want failed", failed.SliceStatus)
	}
	if failed.SliceError == nil || *failed.SliceError != reason {
		t.Errorf("slice_error = %v, want the slicer's reason", failed.SliceError)
	}
	if failed.GcodeKey != nil {
		t.Errorf("failed slice left a gcode_key %v", failed.GcodeKey)
	}
}

// Re-running a slice clears the previous failure, so a retry that succeeds does
// not leave a stale error on a batch that now has a good plate.
func TestIntegrationBatchSliceRetryClearsError(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	id := seedBatchRow(t, store)

	reason := "transient failure"
	if err := store.Q.FailBatchSlice(ctx, gen.FailBatchSliceParams{ID: id, SliceError: &reason}); err != nil {
		t.Fatalf("fail slice: %v", err)
	}
	if err := store.Q.MarkBatchSlicing(ctx, id); err != nil {
		t.Fatalf("re-claim: %v", err)
	}

	retried, err := store.Q.GetBatchByID(ctx, id)
	if err != nil {
		t.Fatalf("get batch: %v", err)
	}
	if retried.SliceError != nil {
		t.Errorf("slice_error = %v, want cleared on re-claim", retried.SliceError)
	}
}
