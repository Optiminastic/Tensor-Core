package meshio

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

// box builds a rectangular solid's triangles, offset so a caller can prove a
// transform moved it. Only the geometry matters here, not watertightness.
func box(x0, y0, z0, dx, dy, dz float64) []orientation.Triangle {
	p := func(i, j, k float64) orientation.Vec3 {
		return orientation.Vec3{X: x0 + i*dx, Y: y0 + j*dy, Z: z0 + k*dz}
	}
	// Two triangles per face, six faces.
	quads := [][4]orientation.Vec3{
		{p(0, 0, 0), p(1, 0, 0), p(1, 1, 0), p(0, 1, 0)},
		{p(0, 0, 1), p(1, 0, 1), p(1, 1, 1), p(0, 1, 1)},
		{p(0, 0, 0), p(1, 0, 0), p(1, 0, 1), p(0, 0, 1)},
		{p(0, 1, 0), p(1, 1, 0), p(1, 1, 1), p(0, 1, 1)},
		{p(0, 0, 0), p(0, 1, 0), p(0, 1, 1), p(0, 0, 1)},
		{p(1, 0, 0), p(1, 1, 0), p(1, 1, 1), p(1, 0, 1)},
	}
	var out []orientation.Triangle
	for _, q := range quads {
		out = append(out,
			orientation.Triangle{V0: q[0], V1: q[1], V2: q[2]},
			orientation.Triangle{V0: q[0], V1: q[2], V2: q[3]},
		)
	}
	return out
}

// plank is a two-colour model shaped like what the renderer produces: a white
// base with coloured lettering sitting on top of it, NOT at the origin.
func plank(letterColour string) []Part {
	return []Part{
		{Name: "Plate", Colour: "#FFFFFF", Triangles: box(0, 0, 0, 200, 50, 2)},
		{Name: "Text", Colour: letterColour, Triangles: box(20, 10, 2, 160, 30, 38)},
	}
}

// Write then read must give back the same colours and the same geometry. If this
// drifts, a merged plate silently loses the colours the customer ordered.
func TestWriteRead3MFRoundTrip(t *testing.T) {
	parts := plank("#E4002B")
	data, err := Write3MF(parts)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := Read3MF(data)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(parts) {
		t.Fatalf("read %d parts, wrote %d", len(got), len(parts))
	}
	for i, p := range parts {
		if got[i].Colour != p.Colour {
			t.Errorf("part %d colour = %q, want %q", i, got[i].Colour, p.Colour)
		}
		if got[i].Name != p.Name {
			t.Errorf("part %d name = %q, want %q", i, got[i].Name, p.Name)
		}
		if len(got[i].Triangles) != len(p.Triangles) {
			t.Errorf("part %d has %d triangles, wrote %d",
				i, len(got[i].Triangles), len(p.Triangles))
		}
	}
}

// A bed of four planks must come out as four PRODUCTS, each holding its own
// base and lettering.
//
// This used to assert two objects - every base merged into one, every letter-set
// into another - which was fewer objects and the wrong shape. A slicer arranges
// objects, so that plate came out as four bare bases on one bed and a pile of
// loose letters on the next.
func TestMerge3MFKeepsEachPlankAsOneProduct(t *testing.T) {
	var models []PlacedModel
	for i := range 4 {
		models = append(models, PlacedModel{
			Name:      fmt.Sprintf("11460%d", i),
			Parts:     plank("#0047AB"),
			XOffsetMM: 10,
			YOffsetMM: 10 + float64(i)*60,
		})
	}
	data, _, err := Merge3MF(models)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	model := entryOf(t, data, modelPath)
	// Four assemblies, one per plank, and four build items placing them.
	if got := strings.Count(model, "<components>"); got != 4 {
		t.Errorf("plate has %d assemblies, want 4 - one per plank", got)
	}
	if got := strings.Count(model, "<item objectid="); got != 4 {
		t.Errorf("plate places %d items, want 4 - one per plank", got)
	}

	// Still only two filaments across the whole bed: extruders are assigned per
	// part, so keeping planks separate does not multiply the slots.
	var proj struct {
		Colour []string `json:"filament_colour"`
	}
	if err := json.Unmarshal([]byte(entryOf(t, data, projectSettingsPath)), &proj); err != nil {
		t.Fatalf("project settings: %v", err)
	}
	if len(proj.Colour) != 2 {
		t.Errorf("plate declares %d filament slots, want 2", len(proj.Colour))
	}

	// Every plank's own two parts are present.
	parts, err := Read3MF(data)
	if err != nil {
		t.Fatalf("read merged: %v", err)
	}
	if len(parts) != 8 {
		t.Errorf("plate has %d meshes, want 8 - a base and a lettering per plank", len(parts))
	}
}

