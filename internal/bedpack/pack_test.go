package bedpack

import (
	"math"
	"testing"
)

func TestPackSingleUnitAtMarginInset(t *testing.T) {
	placements, rejected := Pack([]UnitFootprint{{RefID: "a", XMM: 100, YMM: 100, ZMM: 50}})
	if len(rejected) != 0 || len(placements) != 1 {
		t.Fatalf("placed=%d rejected=%d, want 1/0", len(placements), len(rejected))
	}
	p := placements[0]
	if p.XOffsetMM != EdgeMarginMM || p.YOffsetMM != EdgeMarginMM || p.Rotated {
		t.Errorf("placement = %+v, want (%v,%v), not rotated", p, EdgeMarginMM, EdgeMarginMM)
	}
}

func TestPackRejectsTooTall(t *testing.T) {
	_, rejected := Pack([]UnitFootprint{{RefID: "z", XMM: 50, YMM: 50, ZMM: BedZMM + 1}})
	if len(rejected) != 1 {
		t.Errorf("a too-tall unit should be rejected, got %d rejected", len(rejected))
	}
}

func TestPackRejectsTooWide(t *testing.T) {
	// 350 mm wide cannot fit even alone (needs 360 with the gap, and the
	// margin-inset usable width is only 310).
	_, rejected := Pack([]UnitFootprint{{RefID: "w", XMM: 350, YMM: 50, ZMM: 10}})
	if len(rejected) != 1 {
		t.Errorf("an over-wide unit should be rejected, got %d rejected", len(rejected))
	}
}

func TestPackRotatesToFit(t *testing.T) {
	// Usable placement envelope (after the edge margin) is 310x300. "a"
	// (100x290) claims a strip, leaving a 200x300 leftover to one side. "b"
	// (250x100) does not fit that leftover at 0 deg (needs 260 wide) but
	// does turned 90 degrees (needs 110x260, which the 200x300 leftover has
	// room for).
	placements, rejected := PackOn(plateWithNoTower(), []UnitFootprint{
		{RefID: "a", XMM: 100, YMM: 290, ZMM: 20},
		{RefID: "b", XMM: 250, YMM: 100, ZMM: 20},
	})
	if len(rejected) != 0 || len(placements) != 2 {
		t.Fatalf("placed=%d rejected=%d, want 2/0", len(placements), len(rejected))
	}
	if placements[0].Rotated {
		t.Error("a should not need rotation")
	}
	if !placements[1].Rotated {
		t.Error("b should have been rotated to fit the leftover strip")
	}
}

func TestPackTwoUnitsShareBed(t *testing.T) {
	placements, rejected := Pack([]UnitFootprint{
		{RefID: "a", XMM: 100, YMM: 100, ZMM: 20},
		{RefID: "b", XMM: 100, YMM: 100, ZMM: 20},
	})
	if len(placements) != 2 || len(rejected) != 0 {
		t.Fatalf("placed=%d rejected=%d, want 2/0", len(placements), len(rejected))
	}
	if placements[0].RefID != "a" || placements[1].RefID != "b" {
		t.Errorf("placement order = %s,%s, want a,b", placements[0].RefID, placements[1].RefID)
	}
}

func TestPackFullBedRejectsRemainder(t *testing.T) {
	// A near-max part consumes the whole bed; nothing else can fit.
	placements, rejected := PackOn(plateWithNoTower(), []UnitFootprint{
		{RefID: "big", XMM: 290, YMM: 290, ZMM: 20},
		{RefID: "extra", XMM: 100, YMM: 100, ZMM: 20},
	})
	if len(placements) != 1 || len(rejected) != 1 {
		t.Fatalf("placed=%d rejected=%d, want 1/1", len(placements), len(rejected))
	}
	if rejected[0].RefID != "extra" {
		t.Errorf("rejected %s, want extra", rejected[0].RefID)
	}
}

func TestUtilisationPercent(t *testing.T) {
	got := UtilisationPercent([]UnitFootprint{{XMM: 100, YMM: 100}})
	want := math.Round(10000.0/105600.0*100*100) / 100 // 9.47
	if got != want {
		t.Errorf("utilisation = %v, want %v", got, want)
	}
	if UtilisationPercent(nil) != 0 {
		t.Error("empty utilisation should be 0")
	}
}

