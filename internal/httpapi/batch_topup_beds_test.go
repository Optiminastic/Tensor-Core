package httpapi

// Which locked beds are offered for a top-up, and in what order.
//
// The ordering lives in SQL (ListLockedBedsWithRoom), so this is the only place
// it can be pinned. It is not the obvious "oldest first": see the query's own
// comment - a bed's age does not change when it prints, because
// ListBatchesToDispatch already sorts expedited beds to the front regardless.
// What discriminates is what the edit COSTS.

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// lockedBedWith seeds an 'open' bed holding n single-unit jobs, optionally
// marked as already sent to BambuBuddy.
func lockedBedWith(t *testing.T, store *db.Store, number string, n int, queueItem *int32) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	b, err := store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: number, Status: production.BatchOpen, MaterialShortage: false,
	})
	if err != nil {
		t.Fatalf("insert %s: %v", number, err)
	}
	for i := 0; i < n; i++ {
		seedConfiguredJob(t, store, number+"-J"+string(rune('1'+i)), jobConfig{
			batchID: &b.ID, material: "PLA", colour: "BLUE", leftNozzleMm: 0.4, machineFamily: "A2L",
		})
	}
	if queueItem != nil {
		if _, err := store.Pool.Exec(ctx,
			`UPDATE batches SET queue_item_id = $1 WHERE id = $2`, *queueItem, b.ID); err != nil {
			t.Fatalf("mark %s as sent: %v", number, err)
		}
	}
	return b.ID
}

func TestIntegrationLockedBedsWithRoomAreOfferedUnsentAndFullestFirst(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	ctx := context.Background()

	cap := srv.bedUnitCap()
	if cap != 4 {
		t.Fatalf("bed cap = %d, want 4 - this test's numbers assume the default", cap)
	}

	sent := int32(4242)
	sentThree := lockedBedWith(t, store, "BATCH-ROOM-SENT3", 3, &sent)
	unsentThree := lockedBedWith(t, store, "BATCH-ROOM-UNSENT3", 3, nil)
	unsentTwo := lockedBedWith(t, store, "BATCH-ROOM-UNSENT2", 2, nil)
	full := lockedBedWith(t, store, "BATCH-ROOM-FULL", 4, nil)

	// A Draft with room is NOT a candidate: the planner already refills those
	// every run, and taking its jobs here would hollow it out behind its back.
	draft := seedDraftBatch(t, store, "BATCH-ROOM-DRAFT", production.BatchPendingApproval, nil, "A2L")

	rows, err := store.Q.ListLockedBedsWithRoom(ctx, int32(cap))
	if err != nil {
		t.Fatalf("list locked beds with room: %v", err)
	}

	got := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		got = append(got, r.ID)
	}
	want := []uuid.UUID{unsentThree, unsentTwo, sentThree}

	if len(got) != len(want) {
		t.Fatalf("offered %d beds, want %d - full beds and Drafts must not be offered (got %v)",
			len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bed order = %v, want %v: unsent beds first (a sent bed costs a "+
				"withdrawal and re-entry at the back of the printer queue), fullest first "+
				"within that", got, want)
		}
	}

	for _, excluded := range []struct {
		name string
		id   uuid.UUID
	}{{"a full bed", full}, {"a Draft", draft}} {
		for _, r := range rows {
			if r.ID == excluded.id {
				t.Errorf("%s was offered for a top-up", excluded.name)
			}
		}
	}
}

// With no expedited work waiting, the pass opens nothing at all - a locked bed
// is never disturbed for standard work.
func TestIntegrationTopUpDoesNothingWithoutPriorityWork(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	ctx := context.Background()

	batchID := lockedBedWith(t, store, "BATCH-NOPRIO", 2, nil)
	seedConfiguredJob(t, store, "BATCH-NOPRIO-FREE", jobConfig{
		material: "PLA", colour: "BLUE", leftNozzleMm: 0.4, machineFamily: "A2L",
	})

	out := srv.TopUpLockedBedsWithPriority(ctx)
	if out.BedsFilled != 0 || out.PriorityPlaced != 0 {
		t.Errorf("a bed was opened with no expedited work waiting: %+v", out)
	}
	if units := unitsOnBedFor(t, store, batchID); units != 2 {
		t.Errorf("bed now holds %d units, want the 2 it started with", units)
	}
}

// Object storage is unconfigured in tests, and every edit ends in a rebuilt
// plate - so the pass must change nothing rather than withdraw a plate for an
// edit it cannot finish.
func TestIntegrationTopUpChangesNothingWithoutObjectStorage(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	ctx := context.Background()

	batchID := lockedBedWith(t, store, "BATCH-NOSTORE", 2, nil)
	jobID := seedConfiguredJob(t, store, "BATCH-NOSTORE-PRIO", jobConfig{
		material: "PLA", colour: "BLUE", leftNozzleMm: 0.4, machineFamily: "A2L",
	})
	if _, err := store.Pool.Exec(ctx,
		`UPDATE production_jobs SET priority = $1 WHERE id = $2`, PriorityRank, jobID); err != nil {
		t.Fatalf("rank the job: %v", err)
	}

	out := srv.TopUpLockedBedsWithPriority(ctx)
	if out.BedsFilled != 0 {
		t.Errorf("beds were filled with no object storage: %+v", out)
	}
	if units := unitsOnBedFor(t, store, batchID); units != 2 {
		t.Errorf("bed now holds %d units, want the 2 it started with", units)
	}
	job, err := store.Q.GetProductionJobByID(ctx, jobID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if job.BatchID != nil {
		t.Error("the expedited job was assigned to a bed whose plate could never be built")
	}
}

// unitsOnBedFor counts the products currently on a bed.
func unitsOnBedFor(t *testing.T, store *db.Store, batchID uuid.UUID) int {
	t.Helper()
	jobs, err := store.Q.ListJobsForBatch(context.Background(), &batchID)
	if err != nil {
		t.Fatalf("read the bed's jobs: %v", err)
	}
	return unitsOf(jobs)
}