// The point of merging per MODEL rather than per part: a plank's lettering has
// to keep its position relative to its own base. Normalising the two separately
// would drop both onto the origin and leave the text lying beside the plank.
func TestMerge3MFKeepsPartsAlignedWithinAModel(t *testing.T) {
	// The text starts 20mm in and 2mm up from the base's own corner. After
	// placement at (100, 200) the base must sit exactly there, and the text must
	// still be 20mm in and 2mm up from it.
	data, _, err := Merge3MF([]PlacedModel{{
		Parts: plank("#0047AB"), XOffsetMM: 100, YOffsetMM: 200,
	}})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	parts, err := Read3MF(data)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	corner := map[string][3]float64{}
	for _, p := range parts {
		mn, _ := bounds(toTriples(p.Triangles))
		corner[p.Colour] = [3]float64{mn.X, mn.Y, mn.Z}
	}
	base, text := corner["#FFFFFF"], corner["#0047AB"]

	for _, c := range []struct {
		what      string
		got, want float64
	}{
		{"base X", base[0], 100},
		{"base Y", base[1], 200},
		{"base Z", base[2], 0},
		{"text X", text[0], 120},
		{"text Y", text[1], 210},
		{"text Z", text[2], 2},
	} {
		if math.Abs(c.got-c.want) > 1e-6 {
			t.Errorf("%s = %.4f, want %.4f", c.what, c.got, c.want)
		}
	}
}

// A rotated model must turn as one piece, and land inside the bed rather than in
// negative space - the re-normalise after the turn is what guarantees the
// second part.
func TestMerge3MFRotatesTheWholeModel(t *testing.T) {
	data, bbox, err := Merge3MF([]PlacedModel{{
		Parts: plank("#0047AB"), XOffsetMM: 10, YOffsetMM: 10, Rotated: true,
	}})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	// A 200x50 plank turned 90 degrees is 50x200.
	if math.Abs(bbox.XMM-50) > 1e-6 || math.Abs(bbox.YMM-200) > 1e-6 {
		t.Errorf("rotated plate bbox = %.2f x %.2f, want 50 x 200", bbox.XMM, bbox.YMM)
	}
	parts, err := Read3MF(data)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, p := range parts {
		mn, _ := bounds(toTriples(p.Triangles))
		if mn.X < 10-1e-6 || mn.Y < 10-1e-6 {
			t.Errorf("part %q sits at (%.2f, %.2f), before the bed offset (10, 10)",
				p.Name, mn.X, mn.Y)
		}
	}
}

// An uncoloured part must be refused rather than defaulted. The caller knows
// whether a mesh came from the renderer or from an operator's upload; this
// package does not, and a guess here prints a plank in a colour nobody chose.
func TestMerge3MFRejectsAnUncolouredPart(t *testing.T) {
	_, _, err := Merge3MF([]PlacedModel{{
		Parts: []Part{{Name: "Mystery", Triangles: box(0, 0, 0, 10, 10, 10)}},
	}})
	if err == nil {
		t.Fatal("merging a part with no colour must fail")
	}
}

// Read3MF must not invent a colour for a file that never declared one.
func TestRead3MFLeavesAnUnreferencedMaterialEmpty(t *testing.T) {
	const noMaterial = `<?xml version="1.0" encoding="UTF-8"?>
<model unit="millimeter" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02">
<resources><object id="1" type="model"><mesh>
<vertices><vertex x="0" y="0" z="0"/><vertex x="1" y="0" z="0"/><vertex x="0" y="1" z="0"/></vertices>
<triangles><triangle v1="0" v2="1" v3="2"/></triangles>
</mesh></object></resources>
<build><item objectid="1"/></build></model>`

	parts, err := Read3MF(zipOf(t, noMaterial))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1", len(parts))
	}
	if parts[0].Colour != "" {
		t.Errorf("colour = %q, want \"\" so the caller supplies one", parts[0].Colour)
	}
}

