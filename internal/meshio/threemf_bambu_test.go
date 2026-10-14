package meshio

import (
	"archive/zip"
	"bytes"
	"testing"
)

func zipWith(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, n := range names {
		f, err := w.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Bambu Studio writes a model and a sliced plate with the same extension, and
// the sliced one is what the Export button gives you after pressing Slice.
// Confusing them puts a job on the floor measuring somebody else's bed.
func TestASlicedPlateIsNotAModel(t *testing.T) {
	plate := zipWith(t, "3D/3dmodel.model", "Metadata/plate_1.gcode",
		"Metadata/project_settings.config")
	if !IsSlicedPlate(plate) {
		t.Error("a package carrying toolpaths is a sliced plate")
	}

	// Detected from the contents, so a renamed export is still caught.
	renamed := zipWith(t, "3D/3dmodel.model", "Metadata/plate_1.gcode.md5")
	if !IsSlicedPlate(renamed) {
		t.Error("the checksum beside the toolpaths gives it away too")
	}

	model := zipWith(t, "3D/3dmodel.model", "3D/Objects/object_1.model",
		"Metadata/model_settings.config", "Metadata/project_settings.config")
	if IsSlicedPlate(model) {
		t.Error("a project with no toolpaths is a model, which is what a designer uploads")
	}

	if IsSlicedPlate([]byte("not a zip at all")) {
		t.Error("anything unreadable is left to the loader to refuse")
	}
}
