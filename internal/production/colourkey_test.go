package production

// One definition of "same colour", shared by the two paths that decide which
// jobs may share a plate.
//
// The planner has always grouped beds by a normalised colour set. The manual
// add-jobs endpoint used a different key that had no colour in it at all, so it
// offered - and accepted - a RED plank onto a BLUE bed. Both now read
// NormalisedColourKey, and these tests are what stops them drifting apart
// again.

import "testing"

func TestNormalisedColourKeyIgnoresOrderCaseAndSpacing(t *testing.T) {
	for _, c := range []struct {
		name    string
		colours []string
		want    string
	}{
		{"one colour", []string{"BLUE"}, "BLUE"},
		// The store really does send both spellings for one filament: a
		// production bed holds "GOLD" and "Gold" today. Two beds for one colour
		// is the mistake this prevents.
		{"case differs", []string{"Gold"}, "GOLD"},
		{"padded", []string{"  Blue  "}, "BLUE"},
		// The SET is what matters, not the order somebody wrote it in.
		{"order differs", []string{"WHITE", "BLUE"}, "BLUE+WHITE"},
		{"order and case differ", []string{"white", "Blue"}, "BLUE+WHITE"},
		{"blank entries dropped", []string{"BLUE", "", "   "}, "BLUE"},

		// Empty is the colourless job. It is NOT a wildcard: colourbatch
		// refuses to bed it at all, and CompatibilityKey treats it as its own
		// value, so a colourless job does not silently match a coloured one.
		{"no colours", nil, ""},
		{"only blanks", []string{"", "  "}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalisedColourKey(c.colours); got != c.want {
				t.Errorf("NormalisedColourKey(%q) = %q, want %q", c.colours, got, c.want)
			}
		})
	}
}

// The planner's bed key is built from that same function, so a change to one
// cannot leave the other behind.
func TestColourBedKeyIsBuiltFromTheNormalisedColourKey(t *testing.T) {
	a := PlanJob{Colours: []string{"white", "Blue"}, Material: "PLA", MachineFamily: "A2L"}
	b := PlanJob{Colours: []string{"BLUE", "WHITE"}, Material: "PLA", MachineFamily: "A2L"}

	ka, okA := colourBedKey(a)
	kb, okB := colourBedKey(b)
	if !okA || !okB {
		t.Fatalf("both jobs record colours; ok = %v, %v", okA, okB)
	}
	if ka != kb {
		t.Errorf("bed keys differ for the same colour set: %q vs %q", ka, kb)
	}
	if want := NormalisedColourKey(a.Colours) + "|PLA|A2L"; ka != want {
		t.Errorf("bed key = %q, want %q - it must be the normalised colour set plus "+
			"material and family", ka, want)
	}

	// A colourless job still has no bed, which is what ReasonNoColour reports.
	if _, ok := colourBedKey(PlanJob{Material: "PLA", MachineFamily: "A2L"}); ok {
		t.Error("a job recording no colour was given a bed key")
	}
}
