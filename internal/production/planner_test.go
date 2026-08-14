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

// TestKeyForIsOnlyTheHardBoundary pins the compatibility boundary at nozzle,
// quality and material - and nothing else.
//
// This deliberately reverses an earlier test that asserted support and infill
// DID discriminate. That test was right for its time: those fields were in the
// key, so it locked them in. The product decision changed - the boundary is
// what physically prevents two parts sharing a plate, and everything else is a
// preference that belongs in scoreBatch. Each extra key field silently halved
// the pool a bed could draw from, and beds already cannot reach the 80%
// utilisation target.
//
// Support and infill are still real slicing constraints; they are handled by
// slicing the plate conservatively (see plateSliceSpecFor), not by refusing to
// batch. Machine family is handled by assignMachineForBatch refusing to guess.
func TestKeyForIsOnlyTheHardBoundary(t *testing.T) {
	base := smallJob("a", "PLA")
	base.SupportUsed = false
	base.InfillPct = 15
	base.Priority = 5
	base.MachineFamily = "H2C"
	due := time.Now().Add(48 * time.Hour)
	base.DueDate = &due

	// Every one of these differs from base on an axis that used to split the
	// bucket. All of them must now group together.
	sameBucket := map[string]func(PlanJob) PlanJob{
		"support required": func(j PlanJob) PlanJob { j.SupportUsed = true; return j },
		"different infill": func(j PlanJob) PlanJob { j.InfillPct = 40; return j },
		"urgent priority":  func(j PlanJob) PlanJob { j.Priority = 1; return j },
		"due much later": func(j PlanJob) PlanJob {
			later := time.Now().Add(30 * 24 * time.Hour)
			j.DueDate = &later
			return j
		},
		"no due date at all": func(j PlanJob) PlanJob { j.DueDate = nil; return j },
	}
	for name, mutate := range sameBucket {
		if keyFor(base) != keyFor(mutate(base)) {
			t.Errorf("%s: must share a compatibility bucket - it is a scoring preference, not a physical boundary", name)
		}
	}

	// The three that genuinely prevent sharing a plate.
	differentBucket := map[string]func(PlanJob) PlanJob{
		"different material": func(j PlanJob) PlanJob { j.Material = "PA-CF"; return j },
		"different nozzle":   func(j PlanJob) PlanJob { j.NozzleLeft = "0.6"; return j },
		"different quality":  func(j PlanJob) PlanJob { j.QualityMM = "0.28"; return j },
	}
	for name, mutate := range differentBucket {
		if keyFor(base) == keyFor(mutate(base)) {
			t.Errorf("%s: must NOT share a compatibility bucket - the bed is physically set up for one of these", name)
		}
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

// TestPlanFarApartDueDatesStillShareABed reverses TestPlanDueDateClusteringSplits.
//
// Due dates used to hard-split every compatibility group on a 3-day window, so
// these two jobs got a bed each and both beds sat at 5% utilisation. Urgency is
// a score term now (dueUrgencyScore) and a lock condition (DueSoonWindow), which
// prioritises the urgent job without spending a whole plate on it.
func TestPlanFarApartDueDatesStillShareABed(t *testing.T) {
	soon := testNow.Add(12 * time.Hour)
	far := testNow.Add(30 * 24 * time.Hour)
	a := smallJob("a", "PLA")
	a.DueDate = &soon
	b := smallJob("b", "PLA")
	b.DueDate = &far

	batches, _, _ := Plan([]PlanJob{a, b}, testNow, alwaysBatchGate)
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1 - a due date is a preference, not a wall", len(batches))
	}
	if batches[0].UnitsPerBed != 2 {
		t.Errorf("units per bed = %d, want 2", batches[0].UnitsPerBed)
	}
}

// TestDueUrgencyRanksTheSoonerBatchHigher is the other half: the preference
// still has to be expressed, just through the score rather than the grouping.
func TestDueUrgencyRanksTheSoonerBatchHigher(t *testing.T) {
	soon := testNow.Add(6 * time.Hour)
	far := testNow.Add(30 * 24 * time.Hour)

	urgent := smallJob("u", "PLA")
	urgent.DueDate = &soon
	relaxed := smallJob("r", "PLA")
	relaxed.DueDate = &far

	sc := scoreCtx{now: testNow, gate: BatchGate{MaxWait: 4 * time.Hour}}
	urgentBatch := PlannedBatch{Jobs: []PlanJob{urgent}, BedUtilisationPercent: 50}
	relaxedBatch := PlannedBatch{Jobs: []PlanJob{relaxed}, BedUtilisationPercent: 50}

	if scoreBatch(urgentBatch, sc) <= scoreBatch(relaxedBatch, sc) {
		t.Errorf("a batch due in 6h (%v) must score above an identical one due in 30 days (%v)",
			scoreBatch(urgentBatch, sc), scoreBatch(relaxedBatch, sc))
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
	if unb[0].Reason != ReasonOversized {
		t.Errorf("reason = %q, want ReasonOversized", unb[0].Reason)
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

// TestUrgentAndRoutineShareABed reverses TestGroupingPriorityTierSplits.
//
// priorityTier used to be part of groupKey, so an urgent job could never share
// a plate with a routine one - which is backwards. Priority decides what gets
// attention first, not what can physically print together, and splitting on it
// meant an urgent job burned a whole bed at 5% utilisation while a compatible
// routine job waited for its own. Priority is a score term (priorityScore) and
// an unconditional lock override in shouldCreateBatch; neither needs a wall.
func TestUrgentAndRoutineShareABed(t *testing.T) {
	urgent := smallJob("a", "PLA")
	urgent.Priority = urgentPriority
	normal := smallJob("b", "PLA")
	normal.Priority = urgentPriority + 5

	batches, _, _ := Plan([]PlanJob{urgent, normal}, testNow, alwaysBatchGate)
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1 - priority ranks work, it does not partition beds", len(batches))
	}
	if batches[0].UnitsPerBed != 2 {
		t.Errorf("units per bed = %d, want 2", batches[0].UnitsPerBed)
	}
}

func TestLocalSearchNeverRegressesGreedyPartition(t *testing.T) {
	// Varied footprints and priorities, all in one compatibility bucket, so
	// the strategies can genuinely disagree on the best split. bestPartition's
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
	got := partitionScore(batches, testScoreCtx())
	for _, strategy := range packingStrategies {
		naive, _ := packJobs(orderJobs(jobs, strategy), strategy, DefaultNester)
		if naiveScore := partitionScore(naive, testScoreCtx()); got < naiveScore {
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
	if scoreBatch(cohesive, testScoreCtx()) <= scoreBatch(scattered, testScoreCtx()) {
		t.Errorf("cohesive score %v should exceed scattered score %v", scoreBatch(cohesive, testScoreCtx()), scoreBatch(scattered, testScoreCtx()))
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
	if scoreBatch(highUtilNoCustomer, testScoreCtx()) <= scoreBatch(lowUtilAllSameCustomer, testScoreCtx()) {
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

// testScoreCtx is the scoring context for tests that only care about one term
// (cohesion, utilisation). MaxWait is set so waitingScore has a denominator;
// fixtures with a zero CreatedAt contribute nothing to it either way.
func testScoreCtx() scoreCtx {
	return scoreCtx{now: time.Now(), gate: BatchGate{MaxWait: 4 * time.Hour}}
}

// TestScoreBalancesUtilisationAgainstPrintTime is the spec's own example: a
// 96%-full 7-hour plate versus an 86%-full 3-hour one. Always chasing
// utilisation picks the first and hurts throughput; the score has to be able
// to prefer the second.
func TestScoreBalancesUtilisationAgainstPrintTime(t *testing.T) {
	sc := scoreCtx{now: testNow, gate: BatchGate{MaxWait: 4 * time.Hour}}
	mins := func(m int) *int { return &m }

	fullSlow := PlannedBatch{
		Jobs: []PlanJob{smallJob("a", "PLA")}, BedUtilisationPercent: 96,
		TotalPrintTimeMinutes: mins(7 * 60),
	}
	leanFast := PlannedBatch{
		Jobs: []PlanJob{smallJob("b", "PLA")}, BedUtilisationPercent: 86,
		TotalPrintTimeMinutes: mins(3 * 60),
	}

	if scoreBatch(leanFast, sc) <= scoreBatch(fullSlow, sc) {
		t.Errorf("86%%/3h (%v) should beat 96%%/7h (%v): utilisation is the goal, not the only goal",
			scoreBatch(leanFast, sc), scoreBatch(fullSlow, sc))
	}
}

// TestPrintTimePenaltyIsBounded is the regression lock on the term's scale.
//
// The penalty used to be `rawMinutes * 0.10`, so it grew without limit: a
// 10-hour plate scored -60 against a maximum of +50 from utilisation, and a
// real measured plate time (plate slicing produces figures around 1000 minutes)
// drove the score so far negative that a nearly-empty bed outranked a full one.
// Normalising caps the term at its weight, which is the point of a weight.
func TestPrintTimePenaltyIsBounded(t *testing.T) {
	sc := scoreCtx{now: testNow, gate: BatchGate{MaxWait: 4 * time.Hour}}
	mins := func(m int) *int { return &m }

	full := PlannedBatch{
		Jobs: []PlanJob{smallJob("a", "PLA")}, BedUtilisationPercent: 95,
		TotalPrintTimeMinutes: mins(1032), // a real measured plate
	}
	nearlyEmpty := PlannedBatch{
		Jobs: []PlanJob{smallJob("b", "PLA")}, BedUtilisationPercent: 5,
		TotalPrintTimeMinutes: mins(30),
	}

	if scoreBatch(full, sc) <= scoreBatch(nearlyEmpty, sc) {
		t.Errorf("a 95%% bed taking 1032 min (%v) must still beat a 5%% bed taking 30 min (%v); the time term has escaped its weight again",
			scoreBatch(full, sc), scoreBatch(nearlyEmpty, sc))
	}

	// And the term itself must stay inside 0-100 however long the plate.
	for _, m := range []int{0, 60, referencePlateMinutes, 10 * referencePlateMinutes} {
		got := printTimeScore(PlannedBatch{TotalPrintTimeMinutes: mins(m)})
		if got < 0 || got > 100 {
			t.Errorf("printTimeScore(%d min) = %v, want within 0-100", m, got)
		}
	}
	if got := printTimeScore(PlannedBatch{}); got != 0 {
		t.Errorf("printTimeScore with no estimate = %v, want 0 (unknown is not evidence either way)", got)
	}
}

// TestWaitingScoreStopsStarvation: a job that is neither urgent nor due soon
// must still climb the ranking the longer it waits, or a steady stream of
// fuller beds keeps it queued forever.
func TestWaitingScoreStopsStarvation(t *testing.T) {
	gate := BatchGate{MaxWait: 4 * time.Hour}
	sc := scoreCtx{now: testNow, gate: gate}

	fresh := smallJob("fresh", "PLA")
	fresh.CreatedAt = testNow.Add(-time.Minute)
	stale := smallJob("stale", "PLA")
	stale.CreatedAt = testNow.Add(-3 * time.Hour)

	freshBatch := PlannedBatch{Jobs: []PlanJob{fresh}, BedUtilisationPercent: 40}
	staleBatch := PlannedBatch{Jobs: []PlanJob{stale}, BedUtilisationPercent: 40}

	if scoreBatch(staleBatch, sc) <= scoreBatch(freshBatch, sc) {
		t.Errorf("a 3-hour-old batch (%v) must outrank an identical fresh one (%v)",
			scoreBatch(staleBatch, sc), scoreBatch(freshBatch, sc))
	}
	if got := waitingScore([]PlanJob{stale}, testNow, 0); got != 0 {
		t.Errorf("waitingScore with no MaxWait = %v, want 0 (no denominator)", got)
	}
}

// TestIdleMachineCreatesUnderTargetBatch covers BatchGate.IdleMachines: with a
// printer free and the compatible pool exhausted, holding the bed back buys
// nothing and costs machine time.
func TestIdleMachineCreatesUnderTargetBatch(t *testing.T) {
	// One small job: ~2.4% of the bed, far below target, and no other override
	// applies (routine priority, no due date, just created, no aging window).
	job := smallJob("a", "PLA")
	job.Priority = 5
	job.CreatedAt = testNow

	held := BatchGate{MaxWait: 4 * time.Hour, IdleMachines: 0}
	if batches, _, heldOut := Plan([]PlanJob{job}, testNow, held); len(batches) != 0 || len(heldOut) != 1 {
		t.Fatalf("with no idle machine: batches=%d held=%d, want 0/1", len(batches), len(heldOut))
	}

	free := BatchGate{MaxWait: 4 * time.Hour, IdleMachines: 1}
	batches, _, heldOut := Plan([]PlanJob{job}, testNow, free)
	if len(batches) != 1 || len(heldOut) != 0 {
		t.Fatalf("with an idle machine: batches=%d held=%d, want 1/0", len(batches), len(heldOut))
	}
}

// TestIdleOverrideIsBoundedByIdleMachineCount is the other half of the rule.
//
// Idle capacity is finite. Releasing every thin partition because SOME printer
// is free converts the whole backlog into single-job beds, and a created batch
// never returns to the pool - so those beds can never be consolidated later.
// Exactly as many as there are free printers get released; the rest keep
// accumulating compatible volume, which is what holding them back is for.
func TestIdleOverrideIsBoundedByIdleMachineCount(t *testing.T) {
	// Six jobs that cannot share a bed (each a different material, so each is
	// its own compatibility group) and none of which qualifies for any other
	// override: routine priority, no due date, just created.
	materials := []string{"PLA", "PETG", "ABS", "ASA", "TPU", "PC"}
	jobs := make([]PlanJob, 0, len(materials))
	for i, m := range materials {
		j := smallJob(string(rune('a'+i)), m)
		j.Priority = 5
		j.CreatedAt = testNow
		jobs = append(jobs, j)
	}

	gate := BatchGate{MaxWait: 4 * time.Hour, IdleMachines: 2}
	batches, _, heldOut := Plan(jobs, testNow, gate)

	if len(batches) != 2 {
		t.Errorf("created %d batches with 2 idle machines, want 2 - the override must not empty the whole backlog onto thin beds", len(batches))
	}
	if len(heldOut) != len(materials)-2 {
		t.Errorf("held %d partitions, want %d - the remainder should keep waiting for compatible volume", len(heldOut), len(materials)-2)
	}
}
