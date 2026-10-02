package httpapi

// A bed stays open to new work until it is full, then it locks.
//
// These two rules are the whole point of colour batching in practice: a bed of
// two blue planks must be able to take a third when the next blue order lands
// (otherwise every customer opens a bed of their own), and a bed of four must
// stop changing (otherwise the plate an operator is looking at is rearranged
// under them by the next planner pass).

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// seedBedWith returns a fresh Draft bed holding exactly n single-unit jobs.
// seedDraftBatch already puts one on it, so this tops it up to n.
func seedBedWith(t *testing.T, store *db.Store, batchNumber string, n int) uuid.UUID {
	t.Helper()
	batchID := seedDraftBatch(t, store, batchNumber, production.BatchPendingApproval, nil, "A2L")
	for i := 1; i < n; i++ {
		seedConfiguredJob(t, store, fmt.Sprintf("%s-J%d", batchNumber, i+1), jobConfig{
			batchID: &batchID, material: "PLA", leftNozzleMm: 0.4, machineFamily: "A2L",
		})
	}
	return batchID
}

// Four products is full; three is not. bedIsFull is what the planner, the
// dispatcher and the add-jobs endpoint all ask, so this is the single rule
// behind "locked" everywhere it appears.
func TestIntegrationBedIsFullAtTheUnitCap(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	ctx := context.Background()

	// One cap for the whole fleet: a plate is built before anything knows which
	// printer will take it, so it has to fit the smallest bed.
	cap := srv.bedUnitCap()
	if cap != production.MaxColourBatchUnits {
		t.Fatalf("bed cap = %d, want %d - this test's numbers assume the default",
			cap, production.MaxColourBatchUnits)
	}
	partial := seedBedWith(t, store, "BATCH-LOCK-PARTIAL", cap-1)
	full := seedBedWith(t, store, "BATCH-LOCK-FULL", cap)

	if isFull, err := srv.bedIsFull(ctx, partial); err != nil || isFull {
		t.Errorf("a bed of %d is full=%v (err %v); it must stay open so the next "+
			"matching colour can join it", cap-1, isFull, err)
	}
	if isFull, err := srv.bedIsFull(ctx, full); err != nil || !isFull {
		t.Errorf("a bed of %d is full=%v (err %v); it must lock", cap, isFull, err)
	}
}

// Quantity counts. One job for three planks fills three places, not one -
// otherwise a bed could be loaded with four jobs of three and go to a printer
// holding twelve planks.
func TestIntegrationBedFullnessCountsQuantityNotJobs(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	ctx := context.Background()

	// One job, holding the whole bed's worth.
	batchID := seedDraftBatch(t, store, "BATCH-LOCK-QTY", production.BatchPendingApproval, nil, "A2L")
	cap := srv.bedUnitCap()
	// Raw UPDATE: the seed helpers fix quantity at 1 and no query exposes it on
	// its own, and inventing one for a test would put a setter in the
	// production surface that nothing else needs.
	if _, err := store.Pool.Exec(ctx,
		`UPDATE production_jobs SET quantity = $1 WHERE batch_id = $2`,
		cap, batchID); err != nil {
		t.Fatalf("set job quantity: %v", err)
	}

	if isFull, err := srv.bedIsFull(ctx, batchID); err != nil || !isFull {
		t.Errorf("a bed holding one job of %d units is full=%v (err %v), want full",
			cap, isFull, err)
	}
}

