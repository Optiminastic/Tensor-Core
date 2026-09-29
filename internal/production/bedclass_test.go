package production

import "testing"

// The shop's rule: the big beds earn their keep.
func TestBedFamilyForUnits(t *testing.T) {
	for _, c := range []struct {
		units int
		want  string
		why   string
	}{
		{5, "H2C", "a full bed goes to the biggest machine"},
		{4, "A2L", "four goes to the middle class"},
		{3, "P2S", "three goes to the smallest, so an H2C stays free for a five"},
	} {
		if got := BedFamilyForUnits(c.units); got != c.want {
			t.Errorf("%d units -> %s, want %s (%s)", c.units, got, c.want, c.why)
		}
	}
}

// A bed the planner should never build still has to resolve to something, and
// that something must be the SMALLEST class - a plate that fits a P2S fits
// every machine, while a plate built for an H2C fits three of thirteen.
func TestASizeOutsideTheRangeFallsToTheSmallestBed(t *testing.T) {
	for _, units := range []int{0, 1, 2} {
		if got := BedFamilyForUnits(units); got != "P2S" {
			t.Errorf("%d units -> %s, want P2S", units, got)
		}
	}
}

// More than the cap is not a reason to refuse a class. The planner caps beds
// at MaxBedUnits, but a bed topped up by hand can exceed it, and it still has
// to print somewhere.
func TestMoreThanAFullBedStillGoesToTheBiggestMachine(t *testing.T) {
	if got := BedFamilyForUnits(7); got != "H2C" {
		t.Errorf("7 units -> %s, want H2C", got)
	}
}
