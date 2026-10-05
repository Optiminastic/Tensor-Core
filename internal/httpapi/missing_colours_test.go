package httpapi

// What a machine is MISSING has to be the same question the binding asks.
//
// These two used to disagree. missingColours tested the bed's resolved hex for
// exact membership in the loaded list, while bindPlateToTrays went through
// acceptedHexes - so a printer the dispatcher would have bound without
// complaint was shown as missing the colour, and the screen was more
// pessimistic than the floor about the same machine.

import (
	"slices"
	"testing"
)

// goldIdentity is the shop's real situation: one colour, two spools, two hexes.
//
// The hexes are the ones the fleet actually reports. Neither is #D4AF37, which
// is what the built-in table thinks gold is - that number came from a lookup
// table and no printer has ever held it.
func goldIdentity() []colourIdentity {
	return []colourIdentity{{Name: "GOLD", Hexes: []string{"#D3C5A3", "#D3B7A7"}}}
}

// The case that prompted this: a second spool of the same colour.
//
// Only one hex can be a colour's primary, and the primary is what gets written
// into the plate. Every other mapped hex is a spool the shop has confirmed is
// also that colour, and a machine holding one is not missing anything.
func TestAMachineHoldingTheOtherGoldIsNotMissingGold(t *testing.T) {
	needed := []queueColour{{Name: "GOLD", Hex: "#D3C5A3"}} // the primary
	bed := bedColours{Names: []string{"GOLD"}}

	// The printer with the primary spool. Never in doubt.
	if got := missingColours(needed, []string{"#D3C5A3"}, goldIdentity(), bed); len(got) != 0 {
		t.Errorf("missing = %v on the machine holding the primary gold, want none", got)
	}

	// The printer with the OTHER gold. This is the one that used to read as
	// missing while the dispatcher bound it happily.
	if got := missingColours(needed, []string{"#D3B7A7"}, goldIdentity(), bed); len(got) != 0 {
		t.Errorf("missing = %v on the machine holding the alternative gold; the colour "+
			"map says that spool IS gold, and the binding already agrees", got)
	}
}

// A plate built before its colour was mapped carries an orphaned hex.
//
// It declares #D4AF37 because that is what the built-in table said gold was
// when the bed was merged. No printer reports it. Under exact matching EVERY
// machine was missing gold, however carefully the real spools were mapped -
// the bed asking for a number that only ever existed in a lookup table.
func TestAPlateCarryingTheBuiltInHexIsNotMissingEverywhere(t *testing.T) {
	needed := []queueColour{{Name: "GOLD", Hex: "#D4AF37"}}
	bed := bedColours{Names: []string{"GOLD"}}

	if got := missingColours(needed, []string{"#D3C5A3"}, goldIdentity(), bed); len(got) != 0 {
		t.Errorf("missing = %v; the plate's hex reads back as GOLD through the built-in "+
			"table, and this machine holds a confirmed gold", got)
	}
}

// Widened, not abandoned. A colour nothing on the machine can be is missing.
func TestAColourTheMachineReallyLacksIsStillMissing(t *testing.T) {
	needed := []queueColour{{Name: "GOLD", Hex: "#D3C5A3"}}
	bed := bedColours{Names: []string{"GOLD"}}

	got := missingColours(needed, []string{"#2850E0"}, goldIdentity(), bed)
	if !slices.Contains(got, "GOLD") {
		t.Errorf("missing = %v, want GOLD; a printer holding only blue cannot print "+
			"this bed, and saying otherwise sends a plate to a machine that will refuse it", got)
	}
}

// An unresolvable colour is skipped rather than reported.
//
// A bed whose colour never resolved carries an empty hex, and there is nothing
// to compare. Naming it missing would blame the printer for a colour Tensor
// could not look up.
func TestAColourWithNoHexIsNotBlamedOnTheMachine(t *testing.T) {
	needed := []queueColour{{Name: "CHARTREUSE", Hex: ""}}
	if got := missingColours(needed, []string{"#D3C5A3"}, goldIdentity(), bedColours{}); len(got) != 0 {
		t.Errorf("missing = %v, want none; the colour has no hex to be missing", got)
	}
}

// An unnormalised tray hex still matches.
//
// AMS values arrive as 8-char RGBA and the map's keys are "#RRGGBB" uppercase,
// so a raw key would silently never match and every machine would read as
// missing every colour.
func TestLoadedHexesAreNormalisedBeforeComparing(t *testing.T) {
	needed := []queueColour{{Name: "GOLD", Hex: "#D3C5A3"}}
	for _, raw := range []string{"d3c5a3", "D3C5A3FF", "#d3c5a3"} {
		if got := missingColours(needed, []string{raw}, goldIdentity(), bedColours{}); len(got) != 0 {
			t.Errorf("missing = %v for a tray reporting %q, want none", got, raw)
		}
	}
}
