package meshio

// Writing a plate as one object per product.
//
// This replaces a merge that grouped by COLOUR - every plank's base into one
// object, every plank's lettering into another. Two objects meant two filament
// assignments instead of eight, which was the point, and it was wrong twice
// over:
//
//   - A slicer arranges OBJECTS. Two colour-objects are two independent things,
//     so the bed came out as four bare base plates on one plate and a pile of
//     loose letters on another. A plank is one product and has to reach the
//     slicer as one.
//   - Objects get dropped to the bed. The lettering object's lowest point sits
//     a millimetre above the plate it belongs to, so dropping it sank every
//     letter into the base - the defect that looked like a geometry bug and was
//     really this.
//
// So: one <object> per product, holding its coloured parts as components, and
// each part assigned its own extruder in model_settings.config. The filament
// count is unchanged - two - because extruders are assigned per part, not per
// object.

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
)

// Model is one printable product on the plate - a plank, with its base and its
// lettering as separate coloured parts that print together.
type Model struct {
	// Name appears in the slicer's object list. The orders on the plank read
	// better there than a filename.
	Name  string
	Parts []Part
}

// Write3MF writes a single model, for a job's own print file.
func Write3MF(parts []Part) ([]byte, error) {
	return WriteModels3MF([]Model{{Name: "Plank", Parts: parts}})
}

// WriteModels3MF writes a plate of models.
//
// Every model's parts must already share a coordinate space with each other and
// with the plate - they are written as-is, with no transform. Merge3MF is what
// puts them there.
func WriteModels3MF(models []Model) ([]byte, error) {
	if len(models) == 0 {
		return nil, fmt.Errorf("a 3mf needs at least one model")
	}
	for _, m := range models {
		if len(m.Parts) == 0 {
			return nil, fmt.Errorf("model %q has no parts", m.Name)
		}
		for _, p := range m.Parts {
			if !validHexColour(p.Colour) {
				return nil, fmt.Errorf("part %q has colour %q; want #RRGGBB", p.Name, p.Colour)
			}
			if len(p.Triangles) == 0 {
				return nil, fmt.Errorf("part %q has no geometry", p.Name)
			}
		}
	}

	plan := planObjects(models)
	model, err := plateXML(models, plan)
	if err != nil {
		return nil, err
	}

	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	for _, f := range []struct{ name, body string }{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", relsXML},
		{"3D/3dmodel.model", model},
		// Not declared in [Content_Types].xml, matching what Bambu Studio
		// itself writes - it reads these by path, and adding a declaration it
		// does not expect is a change with no upside.
		{bambuSettingsPath, plateModelSettings(models, plan)},
		{projectSettingsPath, plateProjectSettings(models)},
	} {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// objectPlan assigns the ids and extruders the two XML documents must agree on.
//
// Computed once and shared, because 3dmodel.model and model_settings.config
// address the same things from different files: a part id in one has to be the
// object id in the other, and an extruder number has to match the slot the
// project settings declare. Deriving them twice is how those drift.
type objectPlan struct {
	// PartIDs[m][p] is the object id of model m's part p.
	PartIDs [][]int
	// ModelIDs[m] is the id of the assembly holding model m's parts.
	ModelIDs []int
	// Extruder[colour] is the 1-based slot a colour prints on, shared across
	// every model: two planks of the same colour must use the same extruder or
	// the plate would need one slot per plank.
	Extruder map[string]int
	// Colours is the distinct colours in slot order.
	Colours []string
}

func planObjects(models []Model) objectPlan {
	plan := objectPlan{Extruder: map[string]int{}}
	// Material ids take 1, so object ids start at 2.
	next := 2
	for _, m := range models {
		ids := make([]int, 0, len(m.Parts))
		for _, p := range m.Parts {
			if _, seen := plan.Extruder[p.Colour]; !seen {
				plan.Colours = append(plan.Colours, p.Colour)
				plan.Extruder[p.Colour] = len(plan.Colours)
			}
			ids = append(ids, next)
			next++
		}
		plan.PartIDs = append(plan.PartIDs, ids)
		plan.ModelIDs = append(plan.ModelIDs, next)
		next++
	}
	return plan
}

// plateXML builds 3D/3dmodel.model: one mesh object per part, one assembly per
// model, and one build item per assembly.
func plateXML(models []Model, plan objectPlan) (string, error) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&b, `<model unit="millimeter" xml:lang="en-US" xmlns="%s" xmlns:m="%s">`+"\n",
		nsCore, nsMaterials)
	b.WriteString("<resources>\n")

	// One material per distinct colour, in slot order, so a part's pindex is
	// its extruder minus one.
	fmt.Fprintf(&b, `<basematerials id="%d">`+"\n", baseMaterialsID)
	for _, colour := range plan.Colours {
		// 3MF wants #RRGGBBAA. Opaque, always: transparency is not something a
		// printer can do.
		fmt.Fprintf(&b, `<base name=%s displaycolor="%sFF"/>`+"\n",
			quoteXML("Colour "+colour), strings.ToUpper(colour))
	}
	b.WriteString("</basematerials>\n")

	for mi, m := range models {
		for pi, p := range m.Parts {
			fmt.Fprintf(&b, `<object id="%d" type="model" pid="%d" pindex="%d" name=%s>`+"\n",
				plan.PartIDs[mi][pi], baseMaterialsID, plan.Extruder[p.Colour]-1, quoteXML(p.Name))
			if err := writeMesh(&b, p.Triangles); err != nil {
				return "", err
			}
			b.WriteString("</object>\n")
		}
		// The assembly IS the product. Everything the slicer does to an object -
		// arrange, drop to the bed, move - now happens to a whole plank rather
		// than to its base and its lettering separately.
		fmt.Fprintf(&b, `<object id="%d" type="model" name=%s>`+"\n",
			plan.ModelIDs[mi], quoteXML(m.Name))
		b.WriteString("<components>\n")
		for _, id := range plan.PartIDs[mi] {
			fmt.Fprintf(&b, `<component objectid="%d"/>`+"\n", id)
		}
		b.WriteString("</components>\n</object>\n")
	}

	b.WriteString("</resources>\n<build>\n")
	for _, id := range plan.ModelIDs {
		fmt.Fprintf(&b, `<item objectid="%d"/>`+"\n", id)
	}
	b.WriteString("</build>\n</model>\n")
	return b.String(), nil
}

