package personalise

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Every face committed to the repo must be in the built-in list, or a template
// naming it would be refused for a font every worker has.
func TestBuiltinFontsMatchTheCommittedFiles(t *testing.T) {
	var files []string
	for _, dir := range []string{"../../fonts", "../../scadassets/fonts"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext == ".ttf" || ext == ".otf" {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
	}
	if len(files) < 6 {
		t.Fatalf("found only %d committed fonts", len(files))
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		face, err := ReadFontFace(data)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, fam := range face.Families {
			if !BuiltinFontFamily(fam) {
				t.Errorf("%s names %q, which builtinFontFamilies does not list", f, fam)
			}
		}
	}
}

// No template compiled into Tensor may need an upload: the check must never
// refuse a file that works today.
func TestEmbeddedTemplatesNameOnlyBuiltinFonts(t *testing.T) {
	entries, err := templates.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".scad" {
			continue
		}
		src, err := templates.ReadFile("templates/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if missing := uploadedFamiliesFor(src); len(missing) > 0 {
			t.Errorf("%s names %v, which no worker ships", e.Name(), missing)
		}
	}
}

func TestTemplateFontFamiliesReadsLiteralsAndOneVariable(t *testing.T) {
	src := []byte(`
// font = "Commented Out"
/* text(t, font = "Also Out"); */
name_font = "Ondfuturs";
url = "https://example.com//x"; // a // inside a string is not a comment
text(a, font = name_font);
text(b, font = "Segoe UI:style=Black");
text(c, font="Lobster");
text(d, font = name_font);
module m(font = "Gotham Rounded") { text("x", font = font); }
if (x == name_font) cube(1);
`)
	got := TemplateFontFamilies(src)
	want := []string{"Ondfuturs", "Segoe UI", "Lobster", "Gotham Rounded"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("families = %q, want %q", got, want)
	}
	if missing := uploadedFamiliesFor(src); !reflect.DeepEqual(missing, []string{"Gotham Rounded"}) {
		t.Fatalf("needing an upload = %q", missing)
	}
}

func TestReadFontFaceNamesTheFamilyAndRefusesNonFonts(t *testing.T) {
	data, err := os.ReadFile("../../scadassets/fonts/Ondfuturs-Demo.ttf")
	if err != nil {
		t.Fatal(err)
	}
	face, err := ReadFontFace(data)
	if err != nil || len(face.Families) == 0 || face.Families[0] != "Ondfuturs" || face.Style != "Regular" {
		t.Fatalf("face = %+v, %v", face, err)
	}
	seg, _ := os.ReadFile("../../fonts/seguibl.ttf")
	if f, _ := ReadFontFace(seg); !reflect.DeepEqual(f.Families, []string{"Segoe UI Black", "Segoe UI"}) || f.Style != "Black" {
		t.Errorf("a typographic family is a second name: %+v", f)
	}
	for _, bad := range [][]byte{[]byte("wOF2 not really"), []byte("cube(1);"), nil} {
		if _, err := ReadFontFace(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestUploadedFontsAreStagedOnlyForTheFamiliesATemplateNeeds(t *testing.T) {
	var asked [][]string
	r := NewRenderer("openscad", "", 0).WithFontLoader(func(_ context.Context, fams []string) (map[string][]byte, error) {
		asked = append(asked, fams)
		return map[string][]byte{"abc.ttf": []byte("face")}, nil
	})

	dir := t.TempDir()
	if err := r.stageUploadedFonts(context.Background(), dir, []byte(`text("A", font = "Segoe UI Black");`)); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 0 {
		t.Fatalf("a template naming only built-in fonts asked for uploads: %v", asked)
	}
	if _, err := os.Stat(filepath.Join(dir, fontDirName)); !os.IsNotExist(err) {
		t.Fatal("nothing may be staged for a built-in-only template")
	}

	if err := r.stageUploadedFonts(context.Background(), dir, []byte(`f = "Gotham"; text("A", font = f);`)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(asked, [][]string{{"Gotham"}}) {
		t.Fatalf("asked = %v", asked)
	}
	if b, err := os.ReadFile(filepath.Join(dir, fontDirName, "abc.ttf")); err != nil || string(b) != "face" {
		t.Fatalf("staged face: %q %v", b, err)
	}
}
