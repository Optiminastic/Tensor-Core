package bedpack

import "testing"

// The prime tower must land where every nozzle that purges into it can reach.
//
// This is the bug that blocked every two-colour H2C bed. Tensor turned the
// tower on and never said where, BambuBuddy defaulted to wipe_tower_x 15, and
// an H2C's second extruder starts at X=25 - so the tower straddled the edge of
// its printable area and the slice was refused outright. Same plate, same
// presets, tower moved to 165: sliced, two nozzles, 54.91g + 46.57g.
func TestTheTowerLandsWhereBothH2CNozzlesCanReach(t *testing.T) {
	const (
		minReach = 25.0
		maxReach = 325.0
	)
	for _, family := range []string{"H2C", "A2L", "P2S"} {
		t.Run(family, func(t *testing.T) {
			bed := BedForFamily(family)
			x, y, ok := bed.TowerOrigin(planks(3))
			if !ok {
				t.Fatal("no tower band on a multi-colour bed; the plate has nowhere to purge")
			}
			if x < minReach {
				t.Errorf("tower at X=%.1f, before the H2C's second nozzle at %.1f - "+
					"this is exactly what BambuBuddy refused", x, minReach)
			}
			if right := x + bed.WipeTowerMM; right > maxReach {
				t.Errorf("tower ends at X=%.1f, past the first nozzle's reach at %.1f",
					right, maxReach)
			}
			if y < 0 || y+bed.WipeTowerMM > bed.YMM {
				t.Errorf("tower at Y=%.1f..%.1f, off a bed %.1f deep",
					y, y+bed.WipeTowerMM, bed.YMM)
			}
		})
	}
}

// The tower goes in the band the packer actually kept clear, not the other one.
func TestTheTowerGoesInTheBandThatWasKeptClear(t *testing.T) {
	bed := BedForFamily("H2C")

	// A plank column leaves the tower room beside it, so the band is the strip
	// at the right that limitX -= WipeTowerMM held the parts back from.
	x, y, _ := bed.TowerOrigin(planks(3))
	if want := bed.XOriginMM + bed.XMM - bed.EdgeMarginMM - bed.WipeTowerMM; x != want {
		t.Errorf("beside: tower X=%.1f, want %.1f (flush with where the parts stop)", x, want)
	}
	if y != bed.EdgeMarginMM {
		t.Errorf("beside: tower Y=%.1f, want the edge margin %.1f", y, bed.EdgeMarginMM)
	}

	// A part too wide to sit beside the tower pushes the band to the back, and
	// the tower must follow it there or it lands on the part.
	wide := []UnitFootprint{{RefID: "wide", XMM: bed.XMM - 2*bed.EdgeMarginMM, YMM: 50, ZMM: 10}}
	if FitsBesideTower(bed.Normalised(), wide) {
		t.Fatal("that part was supposed to be too wide to sit beside the tower")
	}
	_, y2, _ := bed.TowerOrigin(wide)
	if want := bed.EdgeMarginMM + bed.Normalised().ModelYMM(); y2 != want {
		t.Errorf("behind: tower Y=%.1f, want %.1f (past the depth the parts were held to)", y2, want)
	}
}

// A single-filament plate reserves no band and must be told nothing: those beds
// slice correctly today and a tower there is plastic and minutes for nothing.
func TestASingleFilamentPlateGetsNoTower(t *testing.T) {
	bed := BedForFamily("H2C")
	bed.WipeTowerMM = NoWipeTower
	if _, _, ok := bed.TowerOrigin(planks(3)); ok {
		t.Error("a plate that never changes colour was given a prime tower")
	}
}
