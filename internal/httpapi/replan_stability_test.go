package httpapi

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// stabilityJob is one small, compatible, immediately-batchable job.
func stabilityJob(id string, xmm, ymm float64) production.PlanJob {
	return production.PlanJob{
		ID: id, JobNumber: "JOB-" + id, Material: "PLA Basics", Quantity: 1,
		CreatedAt: time.Now().Add(-time.Minute),
		Footprint: bedpack.UnitFootprint{RefID: id, XMM: xmm, YMM: ymm, ZMM: 20},
	}
}

func stabilityServer(minImprovementPercent float64) *Server {
	return &Server{cfg: config.Settings{
		Environment:                      "development",
		BatchReplanMinImprovementPercent: minImprovementPercent,
	}}
}

// TestWorthReplanningProtectsDraftsFromChurn is the plan-stability rule.
//
// The planner re-runs every couple of minutes. Without a floor on what counts
// as an improvement, any reshuffle scoring 0.01 better dissolves every Draft
// and mints replacements: batch numbers change under the operator, cached
// preview plates are rebuilt (an STL download and mesh merge per batch), and
// the board rewrites itself for no production benefit.
func TestWorthReplanningProtectsDraftsFromChurn(t *testing.T) {
	a, b := stabilityJob("a", 100, 100), stabilityJob("b", 100, 100)
	pool := map[string]production.PlanJob{"a": a, "b": b}
	draftID := uuid.New()
	drafts := map[uuid.UUID][]string{draftID: {"a", "b"}}

	same, ok := production.EvaluateBatch([]production.PlanJob{a, b})
	if !ok {
		t.Fatal("fixture does not pack onto one bed")
	}
	srv := stabilityServer(2)

	t.Run("an identical plan is not worth applying", func(t *testing.T) {
		apply, why := srv.worthReplanning(
			batchStrategyPlanner,
			[]production.PlannedBatch{same}, pool, drafts, time.Now(), production.BatchGate{})
		if apply {
			t.Errorf("replan applied for an identical plan (%s); this is pure churn", why)
		}
	})

	t.Run("a plan batching more jobs always applies", func(t *testing.T) {
		// Job c arrives and joins the bed. The score delta may be small, but
		// placing work nothing was placing before is real progress - this is
		// the case that lets a Draft absorb a new compatible arrival.
		c := stabilityJob("c", 100, 100)
		grown, ok := production.EvaluateBatch([]production.PlanJob{a, b, c})
		if !ok {
			t.Fatal("three small jobs should still pack onto one bed")
		}
		withC := map[string]production.PlanJob{"a": a, "b": b, "c": c}

		apply, why := srv.worthReplanning(
			batchStrategyPlanner,
			[]production.PlannedBatch{grown}, withC, drafts, time.Now(), production.BatchGate{})
		if !apply {
			t.Errorf("replan skipped a plan that batches an extra job (%s); a Draft must be able to grow", why)
		}
	})

	t.Run("a threshold of zero lets any improvement through", func(t *testing.T) {
		// The floor is configurable, and disabling it must restore the old
		// always-replan behaviour rather than wedging the planner.
		open := stabilityServer(0)
		apply, _ := open.worthReplanning(
			batchStrategyPlanner,
			[]production.PlannedBatch{same}, pool, drafts, time.Now(), production.BatchGate{})
		if !apply {
			t.Error("with a 0% threshold an equal-scoring plan should still apply")
		}
	})

	t.Run("no drafts means nothing to protect", func(t *testing.T) {
		apply, _ := srv.worthReplanning(
			batchStrategyPlanner,
			[]production.PlannedBatch{same}, pool, nil, time.Now(), production.BatchGate{})
		if !apply {
			t.Error("replan skipped with no existing drafts; there is nothing to churn")
		}
	})

	t.Run("a draft whose jobs left the pool is not used as a baseline", func(t *testing.T) {
		// Approved, held or flagged since the pool was read. It cannot be
		// rebuilt or compared, so the comparison is unsafe and the replan must
		// proceed rather than silently scoring against a partial batch.
		partial := map[string]production.PlanJob{"a": a}
		apply, why := srv.worthReplanning(
			batchStrategyPlanner,
			[]production.PlannedBatch{same}, partial, drafts, time.Now(), production.BatchGate{})
		if !apply {
			t.Errorf("replan skipped on an unscoreable draft (%s); it must fail open", why)
		}
	})
}
