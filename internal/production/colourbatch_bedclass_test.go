package production

import (
	"github.com/Optiminastic/tensor-core/internal/bedpack"
	"testing"
	"time"
)

// A job that fits a big bed and not the small one.
//
// The Social Mirror's name piece measures 277 x 58.7 mm, which the .scad
// reports as "within 300 mm (A2L bed + prime tower)". Against the bed classes:
//
//	BedP2S  231 x 256   <- BedFamilyForUnits picks this for 1-3 units
//	BedA2L  305 x 320   <- 4 units
//	BedH2C  300 x 320   <- 5 or more
//
// So it fits the largest bed, passes the ReasonTooLargeForBed guard, and then
// cannot be placed on the P2S that a bed of one routes to.
func mirrorJob() PlanJob {
	return PlanJob{
		ID: "tsm-1", JobNumber: "JOB-116373", Quantity: 1,
		Colours: []string{"White"}, Family: "TSM",
		Footprint: unit(277, 58.7, 12),
	}
}

func unit(x, y, z float64) bedpack.UnitFootprint {
	return bedpack.UnitFootprint{XMM: x, YMM: y, ZMM: z}
}

// GroupByColour must TERMINATE on it, whatever else it decides.
//
// The inner loop shrinks `take` until the trial packs, and on zero closes the
// bed and continues - but closeBed drops a bed with no jobs on it, nothing is
// appended to unbatchable, and `remaining` is never decremented. The next pass
// opens a fresh empty bed and asks the identical question.
//
// Run in a goroutine with a deadline because the failure is a hang, and a test
// that hangs tells you far less than one that fails.
func TestAJobTooBigForItsBedClassDoesNotSpin(t *testing.T) {
	done := make(chan struct{})
	var batches []PlannedBatch
	var unb []Unbatchable

	go func() {
		defer close(done)
		batches, unb = GroupByColour([]PlanJob{mirrorJob()}, 0, nil)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("GroupByColour never returned for a job that fits the H2C but not the P2S")
	}

	// Either answer is defensible - a bed of one routed to a bigger class, or
	// a named refusal - but silence is not. A job that is neither batched nor
	// reported is invisible on every screen and eligible again every run.
	if len(batches) == 0 && len(unb) == 0 {
		t.Fatal("the job was neither placed on a bed nor reported as unbatchable")
	}

	// AND IT SHOULD PRINT, not be refused. The part fits three of the four
	// machines on the floor; routing it to one of them is the whole point of
	// promoting the class, and "unbatchable" would leave a sellable product
	// permanently unmakeable.
	if len(batches) != 1 {
		t.Fatalf("want the mirror on one bed, got %d batches and %d refusals", len(batches), len(unb))
	}
	if family := batches[0].MachineFamily; family == "P2S" || family == "" {
		t.Fatalf("laid out on %q, which cannot fit a 298mm part", family)
	}
}

// The promotion must not drag everything else onto the big machines: a plank
// that fits the P2S still belongs there, because the smallest plate prints on
// every machine on the floor.
func TestAPartThatFitsTheSmallBedStaysOnIt(t *testing.T) {
	plank := PlanJob{
		ID: "dnp-1", JobNumber: "JOB-1", Quantity: 3,
		Colours: []string{"Gold"}, Family: "DNP",
		Footprint: unit(200, 50, 40),
	}
	batches, unb := GroupByColour([]PlanJob{plank}, 0, nil)
	if len(batches) != 1 || len(unb) != 0 {
		t.Fatalf("a plank should batch: %d batches, %d refusals", len(batches), len(unb))
	}
	if got := batches[0].MachineFamily; got != "P2S" {
		t.Fatalf("three planks went to %q, want P2S - promotion must need a reason", got)
	}
}
