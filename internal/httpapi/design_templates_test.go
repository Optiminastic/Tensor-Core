package httpapi

import (
	"testing"
)

// A template's required variables depend on what it is replacing.
//
// Replacing a plank means the DNP path drives it, and that path passes the
// names unconditionally - so a replacement without them is the silent failure
// the gate exists for. A NEW key belongs to a registry-configured product
// whose variables are whatever its own mapping names, and a keychain has no
// NAME_R: demanding one would refuse every product that is not a plank.
func TestRequiredParamsDependOnTheKey(t *testing.T) {
	// Every plank key demands the names - under whichever of the two
	// vocabularies that key is driven in. The joinery template has never had a
	// NAME_L and never will; what matters is that a replacement for it still
	// has to declare the two names the renderer passes, or it renders somebody
	// else's plank and exits 0.
	namesFor := map[string][2]string{
		"dnp_heart_joinery": {"name1", "name2"},
		"dnp_charm_joinery": {"name1", "name2"},
	}
	// The variable that SELECTS THE PIECE, which is PART for every template
	// but the charm file's - it calls the same thing export_part. The rule is
	// unchanged and is the reason this assertion exists: the renderer runs a
	// template once per coloured piece, and one that does not read the piece
	// it was asked for returns the whole model every time, so the 3MF holds
	// two overlapping copies in two colours. Only the word differs, so the
	// check names the word rather than dropping the rule.
	pieceVarFor := map[string]string{
		"dnp_charm_joinery": "export_part",
	}
	for _, key := range embeddedTemplateKeys {
		got := requiredParamsFor(key)
		names, ok := namesFor[key]
		if !ok {
			names = [2]string{"NAME_L", "NAME_R"}
		}
		for _, want := range names {
			if !contains(got, want) {
				t.Errorf("%s requires %v; a plank replacement must still declare %s",
					key, got, want)
			}
		}
		piece, named := pieceVarFor[key]
		if !named {
			piece = "PART"
		}
		if !contains(got, piece) {
			t.Errorf("%s requires %v; it must declare %s, or every colour pass "+
				"returns the whole model", key, got, piece)
		}
	}

	got := requiredParamsFor("keychain_name_tag")
	for _, name := range got {
		if name == "NAME_L" || name == "NAME_R" {
			t.Errorf("a new product is required to declare %s; it may have no such field", name)
		}
	}
	// PART is demanded, and only PART. The renderer runs every template twice,
	// once as "base" and once as "text", and colours the two results
	// separately - so a template ignoring it comes out as the same shape
	// twice, overlapping, in two colours.
	var hasPart bool
	for _, name := range got {
		if name == "PART" {
			hasPart = true
		}
	}
	if !hasPart {
		t.Error("a new product is not required to declare PART, which every render sets")
	}

	// OUT_X/Y/Z are NOT demanded, and an earlier version of this test asserted
	// they were "what every render sets". They are not: they come from
	// personalise.Params.Args(), which is the plank path, while a registry
	// product's arguments are its own field mapping plus PART. Demanding them
	// refused the first real product anybody tried to add.
	for _, name := range got {
		if name == "OUT_X" || name == "OUT_Y" || name == "OUT_Z" {
			t.Errorf("a new product is required to declare %s, which the registry "+
				"render path never passes", name)
		}
	}
}
