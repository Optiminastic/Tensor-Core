package production

import (
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
)

func smallJob(id, material string) PlanJob {
	return PlanJob{
		ID: id, JobNumber: "JOB-" + id, Material: material, Quantity: 1,
		Footprint: bedpack.UnitFootprint{RefID: id, XMM: 50, YMM: 50, ZMM: 20},
	}
}

func TestPlanNoFootprintIsUnbatchable(t *testing.T) {
	j := smallJob("a", "PLA")
	j.Footprint = bedpack.UnitFootprint{}
	batches, unb := Plan([]PlanJob{j})
	if len(batches) != 0 || len(unb) != 1 {
		t.Fatalf("batches=%d unbatchable=%d, want 0/1", len(batches), len(unb))
	}
	if unb[0].Reason != "No print file with measurable STL dimensions uploaded yet." {
		t.Errorf("reason = %q", unb[0].Reason)
	}
}

func TestPlanSameKeyShareOneBatch(t *testing.T) {
	batches, unb := Plan([]PlanJob{smallJob("a", "PLA"), smallJob("b", "PLA")})
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
	batches, _ := Plan([]PlanJob{smallJob("a", "PLA"), smallJob("b", "PETG")})
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
	batches, _ := Plan([]PlanJob{a, b})
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
	batches, unb := Plan([]PlanJob{j})
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
	batches, unb := Plan([]PlanJob{a, b})
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
	batches, unb := Plan([]PlanJob{a, b})
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(batches) != 2 {
		t.Errorf("batches = %d, want 2 (4+ colour jobs never merge, even with each other)", len(batches))
	}
}

func TestGroupingThreeColoursShareABatch(t *testing.T) {
	a := smallJob("a", "PLA")
	a.Colours = []string{"Red", "Yellow", "Black"}
	b := smallJob("b", "PLA")
	b.Colours = []string{"Black", "Red", "Yellow"}
	batches, _ := Plan([]PlanJob{a, b})
	if len(batches) != 1 {
		t.Errorf("batches = %d, want 1 (3 colours is within the cap, order-independent)", len(batches))
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
	batches, unb := Plan([]PlanJob{a, b})
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
	batches, unb := Plan([]PlanJob{a, b})
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
	batches, _ := Plan([]PlanJob{urgent, normal})
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
	batches, unb := Plan(jobs)
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	got := partitionScore(batches)
	for _, strategy := range packingStrategies {
		naive, _ := packJobs(orderJobs(jobs, strategy), strategy)
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
	batches, _ := Plan([]PlanJob{a})
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

func TestFillBedPullsInLaterSmallerJobToHitTarget(t *testing.T) {
	// "big" alone leaves a 100x320 leftover strip. "medium" (next in FCFS
	// order) does not fit that strip, but "small" (further down the queue)
	// does. A packer that flushes on the first miss would close the bed
	// after "big" alone at ~63% utilisation; fillBed's lookahead must instead
	// skip "medium" and pull "small" in, clearing the 80% target - "medium"
	// is left to its own bed since nothing else fits alongside it.
	big := PlanJob{ID: "big", JobNumber: "JOB-big", Quantity: 1,
		Footprint: bedpack.UnitFootprint{XMM: 220, YMM: 300, ZMM: 20}}
	medium := PlanJob{ID: "medium", JobNumber: "JOB-medium", Quantity: 1,
		Footprint: bedpack.UnitFootprint{XMM: 150, YMM: 150, ZMM: 20}}
	small := PlanJob{ID: "small", JobNumber: "JOB-small", Quantity: 1,
		Footprint: bedpack.UnitFootprint{XMM: 85, YMM: 220, ZMM: 20}}

	batches, unb := packJobs([]PlanJob{big, medium, small}, strategyFCFS)
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
	if batches[0].BedUtilisationPercent < TargetBedUtilisationPercent {
		t.Errorf("first bed utilisation = %.2f, want >= %.0f (big+small)",
			batches[0].BedUtilisationPercent, TargetBedUtilisationPercent)
	}

	secondBed := jobIDSet(batches[1].Jobs)
	if !secondBed["medium"] || len(secondBed) != 1 {
		t.Errorf("second bed jobs = %v, want medium alone", secondBed)
	}
}

func jobIDSet(jobs []PlanJob) map[string]bool {
	out := make(map[string]bool, len(jobs))
	for _, j := range jobs {
		out[j.ID] = true
	}
	return out
}
