package personalise

// The check that stops a bad template printing a blank plank.

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// OpenSCAD accepts an unknown -D silently: the assignment is made and the
// template simply never reads it. So a template missing NAME_L renders a plank
// with no name on it and exits 0 - a successful-looking failure that reaches a
// customer. Caught when a template enters the system, where the person who can
// fix it is still there.
func TestMissingTemplateParamsRefusesATemplateThatIgnoresTheNames(t *testing.T) {
	got := MissingTemplateParams([]byte(`
		// A perfectly valid OpenSCAD file that is not a plank template.
		WIDTH = 10;
		cube([WIDTH, WIDTH, WIDTH]);
	`))
	want := []string{"NAME_L", "NAME_R", "OUT_X", "OUT_Y", "OUT_Z", "PART"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("missing = %v, want all six", got)
	}
}

// The shop's own masters must pass, or the check is useless.
//
// Read from the templates on disk rather than a fixture, and discovered rather
// than listed: a fixture would drift from the real files, and a hardcoded list
// would quietly stop covering a template the day somebody adds one.
func TestTheShopsOwnTemplatesPassTheCheck(t *testing.T) {
	paths, err := filepath.Glob("templates/*.scad")
	if err != nil {
		t.Fatalf("glob templates: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no templates found - this test would pass vacuously")
	}
	for _, path := range paths {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if missing := MissingTemplateParams(source); len(missing) > 0 {
			t.Errorf("%s would be refused for missing %v - the check is wrong, "+
				"not the template", filepath.Base(path), missing)
		}
	}
}

// A parameter is declared when it is ASSIGNED at top level, which is what makes
// it overridable with -D. Merely mentioning it is not enough.
func TestDeclaresParam(t *testing.T) {
	for _, c := range []struct {
		name string
		text string
		want bool
	}{
		{"plain assignment", "NAME_L = \"A\";", true},
		{"no space", "NAME_L=\"A\";", true},
		{"indented", "   NAME_L = \"A\";", true},
		{"with a trailing comment", "NAME_L = \"A\"; // the left name", true},
		// Used but never declared: -D would set a variable the template never
		// reads, which is exactly the silent failure being guarded against.
		{"only used", "translate([0,0,0]) text(NAME_L);", false},
		{"only mentioned in a comment", "// NAME_L is the left name", false},
		// A longer name that merely starts the same must not count.
		{"different variable", "NAME_LEFT = \"A\";", false},
		{"empty", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := declaresParam(c.text, "NAME_L"); got != c.want {
				t.Errorf("declaresParam(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}
