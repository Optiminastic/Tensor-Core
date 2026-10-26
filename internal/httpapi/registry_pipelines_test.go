package httpapi

// Which slicer settings a SKU prints with.
//
// The rule is one line and the reason is forty-four: a product's colourways
// all print the same way, so the mapping belongs to the parent, and the one
// colour that genuinely differs still says so for itself.

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func pipeline(name string, id int32) *gen.SkuSlicerPipeline {
	return &gen.SkuSlicerPipeline{PipelineID: id, PipelineName: name}
}

func TestASKUInheritsItsProductsPipeline(t *testing.T) {
	got := resolvePipeline(nil, pipeline("DNP-H2C", 7))
	if got == nil || got.PipelineName != "DNP-H2C" {
		t.Fatalf("pipeline = %v, want the product's", got)
	}
}

// THE POINT OF KEEPING SKU ROWS. One colourway that prints differently - a
// glow filament needing its own profile - must not be overruled by the
// product's default sitting above it.
func TestASKUsOwnPipelineBeatsItsProducts(t *testing.T) {
	got := resolvePipeline(pipeline("DNP-GLOW", 9), pipeline("DNP-H2C", 7))
	if got == nil || got.PipelineName != "DNP-GLOW" {
		t.Fatalf("pipeline = %v, want the SKU's own", got)
	}
}

// Neither is a real answer: the machine class default is what every SKU used
// before any of this existed, and an unmapped SKU still uses it.
func TestNoMappingAtEitherLevelIsNoMapping(t *testing.T) {
	if got := resolvePipeline(nil, nil); got != nil {
		t.Fatalf("pipeline = %v, want none so the class default stands", got)
	}
}

// The parent's own name, taken from one of its order lines.
func TestParentTitleStripsTheVariant(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"Dual Name Plank - RED / NO LIGHT", "Dual Name Plank"},
		{"PREMIUM DUAL NAME PLANK - Pink", "PREMIUM DUAL NAME PLANK"},
		{"Single Name Plank - White / NO LIGHT", "Single Name Plank"},
		// LastIndex, not Index: a product whose OWN name contains " - " keeps
		// it. This one is the Lotus, not the Wall Diya Holder.
		{"Personalized Wall Diya Holder - Lotus - Red", "Personalized Wall Diya Holder - Lotus"},
		// Nothing to strip.
		{"Night Lamp", "Night Lamp"},
		{"  Doggo Bowl  ", "Doggo Bowl"},
	} {
		if got := parentTitle(tc.line); got != tc.want {
			t.Errorf("parentTitle(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}
