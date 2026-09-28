package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// A template's required variables depend on what it is replacing.
//
// Replacing a plank means the DNP path drives it, and that path passes the
// names unconditionally - so a replacement without them is the silent failure
// the gate exists for. A NEW key belongs to a registry-configured product
// whose variables are whatever its own mapping names, and a keychain has no
// NAME_R: demanding one would refuse every product that is not a plank.
func TestRequiredParamsDependOnTheKey(t *testing.T) {
	for _, key := range embeddedTemplateKeys {
		got := requiredParamsFor(key)
		if len(got) != len(personalise.RequiredTemplateParams) {
			t.Errorf("%s requires %v; a plank replacement must still declare the names", key, got)
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
