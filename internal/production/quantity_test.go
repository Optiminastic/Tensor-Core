package production

// What a quantity of three MEANS to the planner.
//
// Three of one plank and three planks carrying three different names are not
// the same thing, and the difference is invisible on a bed: both are three
// footprints of identical size, laid out identically, printed in one run. The
// only place they differ is which model each one carries, and the planner
// never sees a model.
//
// So the rule it does follow is pinned here. A job's quantity is N copies of
// ONE model. An order line that needs three different names has to arrive as
// three jobs, or two of its customers get somebody else's name.

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
)

func TestAQuantityIsCopiesOfOneModel(t *testing.T) {
	j := plankJob("three", "GOLD")
	j.Quantity = 3

	units := unitsFor(j)
	if len(units) != 3 {
		t.Fatalf("units = %d, want 3", len(units))
	}
	// Every unit refers back to the SAME job, which is the whole statement:
	// one model, three times. If a line ever needs three different names,
	// splitting it is job creation's work and not something the planner can
	// infer - by the time it gets here the names are already gone.
	for i, u := range units {
		if u.RefID != j.ID {
			t.Errorf("unit %d belongs to %q, want %q - a quantity is copies of ONE job",
				i, u.RefID, j.ID)
		}
		if u.XMM != j.Footprint.XMM || u.YMM != j.Footprint.YMM || u.ZMM != j.Footprint.ZMM {
			t.Errorf("unit %d is %gx%gx%g, want the job's own %gx%gx%g",
				i, u.XMM, u.YMM, u.ZMM, j.Footprint.XMM, j.Footprint.YMM, j.Footprint.ZMM)
		}
	}
}

func TestAMissingQuantityIsOneRatherThanNone(t *testing.T) {
	// A job with no quantity recorded is one unit, not zero. Zero would mean
	// a bed that packs nothing and a plank nobody ever prints - and the job
	// would look batched.
	for _, n := range []int{0, -1} {
		j := plankJob("odd", "GOLD")
		j.Quantity = n
		if got := len(unitsFor(j)); got != 1 {
			t.Errorf("quantity %d gave %d units, want 1", n, got)
		}
	}
}

func TestThreeJobsOfOneAreNotOneJobOfThree(t *testing.T) {
	// The same three planks, as three jobs. Each carries its own model, so
	// each must stay its own unit on the bed - a planner that collapsed them
	// would print one name three times.
	var units []bedpack.UnitFootprint
	for _, id := range []string{"asha", "ravi", "meera"} {
		units = append(units, unitsFor(plankJob(id, "GOLD"))...)
	}
	if len(units) != 3 {
		t.Fatalf("units = %d, want 3", len(units))
	}
	seen := map[string]bool{}
	for _, u := range units {
		seen[u.RefID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("three jobs produced %d distinct models (%v); each customer's "+
			"plank must stay its own", len(seen), seen)
	}
}
