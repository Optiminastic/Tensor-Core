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
	// What Tensor passes whatever the product is must still be demanded: PART
	// splits the model into its coloured pieces and OUT_X/Y/Z scale it to the
	// finished size. A template ignoring those renders one solid lump at the
	// template's own size, which slices and prints and is wrong.
	for _, want := range []string{"PART", "OUT_X", "OUT_Y", "OUT_Z"} {
		var found bool
		for _, name := range got {
			if name == want {
				found = true
			}
		}
		if !found {
			t.Errorf("a new product is not required to declare %s, which every render sets", want)
		}
	}
}
