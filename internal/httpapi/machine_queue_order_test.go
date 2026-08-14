package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// seedQueuedBatch puts one batch on a machine profile with a single job at the
// given priority and due date - the two things the machine queue now orders by.
func seedQueuedBatch(
	t *testing.T, store *db.Store, number, status string, profileID uuid.UUID,
	priority int32, due *time.Time,
) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	b, err := store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: number, MachineID: &profileID,
		Status: status, MaterialShortage: false,
	})
	if err != nil {
		t.Fatalf("insert batch %s: %v", number, err)
	}
	family := "H2C"
	if _, err := store.Q.InsertProductionJob(ctx, gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: number + "-J1", BatchID: &b.ID, Description: "Test job",
		Quantity: 1, Status: production.StatusQueued, AssemblyStatus: production.AssemblyPending,
		QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
		PersonalisationStatus: production.PersonalisationNotRequired, Colours: []byte("[]"),
		MachineFamily: &family, Priority: priority, DueDate: db.Timestamptz(due),
	}); err != nil {
		t.Fatalf("insert job for %s: %v", number, err)
	}
	return b.ID
}

// fleetMachineForProfile finds the fleet unit linked to a profile, which is
// what the machine queue is addressed by.
func fleetMachineForProfile(t *testing.T, store *db.Store, profileID uuid.UUID) uuid.UUID {
	t.Helper()
	rows, err := store.Q.ListFleetMachinesWithFamily(context.Background())
	if err != nil {
		t.Fatalf("list fleet machines: %v", err)
	}
	for _, r := range rows {
		if r.MachineProfileID != nil && *r.MachineProfileID == profileID {
			return r.ID
		}
	}
	t.Fatalf("no fleet machine linked to profile %s", profileID)
	return uuid.Nil
}

// TestIntegrationMachineQueueOrdersByUrgencyNotAge is the fix for scheduling
// decisions being thrown away at the last step.
//
// The queue used to be plain `ORDER BY created_at`, so a batch of urgent,
// due-today work queued behind a routine one purely because the routine one was
// planned earlier. Every priority and due-date weight applied during batch
// scoring stopped mattering the moment the batch reached a machine - and this
// order is also what startNextBatch picks from, so it decides what actually
// prints next.
func TestIntegrationMachineQueueOrdersByUrgencyNotAge(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	profileID := seedFleetMachineWithProfile(t, store, "H2C-Q1", "H2C", production.MachineOnline)
	fleetID := fleetMachineForProfile(t, store, profileID)

	// Created oldest-first, so creation order and urgency order disagree.
	routine := seedQueuedBatch(t, store, "BATCH-ROUTINE", production.BatchOpen, profileID, 5, nil)
	urgent := seedQueuedBatch(t, store, "BATCH-URGENT", production.BatchOpen, profileID, production.UrgentPriority, nil)

	queue, err := store.Q.ListQueuedBatchesForFleetMachine(ctx, fleetID)
	if err != nil {
		t.Fatalf("ListQueuedBatchesForFleetMachine: %v", err)
	}
	if len(queue) != 2 {
		t.Fatalf("queue has %d batches, want 2", len(queue))
	}
	if queue[0].ID != urgent {
		t.Errorf("queue starts with %s, want the urgent batch; the older routine batch (%s) would print first",
			queue[0].BatchNumber, routine)
	}
}

// TestIntegrationMachineQueueBreaksTiesByDueDate: with equal priority, the bed
// carrying the nearest deadline goes first.
func TestIntegrationMachineQueueBreaksTiesByDueDate(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	profileID := seedFleetMachineWithProfile(t, store, "H2C-Q2", "H2C", production.MachineOnline)
	fleetID := fleetMachineForProfile(t, store, profileID)

	later := time.Now().Add(72 * time.Hour)
	sooner := time.Now().Add(4 * time.Hour)
	seedQueuedBatch(t, store, "BATCH-LATER", production.BatchOpen, profileID, 5, &later)
	soon := seedQueuedBatch(t, store, "BATCH-SOONER", production.BatchOpen, profileID, 5, &sooner)

	queue, err := store.Q.ListQueuedBatchesForFleetMachine(ctx, fleetID)
	if err != nil {
		t.Fatalf("ListQueuedBatchesForFleetMachine: %v", err)
	}
	if len(queue) != 2 || queue[0].ID != soon {
		t.Errorf("queue starts with %s, want the batch due in 4 hours", queue[0].BatchNumber)
	}
}

// TestIntegrationMachineQueueKeepsTheRunningBatchFirst: a batch physically on
// the bed is not a scheduling decision any more. Re-ordering it below a more
// urgent one would misreport what the machine is actually doing.
func TestIntegrationMachineQueueKeepsTheRunningBatchFirst(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	profileID := seedFleetMachineWithProfile(t, store, "H2C-Q3", "H2C", production.MachineOnline)
	fleetID := fleetMachineForProfile(t, store, profileID)

	// Routine work, but already printing.
	running := seedQueuedBatch(t, store, "BATCH-RUNNING", production.BatchInProgress, profileID, 9, nil)
	// Urgent, but only queued.
	seedQueuedBatch(t, store, "BATCH-NEXT", production.BatchOpen, profileID, production.UrgentPriority, nil)

	queue, err := store.Q.ListQueuedBatchesForFleetMachine(ctx, fleetID)
	if err != nil {
		t.Fatalf("ListQueuedBatchesForFleetMachine: %v", err)
	}
	if len(queue) != 2 || queue[0].ID != running {
		t.Errorf("queue starts with %s, want the in-progress batch; what is on the bed cannot be reordered",
			queue[0].BatchNumber)
	}
}
