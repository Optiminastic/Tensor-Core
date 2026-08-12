package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// numeric builds a pgtype.Numeric for constructing a gen.ProductionJob
// directly in tests - the model type sqlc generates for a SELECT, distinct
// from the *float64 InsertProductionJobParams takes as input.
func numeric(f float64) pgtype.Numeric {
	var n pgtype.Numeric
	if err := n.Scan(strconv.FormatFloat(f, 'f', -1, 64)); err != nil {
		panic(err)
	}
	return n
}

// TestApplyMatchSnapshotsGeometryAndSlicedWeights covers Phase C: bounding
// box and colour count are per-unit facts and must not scale with quantity,
// while support/purge weight follow filament_grams_required's existing
// whole-job-total convention (same source metric, same reasoning).
func TestApplyMatchSnapshotsGeometryAndSlicedWeights(t *testing.T) {
	bx, by, bz := 100.0, 50.0, 25.0
	supportG, purgeG := 2.5, 1.2
	colourCount := 3
	supportUsed := true
	infillPct := 20.0
	fileID := uuid.New()

	match := production.MatchResult{Design: &production.DesignFacts{
		Material: "PLA", PrintFileID: &fileID,
		BboxXMM: &bx, BboxYMM: &by, BboxZMM: &bz,
		SupportWeightG: &supportG, PurgeWeightG: &purgeG,
		ColourCount: &colourCount, SupportUsed: &supportUsed, InfillPct: &infillPct,
	}}

	var p gen.InsertProductionJobParams
	applyMatch(&p, match, 4)

	if p.BboxXMm == nil || *p.BboxXMm != bx {
		t.Errorf("BboxXMm = %v, want %v (per-unit, not scaled)", p.BboxXMm, bx)
	}
	if p.BboxYMm == nil || *p.BboxYMm != by {
		t.Errorf("BboxYMm = %v, want %v (per-unit, not scaled)", p.BboxYMm, by)
	}
	if p.BboxZMm == nil || *p.BboxZMm != bz {
		t.Errorf("BboxZMm = %v, want %v (per-unit, not scaled)", p.BboxZMm, bz)
	}
	if p.ColourCount == nil || *p.ColourCount != int32(colourCount) {
		t.Errorf("ColourCount = %v, want %d (per-unit, not scaled)", p.ColourCount, colourCount)
	}
	if p.SupportWeightG == nil || *p.SupportWeightG != supportG*4 {
		t.Errorf("SupportWeightG = %v, want %v (scaled by quantity)", p.SupportWeightG, supportG*4)
	}
	if p.PurgeWeightG == nil || *p.PurgeWeightG != purgeG*4 {
		t.Errorf("PurgeWeightG = %v, want %v (scaled by quantity)", p.PurgeWeightG, purgeG*4)
	}
	if p.SupportUsed == nil || *p.SupportUsed != supportUsed {
		t.Errorf("SupportUsed = %v, want %v", p.SupportUsed, supportUsed)
	}
	if p.InfillPct == nil || *p.InfillPct != infillPct {
		t.Errorf("InfillPct = %v, want %v", p.InfillPct, infillPct)
	}
}

