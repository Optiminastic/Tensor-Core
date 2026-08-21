package slicing

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestPlateTemplateNameMatchesPresetNaming(t *testing.T) {
	if got := PlateTemplateName("H2C", 0.4); got != "h2c-0.4.3mf" {
		t.Fatalf("PlateTemplateName(H2C, 0.4) = %q, want h2c-0.4.3mf", got)
	}
	if got := PlateTemplateName("H2C", 0.6); got != "h2c-0.6.3mf" {
		t.Fatalf("PlateTemplateName(H2C, 0.6) = %q, want h2c-0.6.3mf", got)
	}
}

// The H2C is the machine that cannot slice from presets; single-nozzle machines
// must keep the preset path, so they must not accidentally match a template.
func TestHasPlateTemplateOnlyForBundledMachines(t *testing.T) {
	if !HasPlateTemplate("H2C", 0.4) {
		t.Fatal("expected a bundled template for the H2C 0.4 nozzle")
	}
	if HasPlateTemplate("H2S", 0.4) {
		t.Fatal("H2S slices from presets and must not resolve a template")
	}
	if HasPlateTemplate("", 0) {
		t.Fatal("the legacy no-machine path must not resolve a template")
	}
}

func TestBuildPlateProjectRejectsUnknownMachine(t *testing.T) {
	dir := t.TempDir()
	err := BuildPlateProject("P1S", 0.4, writeCubeSTL(t, dir, 10), filepath.Join(dir, "out.3mf"))
	if !errors.Is(err, ErrNoTemplate) {
		t.Fatalf("err = %v, want ErrNoTemplate", err)
	}
}

func TestBuildPlateProjectInjectsMeshAndKeepsPackage(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "project.3mf")
	if err := BuildPlateProject("H2C", 0.4, writeCubeSTL(t, dir, 10), dst); err != nil {
		t.Fatalf("BuildPlateProject: %v", err)
	}

	parts := readZip(t, dst)
	template := readEmbeddedTemplate(t)
	if len(parts) != len(template) {
		t.Fatalf("project has %d parts, template has %d - entries must be preserved", len(parts), len(template))
	}
	for name, want := range template {
		got, ok := parts[name]
		if !ok {
			t.Fatalf("project is missing template part %s", name)
		}
		// Only the geometry and the bed placement may differ; everything else
		// (crucially project_settings.config and every relationship) is settings
		// the slicer needs verbatim.
		if name != meshPath && name != rootModelPath && got != want {
			t.Errorf("part %s was modified but should be copied through", name)
		}
	}

	mesh := parts[meshPath]
	// A cube is 8 corners and 12 facets. STL repeats a shared vertex per facet
	// (36 in total); 3MF indexes them, so a correct injector emits exactly 8.
	if n := strings.Count(mesh, "<vertex "); n != 8 {
		t.Errorf("mesh has %d vertices, want 8 deduplicated corners", n)
	}
	if n := strings.Count(mesh, "<triangle "); n != 12 {
		t.Errorf("mesh has %d triangles, want 12", n)
	}
	if strings.ContainsAny(mesh, "eE") && strings.Contains(mesh, "e+") {
		t.Error("coordinates must not use exponent notation")
	}

	// Bambu centres a mesh on the origin, so a 10mm cube spans -5..5 on each axis.
	for _, want := range []string{`x="-5"`, `x="5"`, `y="-5"`, `z="-5"`, `z="5"`} {
		if !strings.Contains(mesh, want) {
			t.Errorf("mesh is not centred on the origin: missing %s", want)
		}
	}
}

// The build item lifts the centred mesh onto the bed. Getting this wrong sinks
// the plate below Z=0 or floats it, and the slicer rejects the plate as outside
// the print volume (return_code -50).
func TestBuildPlateProjectLiftsMeshOntoBed(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "project.3mf")
	if err := BuildPlateProject("H2C", 0.4, writeCubeSTL(t, dir, 24), dst); err != nil {
		t.Fatalf("BuildPlateProject: %v", err)
	}

	root := readZip(t, dst)[rootModelPath]
	m := regexp.MustCompile(`<item[^>]*transform="([^"]*)"`).FindStringSubmatch(root)
	if m == nil {
		t.Fatal("root model has no build item transform")
	}
	values := strings.Fields(m[1])
	if len(values) != 12 {
		t.Fatalf("transform has %d values, want 12", len(values))
	}
	z, err := strconv.ParseFloat(values[11], 64)
	if err != nil {
		t.Fatalf("parse Z translation %q: %v", values[11], err)
	}
	if math.Abs(z-12) > 1e-9 {
		t.Errorf("Z translation = %v, want 12 (half of a 24mm cube)", z)
	}

	// X and Y are a valid bed position from the template and --arrange owns the
	// XY plane, so they must be left alone.
	templateRoot := readEmbeddedTemplate(t)[rootModelPath]
	tm := regexp.MustCompile(`<item[^>]*transform="([^"]*)"`).FindStringSubmatch(templateRoot)
	want := strings.Fields(tm[1])
	if values[9] != want[9] || values[10] != want[10] {
		t.Errorf("XY translation changed: got %s %s, want %s %s", values[9], values[10], want[9], want[10])
	}
}

// writeCubeSTL writes an axis-aligned binary STL cube of the given size with its
// near corner at the origin, and returns its path.
func writeCubeSTL(t *testing.T, dir string, size float32) string {
	t.Helper()
	c := [8][3]float32{
		{0, 0, 0}, {size, 0, 0}, {size, size, 0}, {0, size, 0},
		{0, 0, size}, {size, 0, size}, {size, size, size}, {0, size, size},
	}
	facets := [12][3]int{
		{0, 2, 1}, {0, 3, 2}, {4, 5, 6}, {4, 6, 7},
		{0, 1, 5}, {0, 5, 4}, {1, 2, 6}, {1, 6, 5},
		{2, 3, 7}, {2, 7, 6}, {3, 0, 4}, {3, 4, 7},
	}

	var b bytes.Buffer
	b.Write(make([]byte, 80))
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(facets)))
	for _, f := range facets {
		_ = binary.Write(&b, binary.LittleEndian, [3]float32{0, 0, 0}) // normal, recomputed on load
		for _, i := range f {
			_ = binary.Write(&b, binary.LittleEndian, c[i])
		}
		_ = binary.Write(&b, binary.LittleEndian, uint16(0))
	}

	path := filepath.Join(dir, "cube.stl")
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatalf("write cube: %v", err)
	}
	return path
}

func readZip(t *testing.T, path string) map[string]string {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer r.Close()
	return collect(t, r.File)
}

func readEmbeddedTemplate(t *testing.T) map[string]string {
	t.Helper()
	data, err := templateFS.ReadFile("templates/" + PlateTemplateName("H2C", 0.4))
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open template: %v", err)
	}
	return collect(t, r.File)
}

func collect(t *testing.T, files []*zip.File) map[string]string {
	t.Helper()
	out := make(map[string]string, len(files))
	for _, f := range files {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		out[f.Name] = string(data)
	}
	return out
}
