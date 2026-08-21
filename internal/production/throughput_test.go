package production

import (
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
)

// sizedJob is smallJob with a caller-chosen footprint, for the cases that turn
// on whether a set physically fits one bed.
func sizedJob(id string, xmm, ymm float64) PlanJob {
	j := smallJob(id, "PLA Basics")
	j.Footprint = bedpack.UnitFootprint{RefID: id, XMM: xmm, YMM: ymm, ZMM: 20}
	return j
}

func batchWith(utilisation float64, minutes int) PlannedBatch {
	m := minutes
	return PlannedBatch{
		Jobs:                  []PlanJob{{ID: "j1", Quantity: 1, CreatedAt: time.Now()}},
		BedUtilisationPercent: utilisation,
		TotalPrintTimeMinutes: &m,
	}
}

// TestThroughputScoreRewardsUtilisationPerHour pins the distinction the whole
// term exists for: "good bed utilisation" and "good production efficiency" are
// not the same measurement. A 96%/7h plate is fuller than an 86%/3h plate and
// worth substantially less per machine-hour.
func TestThroughputScoreRewardsUtilisationPerHour(t *testing.T) {
	full := batchWith(96, 7*60)  // 13.7 utilisation-points per hour
	quick := batchWith(86, 3*60) // 28.7 per hour

	if throughputScore(quick) <= throughputScore(full) {
		t.Errorf("throughput: 86%%/3h scored %.1f, 96%%/7h scored %.1f; the leaner plate delivers twice the utilisation per machine-hour",
			throughputScore(quick), throughputScore(full))
	}
}

// TestThroughputScoreHandlesAnUnknownTime: an unestimated batch must score zero
// rather than infinity. Dividing by a zero print time would make every
// unmeasured batch look infinitely efficient and displace every measured one.
func TestThroughputScoreHandlesAnUnknownTime(t *testing.T) {
	var noTime PlannedBatch
	noTime.BedUtilisationPercent = 90
	if got := throughputScore(noTime); got != 0 {
		t.Errorf("throughputScore with no estimate = %v, want 0", got)
	}

	zero := batchWith(90, 0)
	if got := throughputScore(zero); got != 0 {
		t.Errorf("throughputScore with a zero estimate = %v, want 0", got)
	}
}

// TestThroughputScoreIsCapped keeps one extraordinary batch from dominating the
// ranking outright - the term is a preference, not an override.
func TestThroughputScoreIsCapped(t *testing.T) {
	if got := throughputScore(batchWith(100, 5)); got != 100 {
		t.Errorf("throughputScore(100%% in 5 min) = %v, want it capped at 100", got)
	}
}

// TestEvaluateBatchMatchesThePlannersOwnMeasurement is what makes the
// replan-stability threshold meaningful: an existing Draft has to be scored the
// same way as the proposal that would replace it. Comparing a stored row's
// utilisation against a freshly-packed candidate would compare two different
// measurements and the threshold would mean nothing.
func TestEvaluateBatchMatchesThePlannersOwnMeasurement(t *testing.T) {
	jobs := []PlanJob{
		sizedJob("a", 100, 100),
		sizedJob("b", 100, 100),
	}

	planned, _, _ := Plan(jobs, testNow, alwaysBatchGate)
	if len(planned) != 1 {
		t.Fatalf("Plan produced %d batches, want 1 for two small compatible jobs", len(planned))
	}

	rebuilt, ok := EvaluateBatch(jobs)
	if !ok {
		t.Fatal("EvaluateBatch rejected a set the planner itself batched onto one bed")
	}
	if rebuilt.BedUtilisationPercent != planned[0].BedUtilisationPercent {
		t.Errorf("EvaluateBatch utilisation = %v, Plan's = %v; they must measure identically",
			rebuilt.BedUtilisationPercent, planned[0].BedUtilisationPercent)
	}
}

// TestEvaluateBatchRejectsWhatCannotFit: a Draft whose jobs no longer fit one
// bed cannot be used as a baseline, and the caller must be told so rather than
// handed a silently-wrong score.
func TestEvaluateBatchRejectsWhatCannotFit(t *testing.T) {
	if _, ok := EvaluateBatch(nil); ok {
		t.Error("EvaluateBatch(nil) reported ok; an empty set is not a batch")
	}
	oversized := []PlanJob{sizedJob("huge", 1000, 1000)}
	if _, ok := EvaluateBatch(oversized); ok {
		t.Error("EvaluateBatch accepted a part larger than the bed")
	}
}