func TestUtilisationAreaStats(t *testing.T) {
	stats := Utilisation([]UnitFootprint{{XMM: 100, YMM: 100}})
	if stats.OccupiedMM2 != 10000 {
		t.Errorf("occupied = %v, want 10000", stats.OccupiedMM2)
	}
	if stats.FreeMM2 != bedAreaMM2-10000 {
		t.Errorf("free = %v, want %v", stats.FreeMM2, bedAreaMM2-10000)
	}
	if stats.Percent != UtilisationPercent([]UnitFootprint{{XMM: 100, YMM: 100}}) {
		t.Errorf("percent = %v, want it to match UtilisationPercent", stats.Percent)
	}

	empty := Utilisation(nil)
	if empty.OccupiedMM2 != 0 || empty.FreeMM2 != bedAreaMM2 || empty.Percent != 0 {
		t.Errorf("empty stats = %+v, want occupied=0 free=%v percent=0", empty, bedAreaMM2)
	}
}

// plank is the Dual Name Plank at its finished size, the product this bed is
// mostly built from.
func plank(ref string) UnitFootprint {
	return UnitFootprint{RefID: ref, XMM: 200, YMM: 50, ZMM: 40}
}

// A bed of planks must come out as one column, unrotated, in the order given -
// which for a colour batch is oldest order first. Pack would turn some of them
// and grid them two-by-two to waste less area; that plate is harder to read and
// harder to lift off in order.
func TestPackColumnKeepsOneColumnInOrder(t *testing.T) {
	units := []UnitFootprint{plank("a"), plank("b"), plank("c"), plank("d")}
	placements, rejected := PackColumnOn(plateWithNoTower(), units)
	if len(rejected) != 0 {
		t.Fatalf("rejected %d of 4 planks; they fit in 200 x 230 of a 310 x 300 envelope", len(rejected))
	}
	if len(placements) != 4 {
		t.Fatalf("placed %d units, want 4", len(placements))
	}
	for i, p := range placements {
		if p.Rotated {
			t.Errorf("unit %d was rotated; a column keeps every part facing the same way", i)
		}
		if p.XOffsetMM != EdgeMarginMM {
			t.Errorf("unit %d sits at x=%.1f, want %.1f - a column shares one x",
				i, p.XOffsetMM, EdgeMarginMM)
		}
		if p.RefID != units[i].RefID {
			t.Errorf("position %d holds %q, want %q - the column must keep input order",
				i, p.RefID, units[i].RefID)
		}
	}
	// Stacked in y, with the gap between them and nothing overlapping.
	for i := 1; i < len(placements); i++ {
		want := placements[i-1].YOffsetMM + units[i-1].YMM + ColumnGapMM
		if placements[i].YOffsetMM != want {
			t.Errorf("unit %d starts at y=%.1f, want %.1f (previous end plus a %.0fmm gap)",
				i, placements[i].YOffsetMM, want, ColumnGapMM)
		}
	}
}

// The column stays inside the margin-inset envelope rather than running off the
// bed: what does not fit is rejected, and the caller falls back to Pack.
func TestPackColumnRejectsWhatDoesNotFit(t *testing.T) {
	// Six planks need 6*50 + 5*15 = 375mm of depth; the envelope has 300.
	var units []UnitFootprint
	for i := range 6 {
		units = append(units, plank(string(rune('a'+i))))
	}
	placements, rejected := PackColumn(units)
	if len(rejected) == 0 {
		t.Fatal("six planks cannot fit one column of a 300mm envelope, but none were rejected")
	}
	for _, p := range placements {
		if p.YOffsetMM < EdgeMarginMM || p.YOffsetMM > BedYMM-EdgeMarginMM {
			t.Errorf("a placement at y=%.1f is outside the margin-inset envelope", p.YOffsetMM)
		}
	}
}

// A unit too tall for the machine is rejected here as it is by Pack - the column
// is a layout choice, not a licence to place something unprintable.
func TestPackColumnRejectsAnOverTallUnit(t *testing.T) {
	tall := plank("tall")
	tall.ZMM = BedZMM + 1
	if _, rejected := PackColumn([]UnitFootprint{tall}); len(rejected) != 1 {
		t.Error("a unit taller than the bed must be rejected")
	}
}

// plateWithNoTower is the bed these geometry cases were written against: the
// whole envelope, with nothing reserved.
//
// A single-colour plate really does get this, and stating it here keeps each
// case about the thing it tests - rotation, ordering, rejection - rather than
// about how much room a prime tower takes.
func plateWithNoTower() Bed {
	b := DefaultBed
	b.WipeTowerMM = NoWipeTower
	return b
}

