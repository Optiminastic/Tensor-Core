package bedpack

import "testing"

// Nothing may be placed where the H2C's second nozzle cannot reach.
//
// Its extruders cover different strips - X 0..325 and X 25..330 - so a plate
// both must print lives in the overlap, X 25..325. A plank laid out from the
// usual 10mm margin put its second colour at X=10 and the slice failed with
// "Found G-code in unprintable area of multi-extruder printers" (115459-PURPLE).
//
// RUN AGAINST BOTH LAYOUTS, and that is the point of the table. This test
// existed and passed throughout the months the bug was live, because it called
// PackOn - the layout honouring XOriginMM - while a bed of identical planks
// takes PackColumnOn, which placed every one of them at X=10. An invariant
// proved on one branch and false on the other is worse than no invariant: it
// reads as covered.
func TestNothingIsPlacedOutsideTheH2CsTwoNozzleReach(t *testing.T) {
	const (
		minReach = 25.0  // extruder 2 starts here
		maxReach = 325.0 // extruder 1 stops here
	)
	units := make([]UnitFootprint, 0, 8)
	for i := 0; i < 8; i++ {
		units = append(units, UnitFootprint{RefID: "plank", XMM: 200, YMM: 50, ZMM: 20})
	}

	for _, layout := range []struct {
		name string
		pack func(Bed, []UnitFootprint) ([]Placement, []UnitFootprint)
	}{
		// What a mixed-footprint plate takes.
		{"PackOn", PackOn},
		// What a bed of identical planks takes - the live path.
		{"PackColumnOn", PackColumnOn},
	} {
		t.Run(layout.name, func(t *testing.T) {
			placements, _ := layout.pack(BedH2C, units)
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
		})
	}
}

// Every bed starts at the H2C's origin, including the single-nozzle ones.
//
// They have no reason of their own to avoid the first 25mm - an A2L reaches
// X=0 perfectly well. They give it up so that one plate is printable on any
// machine, because the two-tier fallback hands a P2S-classed bed to an H2C
// whenever no P2S is free, and a plate laid out from X=10 cannot print there.
func TestEveryBedStartsWhereBothNozzlesCanReach(t *testing.T) {
	const minReach = 25.0
	for _, family := range []string{"H2C", "A2L", "P2S"} {
		bed := BedForFamily(family)
		if bed.XOriginMM < minReach {
			t.Errorf("%s starts at X=%.1f; a plate packed there cannot print on an "+
				"H2C, whose second nozzle begins at %.1f", family, bed.XOriginMM, minReach)
		}
	}
	// And the unknown-family fallback, which every unrecognised printer gets.
	if bed := BedForFamily("something-new"); bed.XOriginMM < minReach {
		t.Errorf("the fallback bed starts at X=%.1f, want >= %.1f", bed.XOriginMM, minReach)
	}
}

// The plate that actually failed in production: one plank, packed for each
// class in turn, must land inside the strip both H2C nozzles can reach.
//
// One unit is classed P2S by BedFamilyForUnits, so this is the exact bed that
// was built from X=10, sent to an H2C for want of a free P2S, and refused.
func TestOnePlankPacksInsideTheSharedReachOnEveryBed(t *testing.T) {
	const (
		minReach = 25.0
		maxReach = 325.0
	)
	for _, family := range []string{"H2C", "A2L", "P2S"} {
		t.Run(family, func(t *testing.T) {
			bed := BedForFamily(family)
			placements, rejected := PackColumnOn(bed, planks(1))
			if len(rejected) > 0 || len(placements) != 1 {
				t.Fatalf("a single 200x50 plank did not fit the %s bed (%d rejected); "+
					"narrowing the beds to a shared origin must not cost a plank",
					family, len(rejected))
			}
			p := placements[0]
			if p.XOffsetMM < minReach {
				t.Errorf("%s placed the plank at X=%.1f, before the H2C's second "+
					"nozzle at %.1f", family, p.XOffsetMM, minReach)
			}
			if end := p.XOffsetMM + 200; end > maxReach {
				t.Errorf("%s placed the plank ending at X=%.1f, past the H2C's first "+
					"nozzle at %.1f", family, end, maxReach)
			}
		})
	}
}
