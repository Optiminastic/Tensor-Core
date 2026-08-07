package production

import (
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
)

// testNow is a fixed clock for every test below that isn't specifically
// about the hold/override gate (see TestPlanHolds*/TestPlanOverrides*) - so
// they stay deterministic and unaffected by wall-clock time.
var testNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// alwaysBatchGate has MaxWait=0, so any job with a non-zero, non-future
// CreatedAt (smallJob always sets one) immediately qualifies for the
// max-wait override regardless of utilisation - used by tests exercising
// grouping/packing logic, not the hold-below-target gate itself.
var alwaysBatchGate = BatchGate{}

func smallJob(id, material string) PlanJob {
	return PlanJob{
		ID: id, JobNumber: "JOB-" + id, Material: material, Quantity: 1,
		CreatedAt: testNow.Add(-time.Minute),
		Footprint: bedpack.UnitFootprint{RefID: id, XMM: 50, YMM: 50, ZMM: 20},
	}
}

// TestKeyForDiscriminatesOnSupportAndInfill is the bug-fix half of Phase C:
// before applyMatch snapshotted support_used/infill_pct onto production_jobs,
// every job had the same false/0 value on both axes, so keyFor's supportUsed
// and infillBucket fields never actually varied - a support-required job and
// a plain one always shared a compatibility bucket. With real values flowing
// in now, they must not.
func TestKeyForDiscriminatesOnSupportAndInfill(t *testing.T) {
	base := smallJob("a", "PLA")
	base.SupportUsed = false
	base.InfillPct = 15

	withSupport := base
	withSupport.SupportUsed = true

	if keyFor(base) == keyFor(withSupport) {
		t.Error("a support-required job must not share a groupKey with an otherwise-identical non-support job")
	}

	differentInfill := base
	differentInfill.InfillPct = 40

	if keyFor(base) == keyFor(differentInfill) {
		t.Error("jobs with meaningfully different infill% must not share a groupKey")
	}

	sameBucket := base
	sameBucket.InfillPct = 16 // rounds to the same 5%-bucket as 15

	if keyFor(base) != keyFor(sameBucket) {
		t.Error("infill within the same rounded bucket should still share a groupKey")
	}
}

func TestPlanNoFootprintIsUnbatchable(t *testing.T) {
	j := smallJob("a", "PLA")
	j.Footprint = bedpack.UnitFootprint{}
	batches, unb, _ := Plan([]PlanJob{j}, testNow, alwaysBatchGate)
	if len(batches) != 0 || len(unb) != 1 {
		t.Fatalf("batches=%d unbatchable=%d, want 0/1", len(batches), len(unb))
	}
	if unb[0].Reason != "No print file with measurable STL dimensions uploaded yet." {
		t.Errorf("reason = %q", unb[0].Reason)
	}
}

func TestPlanSameKeyShareOneBatch(t *testing.T) {
	batches, unb, _ := Plan([]PlanJob{smallJob("a", "PLA"), smallJob("b", "PLA")}, testNow, alwaysBatchGate)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1 (both small, same key)", len(batches))
	}
	if batches[0].UnitsPerBed != 2 {
		t.Errorf("units per bed = %d, want 2", batches[0].UnitsPerBed)
	}
}

func TestPlanDifferentMaterialSplits(t *testing.T) {
	batches, _, _ := Plan([]PlanJob{smallJob("a", "PLA"), smallJob("b", "PETG")}, testNow, alwaysBatchGate)
	if len(batches) != 2 {
		t.Errorf("batches = %d, want 2 (different material key)", len(batches))
	}
}

func TestPlanDueDateClusteringSplits(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	far := base.Add(10 * 24 * time.Hour)
	a := smallJob("a", "PLA")
	a.DueDate = &base
	b := smallJob("b", "PLA")
	b.DueDate = &far
	batches, _, _ := Plan([]PlanJob{a, b}, testNow, alwaysBatchGate)
	if len(batches) != 2 {
		t.Errorf("batches = %d, want 2 (due dates 10 days apart)", len(batches))
	}
}

func TestPlanOversizedJobIsUnbatchable(t *testing.T) {
	// Two near-max units of one job cannot share a bed, and jobs never split, so the
	// job is unbatchable.
	j := PlanJob{
		ID: "big", JobNumber: "JOB-big", Material: "PLA", Quantity: 2,
		Footprint: bedpack.UnitFootprint{RefID: "big", XMM: 290, YMM: 310, ZMM: 20},
	}
	batches, unb, _ := Plan([]PlanJob{j}, testNow, alwaysBatchGate)
	if len(batches) != 0 || len(unb) != 1 {
		t.Fatalf("batches=%d unbatchable=%d, want 0/1", len(batches), len(unb))
	}
	if unb[0].Reason != "Exceeds the print bed's capacity even on its own." {
		t.Errorf("reason = %q", unb[0].Reason)
	}
}

