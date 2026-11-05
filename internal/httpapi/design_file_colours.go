package httpapi

// A design file that decides its own colours from the order.
//
// The Desk Name Plank takes one variable, `colors = "Gold & Black"` - letters
// first, stand second - and works out every piece's colour itself. One file
// serves every colourway; the order's colour is mapped to `colors` like any
// other field.
//
// Tensor could not use that. It renders a piece at a time as STL, and STL
// carries no colour, so every hex had to be restated in the registry - one
// scheme per SKU, sixteen rows for four colourways, all of them copies of
// what the file already says. And a file self-coloured the old way needs a
// literal per piece (BASE_COLOR = "#..."), which cannot follow an order.
//
// So a file marked "declares its own colours" that has NO such literals is
// rendered once, as a coloured 3MF, and each object's colour is the print's.
// Files that do declare literals, and every product that marks nothing, are
// untouched.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/meshio"
)

// fileDecidesColours reports whether this template's colours come from its
// rendered model rather than from the registry or from literals.
func (s *Server) fileDecidesColours(ctx context.Context, key string) bool {
	if s.renderer == nil || strings.TrimSpace(key) == "" {
		return false
	}
	self, err := s.store.Q.IsSelfColouredTemplate(ctx, key)
	if err != nil || !self {
		return false
	}
	return len(s.declaredPartColours(ctx, key)) == 0
}

// renderInFileColours builds the print model in one pass, coloured by the
// file.
//
// plan.Args is the order's mapping with no PART, so the file renders every
// piece - `SEL = PART != "" ? PART : piece` falls through to "all" - each as
// its own object in its own colour.
func (s *Server) renderInFileColours(
	ctx context.Context, plan renderPlan, material string,
) ([]byte, error) {
	data, err := s.renderer.Render3MF(ctx, plan.Template, plan.Args)
	if err != nil {
		return nil, fmt.Errorf("render %s in its own colours: %w", plan.Template, err)
	}
	parts, err := meshio.Read3MF(data)
	if err != nil {
		return nil, fmt.Errorf("read %s's coloured model: %w", plan.Template, err)
	}
	for i := range parts {
		// Refused rather than defaulted: a piece with no colour is a piece
		// the slicer cannot assign a filament, and guessing one prints it in
		// a colour nobody chose.
		if parts[i].Colour == "" {
			return nil, fmt.Errorf("%s gave piece %d no colour", plan.Template, i+1)
		}
		parts[i].Material = material
		// Named for its colour, because OpenSCAD's own names ("OpenSCAD
		// Model 2") tell the operator nothing in the slicer's object list.
		parts[i].Name = fmt.Sprintf("piece %d %s", i+1, parts[i].Colour)
	}
	return meshio.Write3MF(parts)
}

// fileColoursProof test-renders a template with its own defaults and reports
// how many objects and distinct colours it produced.
//
// The check behind marking a literal-free file as self-coloured. Two distinct
// colours are required, because a file that never calls color() still exports
// a 3MF - one object in OpenSCAD's default yellow - and accepting that would
// mark a product as coloured when every piece prints the same placeholder.
func (s *Server) fileColoursProof(ctx context.Context, key string) (objects, colours int, err error) {
	// A service with no OpenSCAD cannot prove anything, so the flag is
	// refused exactly as it was before this check existed - never a panic on
	// a nil renderer, which is what the first version of this did.
	if s.renderer == nil || !s.renderer.Available() {
		return 0, 0, errors.New("OpenSCAD is not configured on this service")
	}
	data, err := s.renderer.Render3MF(ctx, key, nil)
	if err != nil {
		return 0, 0, err
	}
	parts, err := meshio.Read3MF(data)
	if err != nil {
		return 0, 0, err
	}
	seen := map[string]bool{}
	for _, p := range parts {
		if p.Colour != "" {
			seen[strings.ToUpper(p.Colour)] = true
		}
	}
	return len(parts), len(seen), nil
}
