package bedpack

import "testing"

// A plate sits inside a larger bed unchanged, so a bigger class can print it.
//
// This is the half of the class rule that was missing. It was enforced as an
// equality, which is right downward and wrong upward, and the cost was three
// H2Cs that never printed anything Tensor planned.
func TestASmallPlateFitsEveryLargerClass(t *testing.T) {
	// A2L->H2C is NOT here any more, and that is the machine's doing. An H2C's
	// two nozzles only both reach X 25..325 - 300mm - while an A2L's bed is
	// 330 wide, so a plate laid out for an A2L has parts the H2C's second
	// nozzle cannot follow. H2C->A2L became possible for the same reason.
	for _, c := range []struct{ packedFor, target string }{
		{"P2S", "A2L"}, {"H2C", "A2L"},
	} {
		if got := FitForFamily(c.packedFor, c.target); got != FitOversized {
			t.Errorf("FitForFamily(%s, %s) = %v, want FitOversized", c.packedFor, c.target, got)
		}
	}
}

// The direction migration 0083 exists for, as a unit test.
//
// Four planks packed on the A2L come out 270x270 and a P2S is 256x256, so the
// plate could not print and BambuBuddy answered "G-code conflicts detected
// after slicing" - there was nowhere left for the wipe tower.
func TestAPlateNeverFitsASmallerClass(t *testing.T) {
	// P2S->H2C and A2L->H2C are refusals, not fits. Both are laid out from
	// X=0 and the H2C's second nozzle starts at X=25, so a two-colour plate
	// packed for either has lettering the H2C cannot reach - which is how
	// 115450-PURPLE sliced and then failed in BambuBuddy.
	for _, c := range []struct{ packedFor, target string }{
		{"A2L", "H2C"}, {"P2S", "H2C"}, {"H2C", "P2S"}, {"A2L", "P2S"},
	} {
		if got := FitForFamily(c.packedFor, c.target); got != FitNone {
			t.Errorf("FitForFamily(%s, %s) = %v, want FitNone - that plate does not fit",
				c.packedFor, c.target, got)
		}
	}
}

func TestAClassAlwaysFitsItself(t *testing.T) {
	for _, family := range []string{"H2C", "a2l", "  P2S  "} {
		if got := FitForFamily(family, family); got != FitExact {
			t.Errorf("FitForFamily(%q, %q) = %v, want FitExact", family, family, got)
		}
	}
	// Written differently on the two sides: a profile recorded as "p2s" is the
	// same machine as one recorded as "P2S", and treating them as two classes
	// would refuse a printer its own work.
	if got := FitForFamily("P2S", " p2s "); got != FitExact {
		t.Errorf("a differently-spelled same class = %v, want FitExact", got)
	}
}

// Deliberately unlike BedForFamily, which resolves an unknown name to the
// smallest bed. That default is right for packing - the plate then fits
// everything - and would be an invitation here, making every unrecognised name
// a synonym for P2S and letting a plate print on a machine nobody identified.
func TestAnUnknownClassIsRefusedRatherThanGuessed(t *testing.T) {
	for _, c := range []struct{ packedFor, target string }{
		{"P2S", "X1C"}, {"X1C", "P2S"}, {"", "P2S"}, {"P2S", ""},
	} {
		if got := FitForFamily(c.packedFor, c.target); got != FitNone {
			t.Errorf("FitForFamily(%q, %q) = %v, want FitNone", c.packedFor, c.target, got)
		}
	}
	// And the lenient default it sits beside is unchanged.
	if got := BedForFamily("X1C"); got != BedP2S.Normalised() {
		t.Errorf("BedForFamily kept no fallback: %+v", got)
	}
}

// Every axis, including the one no class exercises today. A wide bed under a
// short gantry is not a safe fallback for a plate whose parts need the height,
// and the check is cheapest to write while nothing depends on it.
func TestFitIsMeasuredOnEveryAxis(t *testing.T) {
	short := Bed{XMM: 400, YMM: 400, ZMM: 100}
	tall := Bed{XMM: 300, YMM: 300, ZMM: 300}
	if tall.FitsWithin(short) {
		t.Error("a plate 300 tall was called a fit for a bed 100 tall")
	}
	if !tall.FitsWithin(Bed{XMM: 300, YMM: 300, ZMM: 300}) {
		t.Error("a bed did not fit itself")
	}
}
