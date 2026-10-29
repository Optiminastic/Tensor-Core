package httpapi

// What a stored part file may be, and what it is stored as.
//
// These files are kept and never opened: the routes are upload, list and
// delete, and nothing downstream reads the table. That is what makes a .stl
// legitimate alongside the sliced plate it was modelled into - it is the
// source mesh for a part, kept with the product instead of in somebody's
// downloads folder.
//
// The pair below is the thing worth pinning. The object key is built from
// plateExt, so a mesh stored under .gcode is a file nobody downloading it can
// open, and a browser handed text/plain for a zip renders it as mojibake.

import "testing"

func TestAPartFileKeepsItsOwnExtension(t *testing.T) {
	for _, tc := range []struct {
		filename, wantExt, wantType string
	}{
		{"diya-cup-purple.gcode", ".gcode", "text/plain"},
		{"diya-cup-purple.3mf", ".3mf", "model/3mf"},
		{"diya-cup.stl", ".stl", "model/stl"},
		// Shouted by whatever exported it. The suffix check is
		// case-insensitive, so the extension has to be too.
		{"PLATE.GCODE", ".gcode", "text/plain"},
		{"Cup.STL", ".stl", "model/stl"},
		// A name that merely mentions a format is not that format. ".3mf" in
		// the middle of a word must not win over the real suffix.
		{"from-3mf-export.stl", ".stl", "model/stl"},
	} {
		if got := plateExt(tc.filename); got != tc.wantExt {
			t.Errorf("plateExt(%q) = %q, want %q", tc.filename, got, tc.wantExt)
		}
		if got := plateContentType(plateExt(tc.filename)); got != tc.wantType {
			t.Errorf("content type for %q = %q, want %q", tc.filename, got, tc.wantType)
		}
	}
}

func TestAPartFileRefusesAnythingElse(t *testing.T) {
	// Not an exhaustive list - the point is that the check is a suffix match
	// against three extensions and not a guess. A .zip accepted here is a file
	// somebody will later expect a printer to take.
	for _, name := range []string{
		"plate.zip", "notes.txt", "model.step", "cup.stl.txt", "", "gcode",
	} {
		if hasPlateExt(name) {
			t.Errorf("hasPlateExt(%q) = true, want false", name)
		}
	}
	for _, name := range []string{"a.gcode", "a.3mf", "a.stl"} {
		if !hasPlateExt(name) {
			t.Errorf("hasPlateExt(%q) = false, want true", name)
		}
	}
}
