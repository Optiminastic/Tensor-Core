package meshio

// Writing a 3MF with per-object colour.
//
// STL carries no colour at all, and neither does OpenSCAD's own 3MF export -
// its `color()` is a preview aid, which the Dual Name Plank templates say in
// as many words. So a two-colour plank cannot come out of the renderer in one
// piece: the base and the lettering are rendered separately and assembled here,
// each as its own object with its own material.
//
// This is the only code in the repo that writes 3MF. It writes the minimum a
// slicer needs rather than wrapping a library, because the shape is small and
// fixed and a dependency for a handful of XML elements would be the larger cost.
//
// "The minimum" turned out to be TWO things, not one, and that cost a round of
// this being wrong:
//
//   - The core spec's <basematerials displaycolor="...">. This is what the 3MF
//     standard says colour is, and what three.js's loader reads - so it is what
//     makes Tensor's own preview show the right colours.
//   - Bambu Studio's Metadata/model_settings.config, which assigns each part an
//     EXTRUDER index. Bambu (a PrusaSlicer fork) ignores basematerials entirely
//     when deciding which filament a part prints in.
//
// Writing only the first produced a file that was spec-correct, previewed
// correctly in Tensor, and opened in Bambu Studio as one flat default-coloured
// body. Both are written now. See bambuModelSettings.

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

// Part is one coloured object in the finished model.
type Part struct {
	// Name appears in the slicer's object list, so it should say what the part
	// is rather than which file it came from.
	Name string
	// Colour is "#RRGGBB". Anything else is rejected rather than guessed at:
	// a plank printed in a colour nobody chose is worse than one that failed
	// to build.
	Colour string
	// Material is the filament type this part prints in, e.g. "PLA". It reaches
	// the slicer through project_settings.config; empty falls back to
	// DefaultFilamentType rather than failing, since every plank today is PLA
	// and a missing material should not stop a plate being written.
	Material  string
	Triangles []orientation.Triangle
}

// DefaultFilamentType is what a part with no material recorded prints in.
const DefaultFilamentType = "PLA"

// The 3MF namespaces. Pinned to the 2015/02 core spec and the materials
// extension, which is what every slicer in use reads.
const (
	nsCore      = "http://schemas.microsoft.com/3dmanufacturing/core/2015/02"
	nsMaterials = "http://schemas.microsoft.com/3dmanufacturing/material/2015/02"
	relType3D   = "http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"
)

const contentTypesXML = `<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="model" ContentType="application/vnd.ms-package.3dmanufacturing-3dmodel+xml"/>
</Types>`

const relsXML = `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Target="/3D/3dmodel.model" Id="rel0" Type="` + relType3D + `"/>
</Relationships>`

// Object ids. The basematerials group takes 1 and each part's object follows.
const baseMaterialsID = 1

func objectID(i int) int { return i + 2 }

// projectSettingsPath is where BambuBuddy reads a plate's AMS slot configuration
// from - and the reason this file exists at all.
//
// Without it an UNSLICED plate reports no filament requirements, so BambuBuddy's
// slice dialog offers a single filament for the whole bed and both objects print
// in the same colour. Its extractor reads exactly two keys from this file -
// filament_type and filament_colour - and takes the slot count from the longer
// of the two, so this writes those and nothing else.
//
// Writing only those two is deliberate. A fuller project profile would be a
// second, stale copy of print settings competing with the process preset the
// slicer pipeline supplies, and the earlier decision to omit this file entirely
// was an over-correction from that same worry.
const projectSettingsPath = "Metadata/project_settings.config"

// jsonStrings renders a slice as a JSON array. Hand-rolled because the whole
// document is two arrays of short, already-validated tokens - a material name
// and a #RRGGBB - and pulling in a marshaller to emit that would be more code
// than it saves.
func jsonStrings(values []string) string {
	var b strings.Builder
	b.WriteString("[")
	for i, v := range values {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", v)
	}
	b.WriteString("]")
	return b.String()
}

// bambuSettingsPath is where Bambu Studio looks for per-object settings.
const bambuSettingsPath = "Metadata/model_settings.config"

// writeMesh emits one object's vertices and triangles.
//
// 3MF indexes vertices, where STL repeats them per triangle, so identical
// positions are merged. That is not only for size: a slicer treats a mesh whose
// triangles do not share vertices as full of holes, and refuses to slice it.
func writeMesh(b *strings.Builder, tris []orientation.Triangle) error {
	index := make(map[[3]float32]int, len(tris)*3)
	var vertices []([3]float32)

	indexOf := func(v orientation.Vec3) int {
		// Keyed on float32 because that is the precision the STL these came
		// from actually carries; keying on float64 would leave vertices that
		// print identically looking distinct, and the mesh full of holes.
		key := [3]float32{float32(v.X), float32(v.Y), float32(v.Z)}
		if i, ok := index[key]; ok {
			return i
		}
		i := len(vertices)
		index[key] = i
		vertices = append(vertices, key)
		return i
	}

	type tri struct{ a, b, c int }
	faces := make([]tri, 0, len(tris))
	for _, t := range tris {
		faces = append(faces, tri{indexOf(t.V0), indexOf(t.V1), indexOf(t.V2)})
	}

	b.WriteString("<mesh>\n<vertices>\n")
	for _, v := range vertices {
		fmt.Fprintf(b, `<vertex x="%g" y="%g" z="%g"/>`+"\n", v[0], v[1], v[2])
	}
	b.WriteString("</vertices>\n<triangles>\n")
	for _, f := range faces {
		// A degenerate triangle - two corners merged onto one vertex - is
		// dropped rather than written: it has no area, and some slicers treat
		// one as a malformed mesh and reject the whole file.
		if f.a == f.b || f.b == f.c || f.a == f.c {
			continue
		}
		fmt.Fprintf(b, `<triangle v1="%d" v2="%d" v3="%d"/>`+"\n", f.a, f.b, f.c)
	}
	b.WriteString("</triangles>\n</mesh>\n")
	return nil
}

// validHexColour reports whether s is "#RRGGBB".
func validHexColour(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, r := range s[1:] {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// quoteXML renders a string as an escaped, quoted XML attribute value. Part
// names come from product data, so they can contain anything.
func quoteXML(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return `"` + b.String() + `"`
}