func TestGroupingColourOrderIndependent(t *testing.T) {
	a := smallJob("a", "PLA")
	a.Colours = []string{"Red", "Yellow"}
	b := smallJob("b", "PLA")
	b.Colours = []string{"Yellow", "Red"} // same set, different order
	batches, unb, _ := Plan([]PlanJob{a, b}, testNow, alwaysBatchGate)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1 (same colour set regardless of order)", len(batches))
	}
}

func TestGroupingFourPlusColoursNeverMerge(t *testing.T) {
	// Per the "4+ colours stays separate" rule, jobs using more than
	// maxGroupColours colours never merge - not even with an identical set.
	a := smallJob("a", "PLA")
	a.Colours = []string{"Red", "Yellow", "Black", "White"}
	b := smallJob("b", "PLA")
	b.Colours = []string{"Red", "Yellow", "Black", "White"}
	batches, unb, _ := Plan([]PlanJob{a, b}, testNow, alwaysBatchGate)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(batches) != 2 {
		t.Errorf("batches = %d, want 2 (4+ colour jobs never merge, even with each other)", len(batches))
	}
}

func TestGroupingThreeColoursExceedCapStaySeparate(t *testing.T) {
	// maxGroupColours is 2 - a combined union of 3 (even an identical set
	// shared by both jobs) now exceeds the cap, so these must land in
	// separate batches.
	a := smallJob("a", "PLA")
	a.Colours = []string{"Red", "Yellow", "Black"}
	b := smallJob("b", "PLA")
	b.Colours = []string{"Black", "Red", "Yellow"}
	batches, _, _ := Plan([]PlanJob{a, b}, testNow, alwaysBatchGate)
	if len(batches) != 2 {
		t.Errorf("batches = %d, want 2 (3 colours exceeds the 2-colour cap)", len(batches))
	}
}

func TestGroupingColourUnionAtCapSharesABatch(t *testing.T) {
	// Job A{Red,White} + Job B{Red} + Job C{White,Red}: combined union is
	// exactly {Red, White} = 2 colours = maxGroupColours, so all three must
	// share one batch - the cap is inclusive (<=), not exclusive.
	a := smallJob("a", "PLA")
	a.Colours = []string{"Red", "White"}
	b := smallJob("b", "PLA")
	b.Colours = []string{"Red"}
	c := smallJob("c", "PLA")
	c.Colours = []string{"White", "Red"}
	batches, unb, _ := Plan([]PlanJob{a, b, c}, testNow, alwaysBatchGate)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1 (union of 2 colours is exactly at the cap)", len(batches))
	}
	if got := colourUnionSize(batches[0].Jobs); got != 2 {
		t.Errorf("colour union = %d, want 2 (Red+White)", got)
	}
}

func TestGroupingSingleJobOverColourCapStillBatchesAlone(t *testing.T) {
	// A job's own colour count never blocks it batching alone, regardless of
	// the cap - the cap only ever blocks COMBINING jobs.
	a := smallJob("a", "PLA")
	a.Colours = []string{"Red", "Yellow", "Black"}
	batches, unb, _ := Plan([]PlanJob{a}, testNow, alwaysBatchGate)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1 (a job's own colour count never blocks it batching alone)", len(batches))
	}
}

func TestGroupingDifferentColoursWithinCapShareABatch(t *testing.T) {
	// "a" and "b" use entirely different colours - under the old strict
	// equality grouping these would never have shared a bed. Now colour
	// compatibility is a live cap: their combined union (Red, Blue) is only
	// 2 colours, within maxGroupColours, so they should share a batch.
	a := smallJob("a", "PLA")
	a.Colours = []string{"Red"}
	b := smallJob("b", "PLA")
	b.Colours = []string{"Blue"}
	batches, unb, _ := Plan([]PlanJob{a, b}, testNow, alwaysBatchGate)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1 (different colours, but union stays within the cap)", len(batches))
	}
	if got := colourUnionSize(batches[0].Jobs); got != 2 {
		t.Errorf("colour union = %d, want 2 (Red+Blue)", got)
	}
}

func TestGroupingColourUnionOverCapStaysSeparate(t *testing.T) {
	// "a" (Red, Blue) and "b" (Green, Black) each use 2 colours alone (fine),
	// but combined they'd need 4 distinct colours on one bed - over
	// maxGroupColours - so they must land in separate batches even though
	// nothing else (material/size) would stop them sharing one.
	a := smallJob("a", "PLA")
	a.Colours = []string{"Red", "Blue"}
	b := smallJob("b", "PLA")
	b.Colours = []string{"Green", "Black"}
	batches, unb, _ := Plan([]PlanJob{a, b}, testNow, alwaysBatchGate)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(batches) != 2 {
		t.Errorf("batches = %d, want 2 (combined colour union of 4 exceeds the cap)", len(batches))
	}
}

