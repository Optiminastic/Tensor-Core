package httpapi

import (
	"strconv"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
)

// A two-colour plate must be told WHERE its prime tower goes, not just that it
// wants one.
//
// Left unsaid, BambuBuddy supplies wipe_tower_x 15, and an H2C's second
// extruder cannot reach before X=25 - so the tower hangs over the edge of that
// nozzle's printable area and the whole plate is refused with "Found G-code in
// unprintable area of multi-extruder printers". Measured on batch 115483,
// identical in every other respect: x=15 refused, x=165 sliced into two
// nozzles at 54.91g and 46.57g.
func TestATwoColourPlateIsToldWhereItsPrimeTowerGoes(t *testing.T) {
	const minReach = 25.0

	two := []slotAssignment{{TrayHex: "#FFFFFF"}, {TrayHex: "#C12E1F"}}
	units := []bedpack.UnitFootprint{{RefID: "plank", XMM: 200, YMM: 50, ZMM: 40}}

	for _, family := range []string{"H2C", "A2L", "P2S"} {
		t.Run(family, func(t *testing.T) {
			bed := bedpack.BedForFamily(family)
			got := primeTowerOverrides(two, bed, units)
			if got["enable_prime_tower"] != "1" {
				t.Fatalf("enable_prime_tower = %v, want \"1\"", got["enable_prime_tower"])
			}
			xs, ok := got["wipe_tower_x"].([]string)
			if !ok || len(xs) != 1 {
				t.Fatalf("wipe_tower_x = %#v, want a one-entry array - unset is what "+
					"let BambuBuddy default it to 15", got["wipe_tower_x"])
			}
			if xs[0] == "15" {
				t.Fatal("the tower is at BambuBuddy's own default, which is the bug")
			}
			x, err := strconv.ParseFloat(xs[0], 64)
			if err != nil {
				t.Fatalf("wipe_tower_x %q is not a number", xs[0])
			}
			if x < minReach {
				t.Errorf("tower at X=%.1f, before the H2C's second nozzle at %.1f", x, minReach)
			}
			if _, ok := got["wipe_tower_y"].([]string); !ok {
				t.Error("no wipe_tower_y; a position needs both axes")
			}
		})
	}
}