// zipOf wraps a bare 3dmodel.model in the package structure Read3MF expects, so
// a hand-written XML fragment can be used as a fixture.
func zipOf(t *testing.T, model string) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	w, err := zw.Create(modelPath)
	if err != nil {
		t.Fatalf("create model entry: %v", err)
	}
	if _, err := w.Write([]byte(model)); err != nil {
		t.Fatalf("write model entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// Bambu Studio decides which filament a part prints in from
// Metadata/model_settings.config, NOT from the 3MF materials extension. Writing
// only displaycolor produced a file that was spec-correct, previewed correctly
// in Tensor, and opened in Bambu Studio as one flat default-coloured body.
func TestWrite3MFCarriesBambuExtruderAssignment(t *testing.T) {
	data, err := Write3MF(plank("#0047AB"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg := entryOf(t, data, bambuSettingsPath)

	// One <object> per product, with a <part> per colour inside it, each on its
	// own extruder. Parts rather than separate objects is what keeps a plank
	// whole - and what stops a slicer dropping the lettering onto the bed.
	for _, want := range []string{
		`<object id="4">`,
		`<part id="2" subtype="normal_part">`,
		`<metadata key="extruder" value="1"/>`,
		`<part id="3" subtype="normal_part">`,
		`<metadata key="extruder" value="2"/>`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("model_settings.config is missing %s\ngot:\n%s", want, cfg)
		}
	}

	// The part ids must be the ids of the objects actually in the model, or
	// Bambu maps the extruder onto nothing.
	model := entryOf(t, data, modelPath)
	for _, want := range []string{`<object id="2"`, `<object id="3"`} {
		if !strings.Contains(model, want) {
			t.Errorf("3dmodel.model is missing %s", want)
		}
	}
}

// A product's parts must be held in ONE object, not placed loose.
//
// This asserts the opposite of what it used to. Flat objects were adopted
// because an assembly appeared to make Bambu sink the lettering into the base -
// but the real cause was merging every plank's lettering into a single object
// whose lowest point floated above the bed, so dropping it to the plate moved
// the letters down. One assembly per PLANK has no such gap: its base already
// sits on the bed. Flat objects, meanwhile, let the slicer arrange a plank's
// base and lettering onto separate plates entirely.
func TestWrite3MFHoldsAProductsPartsTogether(t *testing.T) {
	data, err := Write3MF(plank("#0047AB"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	model := entryOf(t, data, modelPath)

	for _, want := range []string{
		"<components>",
		`<component objectid="2"/>`,
		`<component objectid="3"/>`,
		// The build places the product, not its parts.
		`<item objectid="4"/>`,
	} {
		if !strings.Contains(model, want) {
			t.Errorf("3dmodel.model is missing %s", want)
		}
	}
	if strings.Contains(model, `<item objectid="2"/>`) {
		t.Error("a bare mesh is placed directly; the slicer could arrange a plank's " +
			"base and lettering onto different plates")
	}
}

// Extruder 1 must be the base plate, not whichever colour sorts first. An
// operator loads the body colour first, and sorting hex alphabetically put
// "#1560BD" ahead of "#FFFFFF" and silently swapped them.
func TestMerge3MFPutsTheBaseColourOnExtruderOne(t *testing.T) {
	// A blue that sorts BEFORE white, so alphabetical ordering would fail this.
	data, _, err := Merge3MF([]PlacedModel{{Parts: plank("#1560BD")}})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	parts, err := Read3MF(data)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("got %d objects, want 2", len(parts))
	}
	if parts[0].Colour != "#FFFFFF" {
		t.Errorf("extruder 1 is %s, want the #FFFFFF base plate", parts[0].Colour)
	}
	if parts[1].Colour != "#1560BD" {
		t.Errorf("extruder 2 is %s, want the #1560BD lettering", parts[1].Colour)
	}
}

// entryOf reads one file out of a 3MF archive.
func entryOf(t *testing.T, data []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		defer func() { _ = rc.Close() }()
		out, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(out)
	}
	t.Fatalf("archive has no %s (has %d entries)", name, len(zr.File))
	return ""
}

// An UNSLICED plate must declare its AMS slots, or BambuBuddy's slice dialog
// offers a single filament for the whole bed and both objects print in the same
// colour. Its extractor reads exactly these two arrays and takes the slot count
// from the longer of them.
func TestWrite3MFDeclaresFilamentSlots(t *testing.T) {
	data, err := Write3MF(plank("#0047AB"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg := entryOf(t, data, projectSettingsPath)

	var got struct {
		Type   []string `json:"filament_type"`
		Colour []string `json:"filament_colour"`
	}
	if err := json.Unmarshal([]byte(cfg), &got); err != nil {
		t.Fatalf("project_settings.config is not valid JSON: %v\ngot: %s", err, cfg)
	}
	// Slot order is the contract: slot 1 is the base plate, slot 2 the
	// lettering. Reversed, every plank prints inside-out.
	if len(got.Colour) != 2 || got.Colour[0] != "#FFFFFF" || got.Colour[1] != "#0047AB" {
		t.Errorf("filament_colour = %v, want [#FFFFFF #0047AB] in that order", got.Colour)
	}
	// A part with no material recorded still has to name a filament type, or
	// the slot count comes out short.
	if len(got.Type) != 2 || got.Type[0] != DefaultFilamentType {
		t.Errorf("filament_type = %v, want two entries defaulting to %s", got.Type, DefaultFilamentType)
	}
}

// The material a part carries reaches the slot declaration, so a bed of
// something other than PLA is not silently reported as PLA.
func TestWrite3MFCarriesPartMaterial(t *testing.T) {
	parts := plank("#0047AB")
	for i := range parts {
		parts[i].Material = "PETG"
	}
	data, err := Write3MF(parts)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	var got struct {
		Type []string `json:"filament_type"`
	}
	if err := json.Unmarshal([]byte(entryOf(t, data, projectSettingsPath)), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for i, m := range got.Type {
		if m != "PETG" {
			t.Errorf("slot %d material = %q, want PETG", i+1, m)
		}
	}
}
