package production

// Priority work consolidates onto its own beds.
//
// The question this answers: given six expedited blue planks and a pile of
// standard blue ones, does the planner produce one full bed of expedited work
// (plus the remainder) or six beds each carrying one expedited plank among
// three standard ones?
//
// It has to be the former. Every bed a priority plank sits on is a bed that
// jumps the dispatch queue, so scattering them means printing six plates to
// clear six expedited customers instead of two - and dragging eighteen standard
// planks ahead of the queue with them.
//
// GroupByColour does not sort, so this is really a test of the contract between
// it and its caller: the pool arrives priority-first (httpapi.sortPriorityFirst)
// and GroupByColour fills each colour's open bed before starting another.

import (
	"sort"
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
)

// planJobsPriorityFirst is what httpapi.sortPriorityFirst produces, duplicated
// here rather than imported because httpapi depends on this package.
func planJobsPriorityFirst(jobs []PlanJob) []PlanJob {
	out := append([]PlanJob(nil), jobs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out
}

func plank(number, colour string, priority int, placed time.Time) PlanJob {
	return PlanJob{
		ID: number, JobNumber: number,
		Material: "PLA", MachineFamily: "A2L",
		Colours: []string{colour}, Priority: priority, Quantity: 1,
		CreatedAt: placed,
		Footprint: bedpack.UnitFootprint{RefID: number, XMM: 200, YMM: 50, ZMM: 40},
	}
}

func TestPriorityPlanksFillTheirOwnBedsRatherThanScatter(t *testing.T) {
	at := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }

	var pool []PlanJob
	// Six expedited blue planks, placed LATE - so oldest-first alone would put
	// every one of them behind the standard work below.
	for i := 0; i < 6; i++ {
		pool = append(pool, plank("P"+string(rune('1'+i)), "BLUE", -1, at(20+i)))
	}
	// Twelve standard blue planks, placed earlier.
	for i := 0; i < 12; i++ {
		pool = append(pool, plank("S"+string(rune('a'+i)), "BLUE", 0, at(1+i)))
	}

	batches, unbatchable := GroupByColour(planJobsPriorityFirst(pool), 4, DefaultBedNester)
	if len(unbatchable) != 0 {
		t.Fatalf("nothing should be unbatchable, got %d", len(unbatchable))
	}

	// How many priority planks landed on each bed, in bed order.
	counts := make([]int, 0, len(batches))
	for _, b := range batches {
		n := 0
		for _, j := range b.Jobs {
			if j.Priority < 0 {
				n++
			}
		}
		counts = append(counts, n)
	}

	// Six expedited planks at four per bed: one full bed of four, then two
	// riding with standard work. NOT six beds of one.
	if counts[0] != 4 {
		t.Errorf("first bed carries %d priority planks, want 4 - expedited work "+
			"must fill a bed before standard work joins it (all beds: %v)", counts[0], counts)
	}
	if counts[1] != 2 {
		t.Errorf("second bed carries %d priority planks, want the remaining 2 (all beds: %v)",
			counts[1], counts)
	}
	beds := 0
	for _, n := range counts {
		if n > 0 {
			beds++
		}
	}
	if beds != 2 {
		t.Errorf("priority work is spread over %d beds, want 2 - every such bed jumps "+
			"the dispatch queue, so scattering drags standard planks ahead with it "+
			"(all beds: %v)", beds, counts)
	}
}

// Different colours cannot share a bed, so expedited work in five colours is
// five beds however it is sorted. Pinned so the test above is not read as
// promising more than the filament allows.
func TestPriorityPlanksStillCannotMixColours(t *testing.T) {
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	pool := []PlanJob{
		plank("P1", "BLUE", -1, at),
		plank("P2", "RED", -1, at),
		plank("P3", "GOLD", -1, at),
	}
	batches, _ := GroupByColour(planJobsPriorityFirst(pool), 4, DefaultBedNester)
	if len(batches) != 3 {
		t.Errorf("three colours produced %d beds, want 3", len(batches))
	}
}
