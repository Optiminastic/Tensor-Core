package httpapi

// A piece the order decides the existence of.
//
// Every configured piece is mandatory by default, and renderPieces holds the
// job when one comes back with no geometry. That rule earns its keep: a scheme
// naming a piece the template does not draw renders an empty solid, which
// slices and prints and is simply missing a part, and the only other way to
// find it is by eye on a finished print.
//
// It is wrong for a piece that is genuinely conditional. The Social Mirror's
// blue verified tick is drawn only for an order that bought one, so
// `PART="tick"` on the rest exits 0 and writes no file. Configuring the
// mirror's four pieces under the old rule would fail every order without a
// tick - which is why the mirror is still unconfigured today, printing as the
// plank's two pieces with no red heart and no blue tick at all.
//
// The two rules live here, apart from the fan-out that applies them, because
// both are one line of judgement with a bed of scrap behind getting them
// wrong, and neither is reachable in a test through renderPieces: that needs a
// Server, a database and OpenSCAD.

import (
	"errors"
	"fmt"

	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// emptyPartIsFine reports whether a piece that drew nothing may be left out.
//
// err is the renderer's, or nil for a piece that rendered a well-formed model
// of no triangles. BOTH are "empty": OpenSCAD writes no file when the top
// level object is empty, and writes a valid STL of zero triangles when the
// script produced an object that collapsed. Only the declared-optional piece
// is allowed either.
//
// A non-empty error is never fine, whatever the piece. A timeout, a syntax
// error or an unreadable font on an optional piece is still a broken render,
// and treating it as "the order did not buy this" would print the mirror
// without its tick and report success.
func emptyPartIsFine(part colourPart, err error) bool {
	if !part.Optional {
		return false
	}
	return err == nil || errors.Is(err, personalise.ErrEmptyModel)
}

// builtMeshes drops the optional pieces this order did not buy.
//
// Takes the fan-out's slice with a zero slot per skipped piece and returns the
// rest IN ORDER, because the slicer's object list is read by a person and must
// not shuffle between two renders of the same product.
//
// Filtering on the triangles rather than on Optional: only a skipped slot is
// ever empty here, since every piece that rendered was checked for geometry
// first, and an optional piece that DID render is an ordinary piece.
//
// A model of no pieces is refused. That is not the optional rule doing its
// job - it is every piece being absent at once, which Write3MF would turn into
// a valid file of no objects that slices to an empty plate and reports
// success. The guard those pieces opted out of exists for exactly this.
func builtMeshes(template string, meshes []meshio.Part) ([]meshio.Part, error) {
	// A fresh slice rather than the in-place meshes[:0] idiom: this is handed
	// a slice the fan-out's goroutines wrote, and quietly rearranging it
	// under the caller buys nothing on a handful of pieces.
	built := make([]meshio.Part, 0, len(meshes))
	for _, m := range meshes {
		if len(m.Triangles) > 0 {
			built = append(built, m)
		}
	}
	if len(built) == 0 {
		return nil, fmt.Errorf("%s rendered nothing for any of its %d %s",
			template, len(meshes), plural(len(meshes), "piece"))
	}
	return built, nil
}