// TestSplitProductionJobCopiesGeometrySnapshot is the other half of Phase C:
// a split fragment is the same physical product, just fewer units, so its
// geometry/slice snapshot must survive the split unchanged (not re-derived,
// not re-scaled - same convention as filament_grams_required already uses).
func TestSplitProductionJobCopiesGeometrySnapshot(t *testing.T) {
	bx, supportG, purgeG := 80.0, 3.4, 0.8
	colourCount := int32(2)
	src := gen.ProductionJob{
		ID: uuid.New(), JobNumber: "JOB-00001", Quantity: 10,
		BboxXMm: numeric(bx), BboxYMm: numeric(40), BboxZMm: numeric(15),
		SupportWeightG: numeric(supportG), PurgeWeightG: numeric(purgeG), ColourCount: &colourCount,
	}

	fragment := splitProductionJob(src, 4, "JOB-1000002")

	if fragment.BboxXMm == nil || *fragment.BboxXMm != bx {
		t.Errorf("fragment BboxXMm = %v, want %v", fragment.BboxXMm, bx)
	}
	if fragment.SupportWeightG == nil || *fragment.SupportWeightG != supportG {
		t.Errorf("fragment SupportWeightG = %v, want %v (unchanged, not re-scaled)", fragment.SupportWeightG, supportG)
	}
	if fragment.PurgeWeightG == nil || *fragment.PurgeWeightG != purgeG {
		t.Errorf("fragment PurgeWeightG = %v, want %v (unchanged, not re-scaled)", fragment.PurgeWeightG, purgeG)
	}
	if fragment.ColourCount == nil || *fragment.ColourCount != colourCount {
		t.Errorf("fragment ColourCount = %v, want %v", fragment.ColourCount, colourCount)
	}
}

// TestIntegrationProductionJobGeometrySnapshotRoundTrips confirms the 6 new
// columns actually persist through Postgres and come back correctly shaped
// on the API response - the part the pure applyMatch test above can't cover.
func TestIntegrationProductionJobGeometrySnapshotRoundTrips(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	bx, by, bz := 120.5, 60.25, 30.0
	supportG, purgeG := 4.5, 1.1
	colourCount := int32(2)
	job, err := store.Q.InsertProductionJob(context.Background(), gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: "JOB-GEOM01", Description: "Geometry snapshot test", Quantity: 1,
		Status: production.StatusQueued, AssemblyStatus: production.AssemblyPending,
		QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
		PersonalisationStatus: production.PersonalisationNotRequired, Colours: []byte("[]"),
		BboxXMm: &bx, BboxYMm: &by, BboxZMm: &bz,
		SupportWeightG: &supportG, PurgeWeightG: &purgeG, ColourCount: &colourCount,
	})
	if err != nil {
		t.Fatalf("insert production job: %v", err)
	}

	read := minter.mint(t, []string{"production:read"})
	rr := doJSON(router, http.MethodGet, "/production-jobs/"+job.ID.String(), read, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get job = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp productionJobResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.BboxXMm == nil || *resp.BboxXMm != bx {
		t.Errorf("bbox_x_mm = %v, want %v", resp.BboxXMm, bx)
	}
	if resp.BboxYMm == nil || *resp.BboxYMm != by {
		t.Errorf("bbox_y_mm = %v, want %v", resp.BboxYMm, by)
	}
	if resp.BboxZMm == nil || *resp.BboxZMm != bz {
		t.Errorf("bbox_z_mm = %v, want %v", resp.BboxZMm, bz)
	}
	if resp.SupportWeightG == nil || *resp.SupportWeightG != supportG {
		t.Errorf("support_weight_g = %v, want %v", resp.SupportWeightG, supportG)
	}
	if resp.PurgeWeightG == nil || *resp.PurgeWeightG != purgeG {
		t.Errorf("purge_weight_g = %v, want %v", resp.PurgeWeightG, purgeG)
	}
	if resp.ColourCount == nil || *resp.ColourCount != colourCount {
		t.Errorf("colour_count = %v, want %v", resp.ColourCount, colourCount)
	}
}