func TestGroupingPriorityTierSplits(t *testing.T) {
	urgent := smallJob("a", "PLA")
	urgent.Priority = urgentPriority
	normal := smallJob("b", "PLA")
	normal.Priority = urgentPriority + 5
	batches, _, _ := Plan([]PlanJob{urgent, normal}, testNow, alwaysBatchGate)
	if len(batches) != 2 {
		t.Errorf("batches = %d, want 2 (urgent jobs never share a bucket with routine ones)", len(batches))
	}
}

func TestLocalSearchNeverRegressesGreedyPartition(t *testing.T) {
	// Same priority tier throughout (so grouping keeps all four jobs in one
	// cluster - priority tier is itself a grouping key, tested separately
	// above) but varied footprints/priorities within that tier, so the
	// strategies can genuinely disagree on the best split. bestPartition's
	// chosen (post-local-search) partition must never score worse than any
	// single strategy's own untouched greedy result.
	mkJob := func(id string, x, y float64, priority int) PlanJob {
		j := smallJob(id, "PLA")
		j.Footprint = bedpack.UnitFootprint{RefID: id, XMM: x, YMM: y, ZMM: 20}
		j.Priority = priority
		return j
	}
	jobs := []PlanJob{
		mkJob("a", 100, 100, 5), mkJob("b", 100, 100, 5),
		mkJob("c", 100, 100, 2), mkJob("d", 100, 200, 2),
	}
	batches, unb, _ := Plan(jobs, testNow, alwaysBatchGate)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	got := partitionScore(batches)
	for _, strategy := range packingStrategies {
		naive, _ := packJobs(orderJobs(jobs, strategy), strategy, DefaultNester)
		if naiveScore := partitionScore(naive); got < naiveScore {
			t.Errorf("optimizer score %.2f worse than plain %q strategy %.2f", got, strategy, naiveScore)
		}
	}
}

func TestPlanEffectiveTimePerUnit(t *testing.T) {
	mins := 120
	a := smallJob("a", "PLA")
	a.EstimatedMinutes = &mins
	a.Quantity = 2
	batches, _, _ := Plan([]PlanJob{a}, testNow, alwaysBatchGate)
	if len(batches) != 1 {
		t.Fatalf("want 1 batch, got %d", len(batches))
	}
	b := batches[0]
	if b.TotalPrintTimeMinutes == nil || *b.TotalPrintTimeMinutes != 120 {
		t.Errorf("total time = %v, want 120", b.TotalPrintTimeMinutes)
	}
	if b.EffectiveTimePerUnitMinutes == nil || *b.EffectiveTimePerUnitMinutes != 60 {
		t.Errorf("effective/unit = %v, want 60", b.EffectiveTimePerUnitMinutes)
	}
}

// realisticGate matches internal/config/config.go's defaults (BATCH_MAX_WAIT_HOURS=4,
// BATCH_DUE_SOON_HOURS=24) - used by the gate-specific tests below, unlike
// alwaysBatchGate which every other test above uses to make the new gate a
// no-op for logic it isn't testing.
var realisticGate = BatchGate{MaxWait: 4 * time.Hour, DueSoonWindow: 24 * time.Hour}

func TestPlanHoldsBelowTargetWithoutOverride(t *testing.T) {
	// Low-fill (50x50 on a 330x320 bed), routine priority, no due date, and
	// recently queued - none of the overrides apply, so this must be held,
	// not created as an under-filled batch.
	j := PlanJob{
		ID: "a", JobNumber: "JOB-a", Material: "PLA", Quantity: 1,
		Priority:  urgentPriority + 5,
		CreatedAt: testNow.Add(-time.Minute),
		Footprint: bedpack.UnitFootprint{XMM: 50, YMM: 50, ZMM: 20},
	}
	batches, unb, held := Plan([]PlanJob{j}, testNow, realisticGate)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(batches) != 0 {
		t.Fatalf("batches = %d, want 0 (held, not created)", len(batches))
	}
	if len(held) != 1 {
		t.Fatalf("held = %d, want 1", len(held))
	}
	if held[0].BedUtilisationPercent >= TargetBedUtilisationPercent {
		t.Errorf("held utilisation = %.2f, want < %.0f (that's the whole point of holding it)",
			held[0].BedUtilisationPercent, TargetBedUtilisationPercent)
	}
}

