package bedpack

import (
	"math"
	"testing"
)

func planks(n int) []UnitFootprint {
	out := make([]UnitFootprint, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, UnitFootprint{RefID: string(rune('a' + i)), XMM: 200, YMM: 50, ZMM: 40})
	}
	return out
}

// extent is how much of the bed a packing actually occupies.
// extent is how big the packed plate IS, which is what a bed's XMM/YMM are
// compared against.
//
// Max minus min, not max alone. It measured the absolute right edge until the
// beds gained a shared 25mm origin, at which point "the plate ends at X=235"
// started being compared against "the bed is 231 wide" - two different
// quantities that agreed only while every bed began at zero. meshio.Merge3MF
// reports the same thing this does (`XMM: mx.X - mn.X`), so the test now
// measures a plate the way production does.
func extent(placed []Placement) (x, y float64) {
	var minX, minY, maxX, maxY float64
	for i, p := range placed {
		w, h := 200.0, 50.0
		if p.Rotated {
			w, h = h, w
		}
		if i == 0 {
			minX, minY = p.XOffsetMM, p.YOffsetMM
			maxX, maxY = p.XOffsetMM+w, p.YOffsetMM+h
			continue
		}
		minX = math.Min(minX, p.XOffsetMM)
		minY = math.Min(minY, p.YOffsetMM)
		maxX = math.Max(maxX, p.XOffsetMM+w)
		maxY = math.Max(maxY, p.YOffsetMM+h)
	}
	return maxX - minX, maxY - minY
}

// The bed a class prints on must hold the bed the shop routes to it.
//
// Five to an H2C, four to an A2L, three to a P2S - see
// production.BedFamilyForUnits. If any of these rejects a unit the routing
// rule promises a plate the machine cannot print.
func TestEachClassHoldsTheBedItIsSent(t *testing.T) {
	for _, c := range []struct {
		family string
		units  int
	}{
		{"H2C", 5}, {"A2L", 4}, {"P2S", 3},
	} {
		bed := BedForFamily(c.family)
		placed, rejected := PackOn(bed, planks(c.units))
		if len(rejected) > 0 || len(placed) != c.units {
			t.Errorf("%s (%.0fx%.0f): placed %d of %d, rejected %d",
				c.family, bed.XMM, bed.YMM, len(placed), c.units, len(rejected))
		}
	}
}

// The regression that this whole rule exists for.
//
// Every plate used to be packed on the A2L bed whatever machine it went to.
// Four planks laid out there come out 270x270, and a P2S is 256x256 - so the
// plate could not print, and BambuBuddy answered "G-code conflicts detected
// after slicing" because there was nowhere left for the wipe tower.
func TestAPlatePackedForItsOwnClassFitsThatClass(t *testing.T) {
	// Packed for the biggest class, measured against the smallest it must
	// never be sent to. Five planks, not four: once a band is kept for the
	// wipe tower the packer turns four planks into a 240x210 grid, which does
	// fit a P2S - so four no longer demonstrates anything and five does.
	wrong, _ := PackOn(BedForFamily("H2C"), planks(5))
	wx, wy := extent(wrong)
	p2s := BedForFamily("P2S")
	if wx <= p2s.XMM && wy <= p2s.YMM {
		t.Fatalf("the H2C packing came out %.0fx%.0f, which fits a P2S - "+
			"this test no longer guards anything", wx, wy)
	}

	// Packed for the P2S, it fits.
	right, rejected := PackOn(p2s, planks(3))
	if len(rejected) > 0 {
		t.Fatalf("3 planks did not fit the P2S bed")
	}
	rx, ry := extent(right)
	if rx > p2s.XMM || ry > p2s.YMM {
		t.Errorf("packed for the P2S and still %.0fx%.0f, over its %.0fx%.0f bed",
			rx, ry, p2s.XMM, p2s.YMM)
	}
}

// An unknown family must fall to the SMALLEST bed. A plate that fits a P2S
// fits everything; a plate built for an H2C fits three machines of thirteen.
func TestAnUnknownFamilyGetsTheSmallestBed(t *testing.T) {
	got := BedForFamily("something-new")
	if got.XMM != BedP2S.XMM || got.YMM != BedP2S.YMM {
		t.Errorf("unknown family got %.0fx%.0f, want the P2S bed", got.XMM, got.YMM)
	}
}
