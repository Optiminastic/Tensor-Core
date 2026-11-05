package personalise

import "testing"

// A comment written for a person stays a comment. This is the common case and
// the one that would hurt if it broke: every template in the shop has prose
// in these positions and none of it asked to be configuration.
func TestProseIsNotConfiguration(t *testing.T) {
	for _, note := range []string{
		"", "the left name", "mm, measured at the base",
		"see the lotus file for the cup", "email tech@x.com if unsure",
	} {
		if _, ok := Annotate(Param{Name: "NAME_L", Note: note}); ok {
			t.Fatalf("note %q read as configuration", note)
		}
	}
}

// An address with spaces in it, which is every label this shop uses.
func TestALabelKeepsItsSpaces(t *testing.T) {
	a, ok := Annotate(Param{Name: "ROSE", Note: "@from STEP 4 - Name on 3D Rose"})
	if !ok {
		t.Fatal("not read as configuration")
	}
	if a.FromRaw != "STEP 4 - Name on 3D Rose" {
		t.Fatalf("raw label: %q", a.FromRaw)
	}
	// Normalised the way the importer and the mapping both do it, or it
	// matches nothing - the failure that looks exactly like an order with no
	// personalisation at all.
	if a.From != "step 4 name on 3d rose" {
		t.Fatalf("normalised: %q", a.From)
	}
}

func TestEveryDirectiveOnOneLine(t *testing.T) {
	a, ok := Annotate(Param{
		Name: "HEARTS",
		Note: "How many hearts. @from Heart @count @default 0 @optional @max 2",
	})
	if !ok {
		t.Fatal("not read as configuration")
	}
	if a.From != "heart" {
		t.Fatalf("from: %q", a.From)
	}
	if !a.KindStated || a.Kind != ValueCount {
		t.Fatalf("kind: %v stated=%v", a.Kind, a.KindStated)
	}
	if a.Default != "0" {
		t.Fatalf("default: %q", a.Default)
	}
	if !a.Optional {
		t.Fatal("optional not read")
	}
	if a.Max != 2 {
		t.Fatalf("max: %d", a.Max)
	}
}

// @fixed with no value means "the value already in the file", which is the
// only readable way to state OUT_X = 200 once.
func TestFixedWithoutAValueMeansTheFilesOwn(t *testing.T) {
	a, ok := Annotate(Param{Name: "OUT_X", Default: "200", Note: "@fixed"})
	if !ok {
		t.Fatal("not read as configuration")
	}
	if !a.Fixed || a.FixedValue != "" {
		t.Fatalf("fixed=%v value=%q", a.Fixed, a.FixedValue)
	}
}

func TestFixedCanStateItsOwnValue(t *testing.T) {
	a, _ := Annotate(Param{Name: "OUT_Y", Note: "@fixed 50"})
	if !a.Fixed || a.FixedValue != "50" {
		t.Fatalf("fixed=%v value=%q", a.Fixed, a.FixedValue)
	}
}

// Required is the default, and a file may say so to be explicit without that
// reading as "the author said nothing".
func TestRequiredIsTheDefaultAndCanBeSaid(t *testing.T) {
	a, ok := Annotate(Param{Name: "NAME_L", Note: "@from Name 1 @required"})
	if !ok || a.Optional {
		t.Fatalf("ok=%v optional=%v", ok, a.Optional)
	}
	bare, _ := Annotate(Param{Name: "NAME_L", Note: "@from Name 1"})
	if bare.Optional {
		t.Fatal("absent @optional did not read as required")
	}
}

// A limit Postgres would refuse is dropped here, where the person who typed
// it is still looking at the file, rather than surfacing as a constraint
// violation on save.
func TestAnImpossibleLimitIsIgnored(t *testing.T) {
	for _, note := range []string{"@max 0", "@max 500", "@max -3", "@max lots", "@max"} {
		a, _ := Annotate(Param{Name: "NAME_L", Note: "@from Name 1 " + note})
		if a.Max != 0 {
			t.Fatalf("note %q yielded max %d", note, a.Max)
		}
	}
}

// The directive is useless without an address, and a bare "@from" must not
// read as a configured field pointing at nothing - that would map every order
// to the empty key and match the wrong property on every line.
func TestFromWithNoLabelIsNotConfiguration(t *testing.T) {
	if _, ok := Annotate(Param{Name: "NAME_L", Note: "@from"}); ok {
		t.Fatal("bare @from read as configuration")
	}
	if _, ok := Annotate(Param{Name: "NAME_L", Note: "@from ---"}); ok {
		t.Fatal("@from with no letters read as configuration")
	}
}

// Trailing punctuation is the sentence, not the label.
func TestTheSentenceAroundADirectiveIsNotPartOfIt(t *testing.T) {
	a, _ := Annotate(Param{Name: "NAME_L", Note: "@from Name 1. Keep it short."})
	if a.From != "name 1" {
		t.Fatalf("from: %q", a.From)
	}
}

// An unknown word is prose. Refusing it would make this a liability on files
// that never opted in.
func TestAnUnknownDirectiveIsIgnoredNotRefused(t *testing.T) {
	a, ok := Annotate(Param{Name: "NAME_L", Note: "@see lotus.scad @from Name 1"})
	if !ok || a.From != "name 1" {
		t.Fatalf("ok=%v from=%q", ok, a.From)
	}
	if _, ok := Annotate(Param{Name: "NAME_L", Note: "@see lotus.scad"}); ok {
		t.Fatal("a file with only unknown directives read as configured")
	}
}

// Read off a whole file, which is how the suggester consumes it, and only
// from top-level declarations - the customizer rule DeclaredParams enforces.
func TestAWholeFileConfiguresOnlyItsDeclarations(t *testing.T) {
	src := []byte(`
/* [Text] */
NAME_L = "";   // @from Name 1 @max 20
NAME_R = "";   // @from Name 2 @optional
OUT_X  = 200;  // @fixed
PART   = "all";

module plank() {
    INNER = "";  // @from Name 9
}
`)
	got := AnnotatedParams(src)
	if len(got) != 3 {
		t.Fatalf("configured %d variables, want 3: %v", len(got), got)
	}
	if _, ok := got["INNER"]; ok {
		t.Fatal("a variable declared inside a module was offered; -D cannot set it")
	}
	if got["NAME_L"].Max != 20 || got["NAME_R"].Optional != true || !got["OUT_X"].Fixed {
		t.Fatalf("read back wrongly: %+v", got)
	}
}
