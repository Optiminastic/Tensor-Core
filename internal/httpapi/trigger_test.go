package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

// testServerWithBatchQueue builds a *Server with a real batch-plan enqueuer
// wired to an insert-only River client against the test DB (setupStore
// already runs db.MigrateRiver), so triggerBatchPlan/
// triggerBatchPlanIfThresholdMet actually insert into river_job instead of
// no-op'ing on a nil batchEnqueuer the way plain testServer's *Server does.
func testServerWithBatchQueue(t *testing.T, store *db.Store, guards *auth.Guards, threshold int) *Server {
	t.Helper()
	client, err := slicing.NewInsertOnlyClient(store.Pool)
	if err != nil {
		t.Fatalf("build river client: %v", err)
	}
	cfg := config.Settings{
		Environment: "development", AuthAudience: "tensor-core",
		CORSOrigins:           []string{"http://localhost:3001"},
		BatchPlanJobThreshold: threshold, BatchPlanDebounceSeconds: 5,
	}
	s := NewServer(cfg, store, guards, nil)
	s.EnableProductionQueue(
		production.NewJobCreationEnqueuer(client),
		production.NewBatchPlanEnqueuer(client, 5*time.Second),
	)
	return s
}

func countPlanBatchesJobs(t *testing.T, store *db.Store) int {
	t.Helper()
	var count int
	if err := store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = 'plan_batches'`).Scan(&count); err != nil {
		t.Fatalf("count plan_batches jobs: %v", err)
	}
	return count
}

// seedBatchableJob inserts a minimal production_jobs row matching
// ListBatchableJobs'/CountBatchableJobs' WHERE clause exactly (queued,
// resolved personalisation, no issue, unbatched, quantity>0) - bypassing the
// SKU-match pipeline entirely, since these tests only care about the
// trigger threshold, not real design matching.
func seedBatchableJob(t *testing.T, store *db.Store) {
	t.Helper()
	jobNumber, err := store.Q.NextJobNumber(context.Background())
	if err != nil {
		t.Fatalf("next job number: %v", err)
	}
	if _, err := store.Q.InsertProductionJob(context.Background(), gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: jobNumber, Description: "Test job", Quantity: 1,
		Status: production.StatusQueued, AssemblyStatus: production.AssemblyPending,
		QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
		PersonalisationStatus: production.PersonalisationNotRequired, Colours: []byte("[]"),
	}); err != nil {
		t.Fatalf("seed batchable job: %v", err)
	}
}

func TestIntegrationTriggerBatchPlanSkipsBelowThreshold(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	s := testServerWithBatchQueue(t, store, nil, 3)

	seedBatchableJob(t, store)
	seedBatchableJob(t, store)

	s.triggerBatchPlanIfThresholdMet(context.Background())
	if got := countPlanBatchesJobs(t, store); got != 0 {
		t.Errorf("plan_batches jobs = %d, want 0 (2 batchable jobs is below the threshold of 3)", got)
	}
}

func TestIntegrationTriggerBatchPlanFiresAtThreshold(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	s := testServerWithBatchQueue(t, store, nil, 2)

	seedBatchableJob(t, store)
	seedBatchableJob(t, store)

	s.triggerBatchPlanIfThresholdMet(context.Background())
	if got := countPlanBatchesJobs(t, store); got != 1 {
		t.Errorf("plan_batches jobs = %d, want 1 (2 batchable jobs clears the threshold of 2)", got)
	}
}

func TestIntegrationBatchCompletedTriggersReplanUnconditionally(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	// threshold=1000: proves the batch-completed trigger fires unconditionally,
	// not gated on the batchable-job count the way the per-job triggers are.
	s := testServerWithBatchQueue(t, store, guards, 1000)
	router := s.Router()

	manage := minter.mint(t, []string{"batch:manage", "batch:read"})
	rr := doJSON(router, http.MethodPost, "/batches", manage, map[string]any{})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create batch = %d body=%s", rr.Code, rr.Body.String())
	}
	var batch batchView
	_ = json.Unmarshal(rr.Body.Bytes(), &batch)

	if got := countPlanBatchesJobs(t, store); got != 0 {
		t.Fatalf("plan_batches jobs after create = %d, want 0", got)
	}
	if rr := doJSON(router, http.MethodPatch, "/batches/"+batch.ID, manage, map[string]any{"status": "completed"}); rr.Code != http.StatusOK {
		t.Fatalf("patch batch completed = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := countPlanBatchesJobs(t, store); got != 1 {
		t.Errorf("plan_batches jobs after completing a batch = %d, want 1 (unconditional trigger)", got)
	}
}

func TestIntegrationFailProductionJobTriggersReplanUnconditionally(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	// threshold=1000: proves the reprint-created trigger fires
	// unconditionally too, same as batch-completed above.
	s := testServerWithBatchQueue(t, store, guards, 1000)
	router := s.Router()

	orderID := seedOrder(t, store, 9201, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA"},
	})
	create := minter.mint(t, []string{"production:create", "production:read"})
	rr := doJSON(router, http.MethodPost, "/production-jobs/from-order/"+orderID.String(), create, nil)
	if rr.Code != http.StatusCreated {
		t.Fatalf("from-order = %d body=%s", rr.Code, rr.Body.String())
	}
	var jobs []jobView
	_ = json.Unmarshal(rr.Body.Bytes(), &jobs)
	jobID := jobs[0].ID

	admin := minter.mint(t, []string{"production:update"})
	if rr := doJSON(router, http.MethodPatch, "/production-jobs/"+jobID, admin, map[string]any{"status": "in_production"}); rr.Code != http.StatusOK {
		t.Fatalf("PATCH status=in_production = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := countPlanBatchesJobs(t, store); got != 0 {
		t.Fatalf("plan_batches jobs before fail = %d, want 0", got)
	}

	failTok := minter.mint(t, []string{"production:fail"})
	if rr := doJSON(router, http.MethodPost, "/production-jobs/"+jobID+"/fail", failTok, map[string]any{"reason": "warping"}); rr.Code != http.StatusOK {
		t.Fatalf("fail = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := countPlanBatchesJobs(t, store); got != 1 {
		t.Errorf("plan_batches jobs after a reprint was created = %d, want 1 (unconditional trigger)", got)
	}
}

// TestIntegrationAssignMachinePrefersMaterialMatch confirms the weighted
// scheduler's material-match preference actually changes which real,
// DB-backed candidate assignMachineForBatch picks - not just the pure
// EffectiveFreeAt/BestMachine math already covered in
// internal/production/scheduler_test.go.
func TestIntegrationAssignMachinePrefersMaterialMatch(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	guards := auth.NewGuards(nil, "")
	s := testServerWithBatchQueue(t, store, guards, 1000)
	ctx := context.Background()

	profileA := seedFleetMachineWithProfile(t, store, "H2C-A", "H2C", "online")
	profileB := seedFleetMachineWithProfile(t, store, "H2C-B", "H2C", "online")

	// Machine A already has an open (queued) batch of PLA jobs; Machine B
	// has nothing. Both are otherwise equally free (raw FreeAt now).
	loadedBatchID := seedDraftBatch(t, store, "BATCH-LOADED", production.BatchOpen, &profileA, "H2C")
	if _, err := store.Pool.Exec(ctx, `UPDATE production_jobs SET material = 'PLA' WHERE batch_id = $1`, loadedBatchID); err != nil {
		t.Fatalf("set material: %v", err)
	}

	best := s.assignMachineForBatch(ctx, "H2C", "PLA", nil)
	if best == nil || *best != profileA {
		t.Errorf("assignMachineForBatch(PLA) = %v, want %s (the PLA-loaded machine, despite its small queue-length penalty), not the empty %s",
			best, profileA, profileB)
	}
}

// TestIntegrationJobCreationWorkerTriggersReplan pins the wiring bug in
// cmd/productionworker: it built a Server but never called
// EnableProductionQueue, so s.batchEnqueuer was nil,
// triggerBatchPlanIfThresholdMet hit its nil guard and returned silently, and
// job creation in that process never prompted a replan. Batching still worked
// via the periodic tick, so nothing looked broken - it was just up to
// BatchPlanIntervalMinutes late.
//
// The pair matters: the second case proves the first is actually exercising
// the enqueuer rather than passing for some unrelated reason.
func TestIntegrationJobCreationWorkerTriggersReplan(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")

	t.Run("with the queue wired", func(t *testing.T) {
		s := testServerWithBatchQueue(t, store, guards, 1)
		seedBatchableJob(t, store)

		before := countPlanBatchesJobs(t, store)
		s.triggerBatchPlanIfThresholdMet(context.Background())
		if after := countPlanBatchesJobs(t, store); after != before+1 {
			t.Errorf("plan_batches jobs = %d, want %d - the threshold trigger did not enqueue", after, before+1)
		}
	})

	t.Run("without the queue wired", func(t *testing.T) {
		s := NewServer(config.Settings{
			Environment: "development", AuthAudience: "tensor-core",
			BatchPlanJobThreshold: 1,
		}, store, guards, nil)
		seedBatchableJob(t, store)

		before := countPlanBatchesJobs(t, store)
		s.triggerBatchPlanIfThresholdMet(context.Background())
		if after := countPlanBatchesJobs(t, store); after != before {
			t.Errorf("plan_batches jobs = %d, want %d - a nil enqueuer must be a silent no-op, not a panic or an insert", after, before)
		}
	})
}