func TestPlanOverridesForUrgentPriority(t *testing.T) {
	j := PlanJob{
		ID: "a", JobNumber: "JOB-a", Material: "PLA", Quantity: 1,
		Priority:  urgentPriority,
		CreatedAt: testNow.Add(-time.Minute),
		Footprint: bedpack.UnitFootprint{XMM: 50, YMM: 50, ZMM: 20},
	}
	batches, unb, held := Plan([]PlanJob{j}, testNow, realisticGate)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(held) != 0 {
		t.Fatalf("held = %d, want 0 (urgent priority should override)", len(held))
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
}

func TestPlanOverridesForDueSoon(t *testing.T) {
	due := testNow.Add(time.Hour)
	j := PlanJob{
		ID: "a", JobNumber: "JOB-a", Material: "PLA", Quantity: 1,
		Priority:  urgentPriority + 5,
		DueDate:   &due,
		CreatedAt: testNow.Add(-time.Minute),
		Footprint: bedpack.UnitFootprint{XMM: 50, YMM: 50, ZMM: 20},
	}
	batches, unb, held := Plan([]PlanJob{j}, testNow, realisticGate)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(held) != 0 {
		t.Fatalf("held = %d, want 0 (due soon should override)", len(held))
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
}

func TestPlanOverridesForMaxWait(t *testing.T) {
	j := PlanJob{
		ID: "a", JobNumber: "JOB-a", Material: "PLA", Quantity: 1,
		Priority:  urgentPriority + 5,
		CreatedAt: testNow.Add(-5 * time.Hour),
		Footprint: bedpack.UnitFootprint{XMM: 50, YMM: 50, ZMM: 20},
	}
	batches, unb, held := Plan([]PlanJob{j}, testNow, realisticGate)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(held) != 0 {
		t.Fatalf("held = %d, want 0 (max wait exceeded should override)", len(held))
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
}

// realisticAgingGate extends realisticGate with aging at the config
// defaults (BATCH_AGING_WINDOW_MINUTES=60, BATCH_AGING_FLOOR_PERCENT=73).
var realisticAgingGate = BatchGate{
	MaxWait: 4 * time.Hour, DueSoonWindow: 24 * time.Hour,
	AgingWindow: 60 * time.Minute, AgingFloorPercent: 73,
}

// agingJob is a single square job at 75.84% bed utilisation (283x283 on the
// 330x320 bed, 80089mm^2 / 105600mm^2) - comfortably above the 73% aging
// floor but below the 80% target, so the decay curve's crossing point falls
// at a predictable, non-edge-case wait: threshold(wait) = 80 -
// 7*(wait/60min) crosses 75.84 at wait ~= 35.66min.
func agingJob(wait time.Duration) PlanJob {
	return PlanJob{
		ID: "a", JobNumber: "JOB-a", Material: "PLA", Quantity: 1,
		Priority:  urgentPriority + 5,
		CreatedAt: testNow.Add(-wait),
		Footprint: bedpack.UnitFootprint{XMM: 283, YMM: 283, ZMM: 20},
	}
}

func TestShouldCreateBatchAgingHoldsBeforeThresholdCrosses(t *testing.T) {
	// At 20min the bar has only relaxed to ~77.67%, still above this job's
	// 75.84% - must stay held.
	batches, unb, held := Plan([]PlanJob{agingJob(20 * time.Minute)}, testNow, realisticAgingGate)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(batches) != 0 {
		t.Fatalf("batches = %d, want 0 (aging bar hasn't relaxed far enough yet)", len(batches))
	}
	if len(held) != 1 {
		t.Fatalf("held = %d, want 1", len(held))
	}
}

func TestShouldCreateBatchAgingAcceptsOnceThresholdCrosses(t *testing.T) {
	// At 40min the bar has relaxed to ~75.33%, at or below this job's
	// 75.84% - must be created despite being under the 80% target.
	batches, unb, held := Plan([]PlanJob{agingJob(40 * time.Minute)}, testNow, realisticAgingGate)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(held) != 0 {
		t.Fatalf("held = %d, want 0 (aging should have accepted this by 40min)", len(held))
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
}

func TestShouldCreateBatchAgingFloorsAndMaxWaitStillBackstops(t *testing.T) {
	// A tiny, isolated job (2.37% utilisation) sits well below even the 73%
	// aging floor - aging alone can never clear it. It must stay held past
	// the aging window (floor reached, still far too low) and only get
	// created once MaxWait's separate, unconditional override fires.
	tiny := func(wait time.Duration) PlanJob {
		return PlanJob{
			ID: "a", JobNumber: "JOB-a", Material: "PLA", Quantity: 1,
			Priority:  urgentPriority + 5,
			CreatedAt: testNow.Add(-wait),
			Footprint: bedpack.UnitFootprint{XMM: 50, YMM: 50, ZMM: 20},
		}
	}

	_, _, held := Plan([]PlanJob{tiny(2 * time.Hour)}, testNow, realisticAgingGate)
	if len(held) != 1 {
		t.Fatalf("held at 2h = %d, want 1 (floor alone can't clear a 2.37%% batch)", len(held))
	}

	batches, unb, held := Plan([]PlanJob{tiny(5 * time.Hour)}, testNow, realisticAgingGate)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(held) != 0 {
		t.Fatalf("held at 5h = %d, want 0 (MaxWait must still force it)", len(held))
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
}

func TestShouldCreateBatchAgingDisabledWhenWindowZero(t *testing.T) {
	// realisticGate has AgingWindow=0 (unset) - the 75.84% job from
	// agingJob must stay held indefinitely (until MaxWait), matching
	// today's pre-aging behaviour exactly, since effectiveUtilisationThreshold
	// returns the unchanged 80% target when aging is disabled.
	_, _, held := Plan([]PlanJob{agingJob(90 * time.Minute)}, testNow, realisticGate)
	if len(held) != 1 {
		t.Fatalf("held = %d, want 1 (aging disabled, bar stays at 80%% target)", len(held))
	}
}

func TestEffectiveUtilisationThresholdDecayMath(t *testing.T) {
	gate := BatchGate{AgingWindow: 60 * time.Minute, AgingFloorPercent: 73}
	cases := []struct {
		name string
		wait time.Duration
		want float64
	}{
		{"zero wait is the full target", 0, TargetBedUtilisationPercent},
		{"halfway decays halfway to the floor", 30 * time.Minute, 76.5},
		{"at the window is exactly the floor", 60 * time.Minute, 73},
		{"past the window stays at the floor", 90 * time.Minute, 73},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := effectiveUtilisationThreshold(c.wait, gate); got != c.want {
				t.Errorf("effectiveUtilisationThreshold(%v) = %v, want %v", c.wait, got, c.want)
			}
		})
	}
}

func TestEffectiveUtilisationThresholdDisabledByDefault(t *testing.T) {
	if got := effectiveUtilisationThreshold(10*time.Hour, BatchGate{}); got != TargetBedUtilisationPercent {
		t.Errorf("threshold with AgingWindow=0 = %v, want unchanged target %v", got, TargetBedUtilisationPercent)
	}
}

func TestFillBedSingleLargeJobClearsTarget(t *testing.T) {
	// Sized to exactly fill the margin-inset usable envelope (310x300, i.e.
	// the 330x320 bed minus the 10mm edge margin on every side) on its own -
	// confirms a single job with enough volume clears the 80% target even
	// measured against the full nominal bed area, despite the margin eating
	// into what's actually placeable.
	big := PlanJob{ID: "big", JobNumber: "JOB-big", Quantity: 1,
		Footprint: bedpack.UnitFootprint{XMM: 300, YMM: 290, ZMM: 20}}

	batches, unb := packJobs([]PlanJob{big}, strategyFCFS, DefaultNester)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
	if batches[0].BedUtilisationPercent < TargetBedUtilisationPercent {
		t.Errorf("utilisation = %.2f, want >= %.0f", batches[0].BedUtilisationPercent, TargetBedUtilisationPercent)
	}
}

func TestFillBedPullsInLaterSmallerJob(t *testing.T) {
	// "big" alone leaves a 100x300 leftover strip. "medium" (next in FCFS
	// order) does not fit that strip, but "small" (further down the queue)
	// does. A packer that flushes on the first miss would close the bed
	// after "big" alone at ~54.9% utilisation; fillBed's lookahead must
	// instead skip "medium" and pull "small" in, raising it to ~72.6% -
	// "medium" is left to its own bed since nothing else fits alongside it.
	// (Not quite the 80% target with only these two parts and the 10mm edge
	// margin factored in - see TestFillBedSingleLargeJobClearsTarget for
	// that; this test is specifically about the lookahead-pull mechanism.)
	big := PlanJob{ID: "big", JobNumber: "JOB-big", Quantity: 1,
		Footprint: bedpack.UnitFootprint{XMM: 200, YMM: 290, ZMM: 20}}
	medium := PlanJob{ID: "medium", JobNumber: "JOB-medium", Quantity: 1,
		Footprint: bedpack.UnitFootprint{XMM: 150, YMM: 150, ZMM: 20}}
	small := PlanJob{ID: "small", JobNumber: "JOB-small", Quantity: 1,
		Footprint: bedpack.UnitFootprint{XMM: 85, YMM: 220, ZMM: 20}}

	batches, unb := packJobs([]PlanJob{big, medium, small}, strategyFCFS, DefaultNester)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(batches) != 2 {
		t.Fatalf("batches = %d, want 2 (big+small sharing one bed, medium alone)", len(batches))
	}

	firstBed := jobIDSet(batches[0].Jobs)
	if !firstBed["big"] || !firstBed["small"] {
		t.Fatalf("first bed jobs = %v, want big+small pulled together", firstBed)
	}
	const bigAloneUtilisation = 54.92
	if batches[0].BedUtilisationPercent <= bigAloneUtilisation {
		t.Errorf("first bed utilisation = %.2f, want meaningfully above big-alone's %.2f",
			batches[0].BedUtilisationPercent, bigAloneUtilisation)
	}

	secondBed := jobIDSet(batches[1].Jobs)
	if !secondBed["medium"] || len(secondBed) != 1 {
		t.Errorf("second bed jobs = %v, want medium alone", secondBed)
	}
}

func TestPackJobsSplitsLargeQuantityAcrossMultipleBatches(t *testing.T) {
	// 100x100 units on the 310x300 usable envelope fit only a handful per
	// bed - quantity 20 must therefore span multiple batches instead of
	// being reported unbatchable, and every unit must be accounted for.
	big := PlanJob{ID: "big", JobNumber: "JOB-big", Quantity: 20,
		Footprint: bedpack.UnitFootprint{XMM: 100, YMM: 100, ZMM: 20}}

	batches, unb := packJobs([]PlanJob{big}, strategyFCFS, DefaultNester)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(batches) < 2 {
		t.Fatalf("batches = %d, want >1 (quantity 20 shouldn't fit on one bed)", len(batches))
	}
	total := 0
	for _, b := range batches {
		if len(b.Jobs) != 1 || b.Jobs[0].ID != "big" {
			t.Fatalf("batch jobs = %+v, want each batch to be just a fragment of big", b.Jobs)
		}
		if b.Jobs[0].Quantity <= 0 {
			t.Errorf("fragment quantity = %d, want > 0", b.Jobs[0].Quantity)
		}
		total += b.Jobs[0].Quantity
	}
	if total != 20 {
		t.Errorf("total split quantity across batches = %d, want 20 (the original quantity)", total)
	}
}

func TestPackJobsSplittingComposesWithOtherJobs(t *testing.T) {
	// Splitting a large job must not lose or duplicate units, and an
	// unrelated smaller compatible job present in the same run must still
	// end up placed somewhere - fillBed's own greedy order decides exactly
	// which bed small lands on (it may grab an early bed before big ever
	// needs to split, which is correct, not a regression), but it must
	// never go missing.
	big := PlanJob{ID: "big", JobNumber: "JOB-big", Quantity: 20,
		Footprint: bedpack.UnitFootprint{XMM: 100, YMM: 100, ZMM: 20}}
	small := PlanJob{ID: "small", JobNumber: "JOB-small", Quantity: 1,
		Footprint: bedpack.UnitFootprint{XMM: 20, YMM: 20, ZMM: 20}}

	batches, unb := packJobs([]PlanJob{big, small}, strategyFCFS, DefaultNester)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}

	bigTotal, smallCount := 0, 0
	for _, b := range batches {
		for _, j := range b.Jobs {
			switch j.ID {
			case "big":
				bigTotal += j.Quantity
			case "small":
				smallCount++
			}
		}
	}
	if bigTotal != 20 {
		t.Errorf("big's total split quantity = %d, want 20", bigTotal)
	}
	if smallCount != 1 {
		t.Errorf("small appeared in %d batches, want exactly 1 (placed once, never lost or duplicated)", smallCount)
	}
}

func TestPackJobsGenuinelyOversizedStaysUnbatchableEvenWithQuantity(t *testing.T) {
	// A job whose single-unit footprint exceeds the bed outright must still
	// be reported unbatchable, quantity > 1 notwithstanding - splitting only
	// ever helps a quantity problem, never a genuine geometry problem.
	big := PlanJob{ID: "big", JobNumber: "JOB-big", Quantity: 5,
		Footprint: bedpack.UnitFootprint{XMM: 400, YMM: 400, ZMM: 20}}

	batches, unb := packJobs([]PlanJob{big}, strategyFCFS, DefaultNester)
	if len(batches) != 0 {
		t.Fatalf("batches = %d, want 0", len(batches))
	}
	if len(unb) != 1 || unb[0].JobID != "big" {
		t.Fatalf("unbatchable = %+v, want big reported unbatchable", unb)
	}
}

func TestSplitJobToFitFindsLargestFittingQuantity(t *testing.T) {
	j := PlanJob{ID: "j", Quantity: 20, Footprint: bedpack.UnitFootprint{XMM: 100, YMM: 100, ZMM: 20}}
	fragment, leftover, ok := splitJobToFit(j, DefaultNester)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if fragment.Quantity+leftover.Quantity != 20 {
		t.Errorf("fragment+leftover = %d+%d, want sum 20", fragment.Quantity, leftover.Quantity)
	}
	if fragment.Quantity <= 0 || fragment.Quantity >= 20 {
		t.Errorf("fragment.Quantity = %d, want strictly between 0 and 20", fragment.Quantity)
	}
}

func TestSplitJobToFitFalseForQuantityOne(t *testing.T) {
	j := PlanJob{ID: "j", Quantity: 1, Footprint: bedpack.UnitFootprint{XMM: 100, YMM: 100, ZMM: 20}}
	if _, _, ok := splitJobToFit(j, DefaultNester); ok {
		t.Errorf("ok = true for quantity 1, want false (nothing left to split)")
	}
}

func TestSplitJobToFitFalseWhenNotEvenOneUnitFits(t *testing.T) {
	j := PlanJob{ID: "j", Quantity: 5, Footprint: bedpack.UnitFootprint{XMM: 400, YMM: 400, ZMM: 20}}
	if _, _, ok := splitJobToFit(j, DefaultNester); ok {
		t.Errorf("ok = true for oversized geometry, want false")
	}
}

func TestFillBedPrefersSameCustomerWhenSpaceLimited(t *testing.T) {
	// anchor leaves a strip only one more 85x220 unit fits into - "other"
	// (different customer) appears first in scan order, "same" (anchor's
	// customer) appears second. Without the affinity preference, first-fit
	// order would pick "other"; the preference must pick "same" instead.
	cust1, cust2 := int64(100), int64(200)
	anchor := smallJob("anchor", "PLA")
	anchor.ShopifyCustomerID = &cust1
	anchor.Footprint = bedpack.UnitFootprint{XMM: 200, YMM: 290, ZMM: 20}

	other := smallJob("other", "PLA")
	other.ShopifyCustomerID = &cust2
	other.Footprint = bedpack.UnitFootprint{XMM: 85, YMM: 220, ZMM: 20}

	same := smallJob("same", "PLA")
	same.ShopifyCustomerID = &cust1
	same.Footprint = bedpack.UnitFootprint{XMM: 85, YMM: 220, ZMM: 20}

	bedJobs, _, _ := fillBed([]PlanJob{anchor, other, same}, DefaultNester)
	ids := jobIDSet(bedJobs)
	if !ids["anchor"] {
		t.Fatalf("expected anchor placed, bed = %+v", bedJobs)
	}
	if !ids["same"] {
		t.Errorf("expected the same-customer job preferred over other, bed = %+v", bedJobs)
	}
	if ids["other"] {
		t.Errorf("expected other (different customer) left for a later bed, bed = %+v", bedJobs)
	}
}

func TestFillBedAffinityNeverOverridesPhysicalFit(t *testing.T) {
	// anchor (200x290) leaves a ~100x300 leftover strip (see
	// TestFillBedPullsInLaterSmallerJob) - tooBig can't fit in it even
	// though it shares anchor's customer; fits (a different customer) can.
	cust := int64(100)
	anchor := smallJob("anchor", "PLA")
	anchor.ShopifyCustomerID = &cust
	anchor.Footprint = bedpack.UnitFootprint{XMM: 200, YMM: 290, ZMM: 20}

	tooBig := smallJob("toobig", "PLA") // same customer, but doesn't fit remaining space
	tooBig.ShopifyCustomerID = &cust
	tooBig.Footprint = bedpack.UnitFootprint{XMM: 200, YMM: 290, ZMM: 20}

	fitsFine := smallJob("fits", "PLA") // different customer, fits the leftover strip
	fitsFine.Footprint = bedpack.UnitFootprint{XMM: 85, YMM: 220, ZMM: 20}

	bedJobs, _, remaining := fillBed([]PlanJob{anchor, tooBig, fitsFine}, DefaultNester)
	ids := jobIDSet(bedJobs)
	if ids["toobig"] {
		t.Fatalf("same-customer job that doesn't fit was placed anyway, bed = %+v", bedJobs)
	}
	if !ids["fits"] {
		t.Errorf("expected the smaller different-customer job to still fill remaining space, bed = %+v", bedJobs)
	}
	if !jobIDSet(remaining)["toobig"] {
		t.Errorf("expected toobig left in remaining for a later bed, remaining = %+v", remaining)
	}
}

func TestFillBedAffinityNeverOverridesColourCap(t *testing.T) {
	cust := int64(100)
	anchor := smallJob("anchor", "PLA")
	anchor.ShopifyCustomerID = &cust
	anchor.Colours = []string{"Red", "Blue"} // already at the 2-colour cap

	breaksCap := smallJob("breaks", "PLA") // same customer, but union would be 3 > cap
	breaksCap.ShopifyCustomerID = &cust
	breaksCap.Colours = []string{"Green"}

	other := smallJob("other", "PLA") // different customer, colour-compatible

	bedJobs, _, _ := fillBed([]PlanJob{anchor, breaksCap, other}, DefaultNester)
	ids := jobIDSet(bedJobs)
	if ids["breaks"] {
		t.Fatalf("same-customer job that breaks the colour cap was placed anyway, bed = %+v", bedJobs)
	}
	if !ids["other"] {
		t.Errorf("expected the colour-compatible different-customer job placed instead, bed = %+v", bedJobs)
	}
}

func TestScoreBatchRewardsSameCustomerCohesion(t *testing.T) {
	cust, other := int64(100), int64(200)
	cohesive := PlannedBatch{BedUtilisationPercent: 80, Jobs: []PlanJob{
		{ID: "a", ShopifyCustomerID: &cust}, {ID: "b", ShopifyCustomerID: &cust},
	}}
	scattered := PlannedBatch{BedUtilisationPercent: 80, Jobs: []PlanJob{
		{ID: "a", ShopifyCustomerID: &cust}, {ID: "b", ShopifyCustomerID: &other},
	}}
	if scoreBatch(cohesive) <= scoreBatch(scattered) {
		t.Errorf("cohesive score %v should exceed scattered score %v", scoreBatch(cohesive), scoreBatch(scattered))
	}
}

func TestCustomerCohesionCountNilCustomersNeverCohere(t *testing.T) {
	b := PlannedBatch{Jobs: []PlanJob{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	if got := customerCohesionCount(b); got != 0 {
		t.Errorf("customerCohesionCount = %d, want 0 (unset customers never cluster)", got)
	}
}

func TestScoreBatchUtilisationDominatesCohesion(t *testing.T) {
	cust := int64(100)
	highUtilNoCustomer := PlannedBatch{BedUtilisationPercent: 80, Jobs: []PlanJob{{ID: "a"}, {ID: "b"}}}
	lowUtilAllSameCustomer := PlannedBatch{BedUtilisationPercent: 40, Jobs: []PlanJob{
		{ID: "a", ShopifyCustomerID: &cust}, {ID: "b", ShopifyCustomerID: &cust},
		{ID: "c", ShopifyCustomerID: &cust}, {ID: "d", ShopifyCustomerID: &cust}, {ID: "e", ShopifyCustomerID: &cust},
	}}
	if scoreBatch(highUtilNoCustomer) <= scoreBatch(lowUtilAllSameCustomer) {
		t.Errorf("a poorly-utilised, highly cohesive batch must not outscore a well-utilised one")
	}
}

// fakeSingleUnitNester places at most the first unit of any trial and
// rejects everything else - used only to prove PlanWithNester's nest
// parameter is genuinely threaded through every packing decision, not
// silently bypassed by a stray direct bedpack.Pack call somewhere.
func fakeSingleUnitNester(units []bedpack.UnitFootprint) ([]bedpack.Placement, []bedpack.UnitFootprint) {
	if len(units) == 0 {
		return nil, nil
	}
	return []bedpack.Placement{{RefID: units[0].RefID}}, units[1:]
}

func TestNesterSeamIsActuallyUsed(t *testing.T) {
	// With DefaultNester, two small same-key jobs share one bed (see
	// TestPlanSameKeyShareOneBatch). With a fake Nester that only ever
	// accepts a single unit per trial, they must land in two separate
	// batches instead - proving nest is genuinely consulted throughout
	// fillBed/finalise/localSearch, not bypassed by a leftover direct call.
	batches, unb, _ := PlanWithNester(
		[]PlanJob{smallJob("a", "PLA"), smallJob("b", "PLA")},
		testNow, alwaysBatchGate, fakeSingleUnitNester,
	)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(batches) != 2 {
		t.Fatalf("batches = %d, want 2 (fake single-unit nester forces separate beds)", len(batches))
	}
}

func TestDefaultNesterMatchesBedpackPack(t *testing.T) {
	units := []bedpack.UnitFootprint{{RefID: "a", XMM: 50, YMM: 50, ZMM: 20}}
	gotPlacements, gotRejected := DefaultNester(units)
	wantPlacements, wantRejected := bedpack.Pack(units)
	if len(gotPlacements) != len(wantPlacements) || len(gotRejected) != len(wantRejected) {
		t.Errorf("DefaultNester diverges from bedpack.Pack: placements %d vs %d, rejected %d vs %d",
			len(gotPlacements), len(wantPlacements), len(gotRejected), len(wantRejected))
	}
}

func TestPlanEqualsPlanWithNesterDefaultNester(t *testing.T) {
	// Plan is documented as a thin wrapper around PlanWithNester(...,
	// DefaultNester) - confirm the two produce identical results for the
	// same input, locking in that relationship.
	jobs := []PlanJob{smallJob("a", "PLA"), smallJob("b", "PLA"), smallJob("c", "PETG")}
	b1, u1, h1 := Plan(jobs, testNow, alwaysBatchGate)
	b2, u2, h2 := PlanWithNester(jobs, testNow, alwaysBatchGate, DefaultNester)
	if len(b1) != len(b2) || len(u1) != len(u2) || len(h1) != len(h2) {
		t.Errorf("Plan and PlanWithNester(..., DefaultNester) diverged: batches %d/%d unbatchable %d/%d held %d/%d",
			len(b1), len(b2), len(u1), len(u2), len(h1), len(h2))
	}
}

func jobIDSet(jobs []PlanJob) map[string]bool {
	out := make(map[string]bool, len(jobs))
	for _, j := range jobs {
		out[j.ID] = true
	}
	return out
}
