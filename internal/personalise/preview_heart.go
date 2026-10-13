package personalise

// The heart that goes in the picture but never on the plate.
//
// A one-heart plank reserves a wide empty margin on its right, and a two-heart
// plank reserves one at each end - MARGIN_R = 75 in dual_one_heart.scad, both
// margins in dnp_two_heart.scad, against MARGIN_L = 20 on the plain variant.
// That space is not decoration and it is not a mistake: it is where a heart is
// fitted after the plank comes off the printer. The heart is a separate piece
// and is NOT printed with the plank, which is why no template draws one and why
// the rendered model - the file the floor slices - must stay exactly as it is.
//
// A customer looking at that model sees a plank with a conspicuous gap at one
// end and no heart, which is not the product they bought. So the heart is added
// to the PICTURE only, composed in when a preview is served and never stored.
//
// It is a DUAL-VIEW heart, built the same way as the plank's lettering: the
// intersection of two heart extrusions, so it reads as a heart from the left
// and from the right, matching a plate whose letter i of NAME_L is swept along
// Y and letter i of NAME_R along X. Both its XZ and YZ silhouettes are hearts;
// seen from above it is a rounded blob. Shipped as the shop drew it, 18 x 18 x
// 38 mm, centred on X and Y with Z spanning -19..+19.

import (
	"embed"
	"fmt"
	"sync"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

//go:embed parts/dual_heart.stl
var parts embed.FS

// previewHeartFile is the shop's own REDHEART.3mf, its mesh taken out and
// written as a binary STL.
//
// STL rather than the 3MF it arrived as, for one reason: that file is a Bambu
// Studio project, and it keeps its mesh in 3D/Objects/object_1.model behind a
// production-extension <components> reference. Read3MF reads 3D/3dmodel.model
// and would find no triangles there at all. Only the geometry matters here -
// the colour is decided below, not by the file - so the mesh was lifted once at
// build time instead of teaching the reader a layout nothing else in Tensor
// writes.
const previewHeartFile = "parts/dual_heart.stl"

var (
	previewHeartOnce sync.Once
	previewHeartMesh orientation.Mesh
	previewHeartErr  error
)

// PreviewHeart is the heart to draw beside a plank, in the plank's own
// coordinate space before it is moved into place.
//
// Parsed once and shared. The mesh is read-only to every caller - placing it
// means building new triangles from these, never moving these - so one copy
// serves every preview, and 1,622 triangles are not worth re-parsing per
// request.
func PreviewHeart() (orientation.Mesh, error) {
	previewHeartOnce.Do(func() {
		data, err := parts.ReadFile(previewHeartFile)
		if err != nil {
			previewHeartErr = fmt.Errorf("read the preview heart: %w", err)
			return
		}
		mesh, err := orientation.LoadSTL(data)
		if err != nil {
			previewHeartErr = fmt.Errorf("parse the preview heart: %w", err)
			return
		}
		if len(mesh.Triangles) == 0 {
			previewHeartErr = fmt.Errorf("the preview heart has no geometry")
			return
		}
		previewHeartMesh = mesh
	})
	return previewHeartMesh, previewHeartErr
}

// HeartsForTemplate is how many hearts a template's plank wears.
//
// Read from the template rather than carried beside the model, because the one
// place the count is already written down for certain is the choice of .scad
// file: templateForHearts picked it from the customer's order, and a plank
// rendered from dnp_two_heart.scad has two hearts whatever else has since been
// recorded about it. Anything unrecognised answers 0 - a product nobody has
// taught this about gets its model drawn plainly, which is the old behaviour.
func HeartsForTemplate(template string) int {
	switch template {
	case templateOneHeart:
		return 1
	case templateTwoHeart:
		return 2
	default:
		return 0
	}
}