// The band is the whole point: a two-colour plate has a tower, and the tower
// has to stand somewhere. The models must leave it a rectangle - beside them or
// behind them, whichever the bed affords, but a whole one.
func TestPackLeavesTheWipeTowerItsBand(t *testing.T) {
	planks := []UnitFootprint{
		{RefID: "a", XMM: 200, YMM: 50, ZMM: 40},
		{RefID: "b", XMM: 200, YMM: 50, ZMM: 40},
		{RefID: "c", XMM: 200, YMM: 50, ZMM: 40},
	}
	placements, rejected := PackOn(BedP2S, planks)
	if len(placements) != 3 || len(rejected) != 0 {
		t.Fatalf("placed=%d rejected=%d, want 3/0 - a P2S bed of three must still pack",
			len(placements), len(rejected))
	}
	bed := BedP2S.Normalised()
	ex, ey := extentOf(placements, planks)
	spareX := bed.XMM - 2*bed.EdgeMarginMM - ex
	spareY := bed.YMM - 2*bed.EdgeMarginMM - ey
	if spareX < WipeTowerMM-0.001 && spareY < WipeTowerMM-0.001 {
		t.Errorf("plate is %.0fx%.0f, leaving %.0fmm beside and %.0fmm behind - "+
			"the tower needs %.0f on one of them", ex, ey, spareX, spareY, WipeTowerMM)
	}
}

// extentOf is how much of the bed the placed models actually cover.
func extentOf(placements []Placement, units []UnitFootprint) (x, y float64) {
	for i, p := range placements {
		w, d := units[i].XMM, units[i].YMM
		if p.Rotated {
			w, d = d, w
		}
		if end := p.XOffsetMM + w; end > x {
			x = end
		}
		if end := p.YOffsetMM + d; end > y {
			y = end
		}
	}
	return x - EdgeMarginMM, y - EdgeMarginMM
}

// Three planks at 15mm apart need 180mm and the band leaves 176mm. Dropping the
// third would leave a bed of two - below the floor, so it would never print at
// all - and 10mm is still the ordinary clearance between parts everywhere else.
func TestPackColumnTightensTheGapRatherThanDropAPlank(t *testing.T) {
	planks := []UnitFootprint{
		{RefID: "a", XMM: 200, YMM: 50, ZMM: 40},
		{RefID: "b", XMM: 200, YMM: 50, ZMM: 40},
		{RefID: "c", XMM: 200, YMM: 50, ZMM: 40},
	}
	placements, rejected := PackColumnOn(BedP2S, planks)
	if len(placements) != 3 || len(rejected) != 0 {
		t.Fatalf("placed=%d rejected=%d, want all three in one column", len(placements), len(rejected))
	}
	if gap := placements[1].YOffsetMM - (placements[0].YOffsetMM + 50); gap != GapMM {
		t.Errorf("gap = %.1fmm, want it tightened to %.0f so the column clears the band", gap, GapMM)
	}
}

// The wide gap is what the shop asked for and must survive wherever it fits.
func TestPackColumnKeepsTheWideGapWhenThereIsRoom(t *testing.T) {
	planks := []UnitFootprint{
		{RefID: "a", XMM: 200, YMM: 50, ZMM: 40},
		{RefID: "b", XMM: 200, YMM: 50, ZMM: 40},
	}
	placements, _ := PackColumnOn(BedP2S, planks)
	if len(placements) != 2 {
		t.Fatalf("placed=%d, want 2", len(placements))
	}
	if gap := placements[1].YOffsetMM - (placements[0].YOffsetMM + 50); gap != ColumnGapMM {
		t.Errorf("gap = %.1fmm, want the full %.0f - two planks leave room for it", gap, ColumnGapMM)
	}
}

// A tower band is a reservation, not a way to end up with no bed at all.
func TestModelDepthNeverCollapsesTheBed(t *testing.T) {
	silly := Bed{XMM: 256, YMM: 256, ZMM: 256, WipeTowerMM: 10000}.Normalised()
	if got := silly.ModelYMM(); got != 236 {
		t.Errorf("model depth = %.1f, want the whole usable bed back when the band is nonsense", got)
	}
	if got := BedP2S.Normalised().ModelYMM(); got != 176 {
		t.Errorf("P2S model depth = %.1f, want 236 less the %.0fmm band", got, WipeTowerMM)
	}
}
