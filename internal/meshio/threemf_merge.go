package meshio

// Merging placed models into one coloured plate.
//
// MergeBinarySTL already lays parts onto a bed, but STL carries no colour, so a
// bed of white-based planks with red lettering came out as one anonymous grey
// solid - and whoever loaded it had to re-assign every colour by hand. This does
// the same placement and writes a 3MF instead, keeping each object's colour.
//
// The unit of placement is a whole MODEL, not a part. A plank is two meshes that
// only mean anything in the same coordinate space: normalising its base and its
// lettering independently would drop both onto the origin separately and float
// the text off the plank. So one transform is computed across a model's parts
// and applied to all of them.

import (
	"fmt"
	"math"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

// PlacedModel is one model on the bed: its coloured parts, and where the packer
// put it. Rotated matches bedpack.Placement.Rotated - a 90-degree turn about Z
// that the packer used to make it fit.
type PlacedModel struct {
	// Name labels this product in the slicer's object list - the order it came
	// from reads better there than a part colour.
	Name      string
	Parts     []Part
	XOffsetMM float64
	YOffsetMM float64
	Rotated   bool
}

// Merge3MF places every model and writes the union as a single 3MF.
//
// Triangles are regrouped by colour, so the output has one object per distinct
// colour on the bed rather than one per source part. That is what a slicer wants:
// a bed of four planks becomes two objects - every base as one white body, every
// set of lettering as one coloured body - which is two filament assignments
// instead of eight.
//
// Also returns the assembled plate's bounding box, measured off the transformed
// triangles rather than re-derived from the inputs, so it describes what was
// actually written.
func Merge3MF(models []PlacedModel) ([]byte, Bbox, error) {
	if len(models) == 0 {
		return nil, Bbox{}, fmt.Errorf("a plate needs at least one model")
	}

	out := make([]Model, 0, len(models))
	var all [][3]vec
	for i, m := range models {
		placed, err := placeModel(m)
		if err != nil {
			return nil, Bbox{}, err
		}
		// One Model per plank, keeping its own parts together. Grouping every
		// plank's base into one object and every plank's lettering into another
		// was fewer objects and the wrong shape: a slicer arranges OBJECTS, so
		// the bed came out as bare base plates on one plate and loose letters on
		// the next, and dropping the letters object to the bed sank every letter
		// into the base it belonged to.
		parts := make([]Part, 0, len(placed))
		for _, g := range placed {
			parts = append(parts, Part{
				Name:      "Colour " + g.colour,
				Colour:    g.colour,
				Material:  g.material,
				Triangles: fromTriples(g.tris),
			})
			all = append(all, g.tris...)
		}
		name := m.Name
		if strings.TrimSpace(name) == "" {
			name = fmt.Sprintf("Plank %d", i+1)
		}
		out = append(out, Model{Name: name, Parts: parts})
	}
	if len(all) == 0 {
		return nil, Bbox{}, fmt.Errorf("the plate's models have no geometry")
	}

	data, err := WriteModels3MF(out)
	if err != nil {
		return nil, Bbox{}, err
	}
	mn, mx := bounds(all)
	return data, Bbox{XMM: mx.X - mn.X, YMM: mx.Y - mn.Y, ZMM: mx.Z - mn.Z}, nil
}

// colourGroup is one colour's triangles within a single model.
type colourGroup struct {
	colour   string
	material string
	tris     [][3]vec
}

// placeModel applies one model's transform to all of its parts and returns the
// result grouped by colour.
//
// The transform matches MergeBinarySTL exactly - normalise, optionally rotate
// and re-normalise, then translate to the bed offset - but the bounds behind
// each normalise are taken across the model's parts TOGETHER. That is the whole
// difference, and it is the difference between a plank and a plank with its name
// lying beside it.
func placeModel(m PlacedModel) ([]colourGroup, error) {
	groups := make([]colourGroup, 0, len(m.Parts))
	for _, p := range m.Parts {
		if p.Colour == "" {
			return nil, fmt.Errorf("part %q has no colour; the caller must supply one", p.Name)
		}
		if len(p.Triangles) == 0 {
			continue
		}
		groups = append(groups, colourGroup{
			colour: p.Colour, material: p.Material, tris: toTriples(p.Triangles),
		})
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("a placed model has no geometry")
	}

	// One shared origin for the whole model.
	shift(groups, boundsOf(groups))
	if m.Rotated {
		for _, g := range groups {
			rotateZ90(g.tris)
		}
		// Re-normalised after the turn: rotating about Z sends x negative, and
		// the packer's offsets are measured from the bed corner.
		shift(groups, boundsOf(groups))
	}
	for _, g := range groups {
		translate(g.tris, m.XOffsetMM, m.YOffsetMM, 0)
	}
	return groups, nil
}

// boundsOf is the minimum corner across every group's CURRENT triangles.
//
// Recomputed from the groups rather than kept in a combined slice: appending the
// parts into one flat slice copies the vertices, so a later translate on a group
// would not be visible in the copy - and the re-normalise after a rotation would
// silently use pre-rotation bounds.
func boundsOf(groups []colourGroup) vec {
	mn := vec{}
	first := true
	for _, g := range groups {
		gmn, _ := bounds(g.tris)
		if first {
			mn, first = gmn, false
			continue
		}
		mn.X = math.Min(mn.X, gmn.X)
		mn.Y = math.Min(mn.Y, gmn.Y)
		mn.Z = math.Min(mn.Z, gmn.Z)
	}
	return mn
}

// shift moves every group so the model's minimum corner sits at the origin.
func shift(groups []colourGroup, mn vec) {
	for _, g := range groups {
		translate(g.tris, -mn.X, -mn.Y, -mn.Z)
	}
}

// fromTriples is toTriples' inverse: vertex triples back into triangles, which
// is what Part carries.
func fromTriples(tris [][3]vec) []orientation.Triangle {
	out := make([]orientation.Triangle, len(tris))
	for i, t := range tris {
		out[i] = orientation.Triangle{V0: t[0], V1: t[1], V2: t[2]}
	}
	return out
}