// plateModelSettings builds Metadata/model_settings.config: which extruder each
// part of each product prints on.
//
// Bambu Studio does not read the 3MF materials extension to decide filaments -
// it reads this. Keyed by the assembly object, with one <part> per component, so
// a plank's base and lettering are two parts of one object rather than two
// objects that a slicer is free to separate.
func plateModelSettings(models []Model, plan objectPlan) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<config>\n")
	for mi, m := range models {
		fmt.Fprintf(&b, `  <object id="%d">`+"\n", plan.ModelIDs[mi])
		fmt.Fprintf(&b, `    <metadata key="name" value=%s/>`+"\n", quoteXML(m.Name))
		fmt.Fprintf(&b, `    <metadata key="extruder" value="%d"/>`+"\n", plan.Extruder[m.Parts[0].Colour])
		for pi, p := range m.Parts {
			fmt.Fprintf(&b, `    <part id="%d" subtype="normal_part">`+"\n", plan.PartIDs[mi][pi])
			fmt.Fprintf(&b, `      <metadata key="name" value=%s/>`+"\n", quoteXML(p.Name))
			fmt.Fprintf(&b, `      <metadata key="extruder" value="%d"/>`+"\n", plan.Extruder[p.Colour])
			b.WriteString("    </part>\n")
		}
		b.WriteString("  </object>\n")
	}
	b.WriteString("</config>\n")
	return b.String()
}

// plateProjectSettings declares one AMS slot per distinct colour.
//
// Per COLOUR, not per part: a bed of four planks needs two slots, not eight.
// Slot order matches the extruder numbering, so slot 1 is whatever colour the
// first part of the first model uses - the plank body, since a plank is built
// base-first.
func plateProjectSettings(models []Model) string {
	plan := planObjects(models)
	material := map[string]string{}
	for _, m := range models {
		for _, p := range m.Parts {
			if material[p.Colour] == "" && strings.TrimSpace(p.Material) != "" {
				material[p.Colour] = p.Material
			}
		}
	}
	types := make([]string, 0, len(plan.Colours))
	colours := make([]string, 0, len(plan.Colours))
	for _, c := range plan.Colours {
		m := material[c]
		if m == "" {
			m = DefaultFilamentType
		}
		types = append(types, m)
		colours = append(colours, strings.ToUpper(c))
	}
	return fmt.Sprintf(`{"filament_type": %s, "filament_colour": %s}`,
		jsonStrings(types), jsonStrings(colours))
}
