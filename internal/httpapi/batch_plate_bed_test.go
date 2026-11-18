package httpapi

// A batch's plate is laid out on the bed the planner packed it for.

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func TestABatchPlateUsesTheBedItWasPackedFor(t *testing.T) {
	// One unit: by count alone this is a P2S plate, which a 200 x 281 mm
	// Family Plank does not fit (BATCH-1001950).
	one := []gen.ProductionJob{{JobNumber: "JOB-116821-2"}}

	for _, family := range []string{"A2L", "H2C"} {
		b := gen.Batch{MachineFamily: &family}
		if got, want := batchPlateBed(b, one), bedpack.BedForFamily(family); got != want {
			t.Errorf("packed for %s: plate laid out on %+v, want %+v", family, got, want)
		}
	}

	// No recorded class: the unit count, as before.
	if got, want := batchPlateBed(gen.Batch{}, one), plateBed(one); got != want {
		t.Errorf("no class: plate laid out on %+v, want the unit-count bed %+v", got, want)
	}
	unknown := "XYZ"
	if got, want := batchPlateBed(gen.Batch{MachineFamily: &unknown}, one), plateBed(one); got != want {
		t.Errorf("unknown class: plate laid out on %+v, want the unit-count bed %+v", got, want)
	}
}
