package httpapi

// The picture is not the print, and the rules that keep them apart.
//
// These guard a failure that does not announce itself: an assembled model is
// a plate with a cup standing off it at ninety degrees, and a bed that
// received one would slice it, print it and waste a plate. Nothing downstream
// would complain, because an assembled model is a perfectly valid mesh.

import (
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func TestThePreviewRoleIsNeverSentToABed(t *testing.T) {
	// partRolesForRender decides what becomes a production job. It filters on
	// two things, and this pins the second: a role is skipped when it has no
	// field mapping, AND the preview role is skipped whatever it maps.
	//
	// The mapping rule alone is what protected this when it was written, and
	// it protects it by accident - the preview borrows the body's mapping and
	// declares none of its own. The day somebody maps a field to the preview
	// role, the accident stops holding and an assembled lotus goes to a
	// printer. isPreviewRole is what makes that impossible instead of
	// unlikely.
	for _, role := range []string{"preview", "PREVIEW", "Preview", " preview "} {
		if !isPreviewRole(role) {
			t.Errorf("isPreviewRole(%q) = false; a preview file would reach a bed", role)
		}
	}

	// And nothing else is swept up with it. "body" prints, and a product that
	// happens to name a role containing the word must still print.
	for _, role := range []string{"body", "", "preview_plate", "frame"} {
		if isPreviewRole(role) {
			t.Errorf("isPreviewRole(%q) = true; a printed piece would stop being made", role)
		}
	}
}

func TestThePreviewAndThePlateAgreeAboutTheColourway(t *testing.T) {
	// Both models resolve their scheme through variantForColourway, which
	// reaches matchVariantByColour - so a customer who bought PWDH-P and
	// chose WHITE gets the white scheme in the picture AND on the plate.
	//
	// Pinned at the level it broke. previewColourParts first looked the
	// variant up by SKU, which is the obvious thing: the bought variant is
	// right until somebody changes their mind, and the personalisation link
	// exists so they can. JOB-990043 then rendered a white plate and a purple
	// picture - the customer would have approved a purple lotus and received
	// a white one, with nothing anywhere disagreeing.
	purple, red, white := uuid.New(), uuid.New(), uuid.New()
	variants := []gen.ListVariantsForProductRow{
		{ID: purple, Name: "Purple", Status: "active"},
		{ID: red, Name: "Red", Status: "active"},
		{ID: white, Name: "White", Status: "active"},
	}

	got := matchVariantByColour(variants, "WHITE")
	if got == nil || *got != white {
		t.Fatalf("chosen WHITE picked %v, want the white variant - "+
			"the picture and the plate must agree", got)
	}

	// A colour this product does not sell has no variant, and the caller
	// falls back to the bought one rather than inventing a scheme.
	if got := matchVariantByColour(variants, "SKY BLUE"); got != nil {
		t.Errorf("an unsold colour picked %v, want none", got)
	}
}