// A locked bed's machine is fixed. Approval reserved that machine's filament
// and sliced the plate for it, so moving it afterwards would leave the
// reservation on one printer and the plate on another.
func TestIntegrationLockedBatchRefusesAMachineChange(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	manage := minter.mint(t, []string{"batch:manage"})

	machineID := seedMachine(t, store, "LOCK-P2S-01")
	locked := seedDraftBatch(t, store, "BATCH-LOCKED-1", production.BatchOpen, &machineID, "P2S")
	other := seedMachine(t, store, "LOCK-P2S-02")

	rr := doJSON(router, "PATCH", "/batches/"+locked.String(), manage,
		map[string]any{"machine_id": other.String()})
	if rr.Code != 409 {
		t.Errorf("machine change on a locked batch = %d, want 409", rr.Code)
	}

	// The status must still move: this is how an operator marks the bed Done.
	rr = doJSON(router, "PATCH", "/batches/"+locked.String(), manage,
		map[string]any{"status": production.BatchCompleted})
	if rr.Code != 200 {
		t.Errorf("marking a locked batch done = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
}

// The top-up rule, end to end: a bed with room takes the next job in its colour
// instead of a new bed being opened for it.
//
// This is the behaviour the whole under-full-bed design exists for. Without it
// three blue orders arriving one at a time produce three beds of one plank, each
// occupying a printer for its own run.
func TestIntegrationAnUnderFullBedAbsorbsTheNextMatchingJob(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)
	manage := minter.mint(t, []string{"batch:manage", "batch:read"})

	// A plank-sized model, so four fit on a bed the way the real product does.
	fileID := seedFileAsset(t, store, 200, 50, 40)

	// Two blue planks first.
	first := seedOrder(t, store, 8801, []map[string]any{
		{"product_id": "SKU1", "product_name": "Plank", "quantity": 1, "material": "PLA", "colour": "BLUE"},
		{"product_id": "SKU1", "product_name": "Plank", "quantity": 1, "material": "PLA", "colour": "BLUE"},
	})
	for _, j := range fromOrderJobs(t, router, minter, first) {
		givePrintFile(t, store, uuid.MustParse(j.ID), fileID)
	}
	if rr := doJSON(router, "POST", "/batches/auto-create", manage, nil); rr.Code != 200 {
		t.Fatalf("first auto-create = %d body=%s", rr.Code, rr.Body.String())
	}
	before := bedsWithUnits(t, store)
	if len(before) != 1 || before[0] != 2 {
		t.Fatalf("beds after two blue planks = %v, want one bed of 2", before)
	}

	// A third blue plank arrives.
	second := seedOrder(t, store, 8802, []map[string]any{
		{"product_id": "SKU1", "product_name": "Plank", "quantity": 1, "material": "PLA", "colour": "BLUE"},
	})
	for _, j := range fromOrderJobs(t, router, minter, second) {
		givePrintFile(t, store, uuid.MustParse(j.ID), fileID)
	}
	if rr := doJSON(router, "POST", "/batches/auto-create", manage, nil); rr.Code != 200 {
		t.Fatalf("second auto-create = %d body=%s", rr.Code, rr.Body.String())
	}

	after := bedsWithUnits(t, store)
	if len(after) != 1 || after[0] != 3 {
		t.Errorf("beds after the third blue plank = %v, want one bed of 3 - the new job "+
			"must join the bed with room, not open one of its own", after)
	}
}

// bedsWithUnits returns the unit count of every batch that still holds jobs,
// ordered, so a test can say "one bed of three" without caring about ids.
func bedsWithUnits(t *testing.T, store *db.Store) []int {
	t.Helper()
	rows, err := store.Pool.Query(context.Background(),
		`SELECT coalesce(sum(j.quantity), 0)::int AS units
		   FROM batches b JOIN production_jobs j ON j.batch_id = b.id
		  GROUP BY b.id ORDER BY 1`)
	if err != nil {
		t.Fatalf("count beds: %v", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var units int
		if err := rows.Scan(&units); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, units)
	}
	return out
}

// A bed under the floor is never locked by the dispatcher, however long it
// waits.
//
// The Draft is the only state that can still absorb the next order in its
// colour - the planner dissolves and reforms Drafts on every run - so locking a
// bed of one commits a wasted plate AND denies the orders behind it their
// place.
//
// There used to be an escape after BATCH_MAX_WAIT_HOURS. It is gone: the
// judgement moved from a clock to a person, who can still approve a thin bed by
// hand.
//
// A nil fleet snapshot is passed deliberately. It makes the FLOOR the only
// thing under test: with no fleet to ask, readyToLock must answer false for
// every bed, so a bed that clears the floor here and still returns false is
// proof the second half of the gate ran - which the test below is for.
func TestIntegrationABedUnderTheFloorIsNotReadyToLock(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	ctx := context.Background()

	floor := srv.bedUnitFloor()
	thinID := seedBedWith(t, store, "BATCH-READY-THIN", floor-1)

	// Old enough that the retired aging rule would have released it.
	if _, err := store.Pool.Exec(ctx,
		`UPDATE batches SET created_at = now() - interval '30 days' WHERE id = $1`,
		thinID); err != nil {
		t.Fatalf("age the thin bed: %v", err)
	}

	thin, err := store.Q.GetBatchByID(ctx, thinID)
	if err != nil {
		t.Fatalf("load the thin bed: %v", err)
	}

	if srv.readyToLock(ctx, thin, nil) {
		t.Errorf("a bed of %d locked after waiting; under the floor of %d it must stay "+
			"a Draft so the next orders in its colour can complete it", floor-1, floor)
	}
}

// Clearing the floor is NOT enough on its own - a printer has to be free.
//
// This is the rule that replaced "a priority bed locks under-full". The shop's
// instruction: a batch locks when it holds at least the floor AND a machine of
// its class is standing empty with the right colours. Locking is one-way, so a
// bed committed while the fleet is full can neither print nor grow.
//
// Expressed with a nil snapshot, which is what the dispatcher passes when the
// fleet cannot be read - the strictest form of "no printer is available". The
// positive case needs a fleet with loaded trays and is covered by the
// weighMachine tests in batch_auto_machine_test.go, which decide eligibility
// for this gate and the send path alike.
func TestIntegrationABedAtTheFloorStillWaitsForAFreePrinter(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	ctx := context.Background()

	floor := srv.bedUnitFloor()
	batchID := seedBedWith(t, store, "BATCH-READY-FLOOR", floor)

	batch, err := store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		t.Fatalf("load the bed: %v", err)
	}

	if srv.readyToLock(ctx, batch, nil) {
		t.Errorf("a bed of %d locked with no printer available; the floor is necessary "+
			"but not sufficient", floor)
	}
}

// Expedited work no longer buys a lock.
//
// It used to: a bed carrying one priority plank locked however thin it was, on
// the reasoning that priority orders arrive one or two per colour per day and
// would otherwise wait days for company. The shop has instructed that priority
// orders are treated as normal ones on a first-come-first-served basis, so such
// a bed stays an open Draft and keeps absorbing jobs of its colour profile.
func TestIntegrationAPriorityBedIsNotLockedUnderTheFloor(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	ctx := context.Background()

	floor := srv.bedUnitFloor()
	batchID := seedBedWith(t, store, "BATCH-PRIO-PARTIAL", floor-1)

	// Every plank on it was expedited - the strongest form of the old rule.
	if _, err := store.Pool.Exec(ctx,
		`UPDATE production_jobs SET priority = $1 WHERE batch_id = $2`,
		PriorityRank, batchID); err != nil {
		t.Fatalf("rank the planks on the bed: %v", err)
	}

	batch, err := store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		t.Fatalf("load the bed: %v", err)
	}

	if srv.readyToLock(ctx, batch, nil) {
		t.Errorf("a bed of %d carrying only expedited work locked; priority is read as "+
			"normal now, so it must wait for the floor like any other bed", floor-1)
	}
}
