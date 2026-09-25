package personalise

import (
	"strings"
	"testing"
)

func TestDeclaredParamsReadsTheCustomizerFormat(t *testing.T) {
	src := []byte(`
// A header comment, not a parameter.
/* [Names] */
NAME_L      = "SUBHANJANA";       // read from the LEFT
NAME_R      = "SUBHANTIKA";
HEART_DROP  = 3;                  // mm the HEART is pushed down
/* [Output size] */
PLATE_T     = 1.48;               // thickness, mm
AUTO_SIZE   = true;               // not a string and not a number
`)
	got := DeclaredParams(src)
	if len(got) != 5 {
		t.Fatalf("declared %d params, want 5: %+v", len(got), got)
	}

	// File order, because the author's grouping is the only guidance anyone
	// has about which of ninety variables matter.
	if got[0].Name != "NAME_L" || got[4].Name != "AUTO_SIZE" {
		t.Errorf("out of file order: %s .. %s", got[0].Name, got[4].Name)
	}
	if got[0].Section != "Names" || got[3].Section != "Output size" {
		t.Errorf("sections = %q / %q", got[0].Section, got[3].Section)
	}
	if got[0].Note != "read from the LEFT" {
		t.Errorf("note = %q; the author's comment is the only documentation these have", got[0].Note)
	}
	if got[1].Note != "" {
		t.Errorf("note = %q, want empty for a line with no comment", got[1].Note)
	}
	if got[0].Default != `"SUBHANJANA"` {
		t.Errorf("default = %q, want the value as written", got[0].Default)
	}

	// Quoting is not cosmetic: an unquoted string is a syntax error and a
	// quoted number silently fails every comparison the script makes.
	for _, c := range []struct {
		i    int
		want ParamType
	}{{0, ParamString}, {2, ParamNumber}, {3, ParamNumber}, {4, ParamOther}} {
		if got[c.i].Type != c.want {
			t.Errorf("%s type = %q, want %q", got[c.i].Name, got[c.i].Type, c.want)
		}
	}
}

func TestDeclaredParamsIgnoresWhatCannotBeSetFromOutside(t *testing.T) {
	src := []byte(`
TOP = 1;
module thing() {
    INNER = 2;
}
if (TOP > 0) {
    NESTED = 3;
}
`)
	got := DeclaredParams(src)
	if len(got) != 1 || got[0].Name != "TOP" {
		// An indented assignment is local to its module or block, so -D would
		// not reach it. Offering it would offer a variable nobody can map.
		t.Errorf("params = %+v, want TOP alone", got)
	}
}

func TestDeclaredParamsTakesTheFirstDeclaration(t *testing.T) {
	// These templates reassign working values further down. The first is what
	// the customizer shows and what -D overrides.
	got := DeclaredParams([]byte("PART = \"all\";\nPART = \"base\";\n"))
	if len(got) != 1 || got[0].Default != `"all"` {
		t.Errorf("params = %+v, want one PART defaulting to \"all\"", got)
	}
}

func TestDeclaredParamsOnTheRealTemplates(t *testing.T) {
	// The templates are embedded, so this reads what production renders from.
	for _, key := range []string{
		"dnp_two_heart", "dual_one_heart", "dnp_with_no_heart", "dnpf_without_heart",
	} {
		t.Run(key, func(t *testing.T) {
			src, err := templates.ReadFile("templates/" + key + ".scad")
			if err != nil {
				t.Fatalf("read template: %v", err)
			}
			params := DeclaredParams(src)
			if len(params) < 40 {
				t.Errorf("declared %d params; these files carry far more", len(params))
			}
			// Every variable Tensor sets with -D must be discoverable, or the
			// mapping UI cannot offer the ones that actually matter.
			if missing := DeclaresAll(src, RequiredTemplateParams); len(missing) > 0 {
				t.Errorf("DeclaresAll missed %v", missing)
			}
			for _, p := range params {
				if strings.TrimSpace(p.Name) == "" {
					t.Errorf("a param parsed with an empty name: %+v", p)
				}
			}
		})
	}
}

func TestDeclaresAllNamesWhatIsMissing(t *testing.T) {
	src := []byte("NAME_L = \"A\";\nPART = \"all\";\n")
	missing := DeclaresAll(src, []string{"NAME_L", "HEARTS", "PART", "COLOUR"})
	if len(missing) != 2 || missing[0] != "HEARTS" || missing[1] != "COLOUR" {
		t.Errorf("missing = %v, want [HEARTS COLOUR]", missing)
	}
	// Case-insensitively, because a mapping is typed by a person and OpenSCAD
	// variable names are not something anyone recalls exactly.
	if got := DeclaresAll(src, []string{"name_l", "part"}); len(got) != 0 {
		t.Errorf("missing = %v, want none", got)
	}
}