// TestIntegrationListProductionJobsPipelineStage seeds a job in each of a
// representative spread of pipeline stages and confirms both the plain list
// and the pipeline_stage-filtered list report the right values/sets.
func TestIntegrationListProductionJobsPipelineStage(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)
	ctx := context.Background()

	baseParams := func(jobNumber string) gen.InsertProductionJobParams {
		return gen.InsertProductionJobParams{
			ID: uuid.New(), JobNumber: jobNumber, Description: "Stage test", Quantity: 1,
			AssemblyStatus: production.AssemblyPending, Colours: []byte("[]"),
		}
	}

	newJob := baseParams("JOB-STAGE-NEW")
	newJob.Status, newJob.QcStatus, newJob.PackagingStatus = production.StatusQueued, production.QcPending, production.PackagingPending
	newJob.PersonalisationStatus = production.PersonalisationPending
	if _, err := store.Q.InsertProductionJob(ctx, newJob); err != nil {
		t.Fatalf("insert NEW job: %v", err)
	}

	waitingJob := baseParams("JOB-STAGE-WAITING")
	waitingJob.Status, waitingJob.QcStatus, waitingJob.PackagingStatus = production.StatusQueued, production.QcPending, production.PackagingPending
	waitingJob.PersonalisationStatus = production.PersonalisationNotRequired
	if _, err := store.Q.InsertProductionJob(ctx, waitingJob); err != nil {
		t.Fatalf("insert WAITING_BATCH job: %v", err)
	}

	reservedBatchID := seedDraftBatch(t, store, "BATCH-STAGE-RESERVED", production.BatchPendingApproval, nil, "H2S")
	// seedDraftBatch already inserts one job (queued, not_required, unheld)
	// on the batch it creates - exactly the RESERVED case, no extra insert needed.

	qcJob := baseParams("JOB-STAGE-QC")
	qcJob.Status, qcJob.QcStatus, qcJob.PackagingStatus = production.StatusCompleted, production.QcPending, production.PackagingPending
	qcJob.PersonalisationStatus = production.PersonalisationNotRequired
	if _, err := store.Q.InsertProductionJob(ctx, qcJob); err != nil {
		t.Fatalf("insert QC job: %v", err)
	}

	packedJob := baseParams("JOB-STAGE-PACKED")
	packedJob.Status, packedJob.QcStatus, packedJob.PackagingStatus = production.StatusCompleted, production.QcPassed, production.PackagingPackaged
	packedJob.PersonalisationStatus = production.PersonalisationNotRequired
	if _, err := store.Q.InsertProductionJob(ctx, packedJob); err != nil {
		t.Fatalf("insert PACKED job: %v", err)
	}

	read := minter.mint(t, []string{"production:read"})
	rr := doJSON(router, http.MethodGet, "/production-jobs", read, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d body=%s", rr.Code, rr.Body.String())
	}
	var all []productionJobResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &all); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	stageByNumber := map[string]string{}
	for _, j := range all {
		stageByNumber[j.JobNumber] = j.PipelineStage
	}
	want := map[string]string{
		"JOB-STAGE-NEW":     production.StageNew,
		"JOB-STAGE-WAITING": production.StageWaitingBatch,
		"JOB-STAGE-QC":      production.StageQC,
		"JOB-STAGE-PACKED":  production.StagePacked,
	}
	for jobNumber, wantStage := range want {
		if got := stageByNumber[jobNumber]; got != wantStage {
			t.Errorf("%s pipeline_stage = %q, want %q", jobNumber, got, wantStage)
		}
	}
	reservedJobs, err := store.Q.ListJobsForBatch(ctx, &reservedBatchID)
	if err != nil || len(reservedJobs) != 1 {
		t.Fatalf("reserved batch jobs = %v, err=%v", reservedJobs, err)
	}
	if got := stageByNumber[reservedJobs[0].JobNumber]; got != production.StageReserved {
		t.Errorf("reserved batch's job pipeline_stage = %q, want %q", got, production.StageReserved)
	}

	rr = doJSON(router, http.MethodGet, "/production-jobs?pipeline_stage=QC", read, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("filtered list = %d body=%s", rr.Code, rr.Body.String())
	}
	var filtered []productionJobResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("unmarshal filtered: %v", err)
	}
	if len(filtered) != 1 || filtered[0].JobNumber != "JOB-STAGE-QC" {
		t.Errorf("pipeline_stage=QC filter returned %+v, want exactly JOB-STAGE-QC", filtered)
	}
}
