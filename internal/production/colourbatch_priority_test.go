package production

// Beds are filled first come, first served - a paid priority upgrade does not
// change the order.
//
// This file used to assert the opposite. It pinned that six expedited planks
// consolidated onto their own beds ahead of twelve older standard ones, on the
// reasoning that every bed carrying a priority plank jumps the dispatch queue,
// so scattering them drags standard work ahead with them.
//
// The shop has since instructed that priority orders are planned as ordinary
// ones. The three places that read the rank are gone - httpapi's
// sortPriorityFirst, carriesPriority in the lock gate, and the min(priority)
// term in ListBatchesToDispatch - and this is the test that proves the planner
// itself never looked at it.
//
// GroupByColour does not sort, so this is really a test of the contract between
// it and its caller: the pool arrives in placed_at order and comes out in that
// order, whatever ranks are scattered through it.

import (
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
)

func plank(number, colour string, priority int, placed time.Time) PlanJob {
	return PlanJob{
		ID: number, JobNumber: number,
		Material: "PLA", MachineFamily: "A2L",
		Colours: []string{colour}, Priority: priority, Quantity: 1,
		CreatedAt: placed,
		Footprint: bedpack.UnitFootprint{RefID: number, XMM: 200, YMM: 50, ZMM: 40},
	}
}

// Six late expedited planks behind twelve early standard ones stay behind them.
//
// The pool is handed over oldest-first, which is what ListReplannableJobs
// returns, and the expedited planks were placed LAST - so under the old
// priority-first rule they filled the first bed, and under this one they fill
// the last.
func TestColourBedsAreFilledFirstComeFirstServed(t *testing.T) {
	at := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }

	var pool []PlanJob
	// Twelve standard blue planks, placed first.
	for i := 0; i < 12; i++ {
		pool = append(pool, plank("S"+string(rune('a'+i)), "BLUE", 0, at(1+i)))
	}
	// Six expedited blue planks, placed after every one of them.
	for i := 0; i < 6; i++ {
		pool = append(pool, plank("P"+string(rune('1'+i)), "BLUE", -1, at(20+i)))
	}

	batches, unbatchable := GroupByColour(pool, 4, DefaultBedNester)
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

	// Eighteen planks at four per bed: five beds. The twelve standard ones fill
	// the first three exactly, so the expedited six are on the last two -
	// four on bed 3, the remaining two on bed 4 - purely because they arrived
	// last. Under the old rule this was [4 2 0 0 0].
	if len(counts) != 5 {
		t.Fatalf("eighteen planks produced %d beds, want 5 (priority per bed: %v)",
			len(counts), counts)
	}
	if counts[0] != 0 {
		t.Errorf("the first bed carries %d priority planks, want 0 - the twelve older "+
			"standard orders come first (all beds: %v)", counts[0], counts)
	}
	for i, want := range []int{0, 0, 0, 4, 2} {
		if counts[i] != want {
			t.Errorf("bed %d carries %d priority planks, want %d - arrival order, "+
				"not rank (all beds: %v)", i, counts[i], want, counts)
		}
	}
}

// The oldest order is on the first bed, which is the property the whole rule
// reduces to. Pinned separately because the counts above would also be
// satisfied by an order nobody intended.
func TestTheOldestOrderIsOnTheFirstBed(t *testing.T) {
	at := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }

	pool := []PlanJob{
		plank("oldest-standard", "BLUE", 0, at(1)),
		plank("expedited", "BLUE", -1, at(2)),
		plank("newest-standard", "BLUE", 0, at(3)),
	}

	batches, _ := GroupByColour(pool, 4, DefaultBedNester)
	if len(batches) != 1 {
		t.Fatalf("three blue planks produced %d beds, want 1", len(batches))
	}
	if got := batches[0].Jobs[0].JobNumber; got != "oldest-standard" {
		t.Errorf("the first place on the bed went to %q, want the oldest order", got)
	}
	if got := batches[0].Jobs[1].JobNumber; got != "expedited" {
		t.Errorf("the second place went to %q, want the plank that arrived second", got)
	}
}

// Different colours cannot share a bed, so work in five colours is five beds
// however it is ordered. Pinned so the tests above are not read as promising
// more than the filament allows.
func TestColourBedsStillCannotMixColours(t *testing.T) {
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	pool := []PlanJob{
		plank("P1", "BLUE", -1, at),
		plank("P2", "RED", -1, at),
		plank("P3", "GOLD", -1, at),
	}
	batches, _ := GroupByColour(pool, 4, DefaultBedNester)
	if len(batches) != 3 {
		t.Errorf("three colours produced %d beds, want 3", len(batches))
	}
}
