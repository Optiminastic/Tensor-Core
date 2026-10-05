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

		// One blue spool serves both names, so both must reach one bed. Before
		// this, a sky blue job waited for three more of a colour the shop does
		// not stock separately.
		{"aliased name", []string{"SKY BLUE"}, "BLUE"},
		{"aliased lower case", []string{"sky blue"}, "BLUE"},
		{"aliased padded", []string{"  Sky   Blue  "}, "BLUE"},
		{"alias and target together name one spool", []string{"BLUE", "SKY BLUE"}, "BLUE"},
		{"alias does not swallow other colours", []string{"SKY BLUE", "WHITE"}, "BLUE+WHITE"},
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
		t.Errorf("bed key = %q, want %q - the normalised colour set, the material "+
			"and the machine class, and nothing else", ka, want)
	}
}

// The SKU does NOT split a bed, and that is the shop's rule stated plainly:
// batching is by colour.
//
// It used to. The key carried the SKU's slicer-pipeline token, so DNP-GLD and
// DNPWL-GLD - the same Dual Name Plank in the same gold, differing only by
// whether an LED base ships in the box - went to two beds. One held a single
// plank and the other two; neither reached the three-unit floor, so neither
// locked and neither printed, while three gold planks sat waiting for a fourth
// that would have opened a third bed.
//
// colourFromVariant already discards the light option as "a fulfilment extra,
// not something that changes which filament goes in the printer". This makes
// the bed key agree with it.
func TestTheSKUDoesNotDecideWhatSharesABed(t *testing.T) {
	// Same colour, same material, same class. Different products.
	plank := PlanJob{Colours: []string{"GOLD"}, Material: "PLA", MachineFamily: "H2C"}
	plankWithLight := PlanJob{Colours: []string{"GOLD"}, Material: "PLA", MachineFamily: "H2C"}

	kp, okP := colourBedKey(plank)
	kl, okL := colourBedKey(plankWithLight)
	if !okP || !okL {
		t.Fatal("both jobs record a colour")
	}
	if kp != kl {
		t.Errorf("two gold planks got different beds: %q vs %q", kp, kl)
	}

	// What still separates beds is what a plate physically cannot mix.
	for _, c := range []struct {
		name string
		job  PlanJob
	}{
		{"a different colour", PlanJob{Colours: []string{"RED"}, Material: "PLA", MachineFamily: "H2C"}},
		{"a different material", PlanJob{Colours: []string{"GOLD"}, Material: "PETG", MachineFamily: "H2C"}},
		{"a different machine class", PlanJob{Colours: []string{"GOLD"}, Material: "PLA", MachineFamily: "P2S"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			k, _ := colourBedKey(c.job)
			if k == kp {
				t.Errorf("%s shared a bed with the gold plank: %q", c.name, k)
			}
		})
	}
}

// The alias is a claim about which spool is loaded, so it must hold everywhere
// a colour is compared - not just in the planner that happens to call it first.
func TestCanonicalColourNameNormalisesAndAliases(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"BLUE", "BLUE"},
		{"blue", "BLUE"},
		{"  Gold  ", "GOLD"},
		{"sky blue", "BLUE"},
		{"SKY  BLUE", "BLUE"},
		{"", ""},
		{"   ", ""},
		// Unaliased names pass through uppercased, never guessed at: baby pink
		// is its own spool and must not collapse into pink.
		{"baby pink", "BABY PINK"},
	} {
		if got := CanonicalColourName(c.in); got != c.want {
			t.Errorf("CanonicalColourName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
