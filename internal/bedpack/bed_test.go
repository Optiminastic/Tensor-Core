package bedpack

import "testing"

// Nothing may be placed where the H2C's second nozzle cannot reach.
//
// Its extruders cover different strips - X 0..325 and X 25..330 - so a plate
// both must print lives in the overlap, X 25..325. A plank laid out from the
// usual 10mm margin put its second colour at X=10 and the slice failed with
// "Found G-code in unprintable area of multi-extruder printers" (115459-PURPLE).
func TestNothingIsPlacedOutsideTheH2CsTwoNozzleReach(t *testing.T) {
	const (
		minReach = 25.0  // extruder 2 starts here
		maxReach = 325.0 // extruder 1 stops here
	)
	units := make([]UnitFootprint, 0, 8)
	for i := 0; i < 8; i++ {
		units = append(units, UnitFootprint{RefID: "plank", XMM: 200, YMM: 50, ZMM: 20})
	}
	placements, _ := PackOn(BedH2C, units)
	if len(placements) == 0 {
		t.Fatal("nothing was placed, so the bed is unusable")
	}
	for _, p := range placements {
		w := 200.0
		if p.Rotated {
			w = 50.0
		}
		if p.XOffsetMM < minReach {
			t.Errorf("placed at X=%.1f, before the second nozzle's reach at %.1f",
				p.XOffsetMM, minReach)
		}
		if p.XOffsetMM+w > maxReach {
			t.Errorf("placed ending at X=%.1f, past the first nozzle's reach at %.1f",
				p.XOffsetMM+w, maxReach)
		}
	}
}
