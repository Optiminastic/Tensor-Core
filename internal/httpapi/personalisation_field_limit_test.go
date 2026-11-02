package httpapi

// The limit precedence, tested directly.
//
// Every way of getting this wrong is SILENT: a name is quietly truncated in a
// text box and nobody sees a number anywhere. That is how PDHR shipped
// configured for 20 and enforcing 12.

import "testing"

func TestASKUInheritsItsProductsLimit(t *testing.T) {
	// THE REGRESSION. The SKU maps the field and states no limit, which is
	// what every child SKU of PDHR and PWDH did. Before this it fell to the
	// built-in default and the product's number was never read.
	got := resolveFieldLimits(
		map[string]bool{"name": true},
		map[string]int{}, // the SKU states nothing
		map[string]int{"name": 20},
		0,
	)
	if got["name"] != 20 {
		t.Errorf("limit is %d, want the product's 20 - a SKU that states"+
			" nothing must not silently fall to the default", got["name"])
	}
}

func TestASKUsOwnLimitOutranksItsProducts(t *testing.T) {
	// Inheritance must not become an override. A colourway printed from a
	// smaller file states its own and keeps it.
	got := resolveFieldLimits(
		map[string]bool{"name": true},
		map[string]int{"name": 8},
		map[string]int{"name": 20},
		16,
	)
	if got["name"] != 8 {
		t.Errorf("limit is %d, want the SKU's own 8", got["name"])
	}
}

func TestTheFileIsUsedOnlyWhenNobodyConfiguredOne(t *testing.T) {
	got := resolveFieldLimits(
		map[string]bool{"name": true}, map[string]int{}, map[string]int{}, 16)
	if got["name"] != 16 {
		t.Errorf("limit is %d, want the file's declared 16", got["name"])
	}
	// And it loses to a number somebody actually typed.
	got = resolveFieldLimits(
		map[string]bool{"name": true}, map[string]int{}, map[string]int{"name": 20}, 16)
	if got["name"] != 20 {
		t.Errorf("limit is %d, want the configured 20 to outrank the file", got["name"])
	}
}

func TestNoLimitAnywhereLeavesTheFieldToTheDefault(t *testing.T) {
	// Absent, not zero: limitFor falls back to maxRunes on absence, so
	// writing a 0 here would cap every name at nothing.
	got := resolveFieldLimits(
		map[string]bool{"name": true}, map[string]int{}, map[string]int{}, 0)
	if n, ok := got["name"]; ok {
		t.Errorf("limit recorded as %d; it must be ABSENT so the default applies", n)
	}
}

func TestOnlyEditableFieldsGetALimit(t *testing.T) {
	got := resolveFieldLimits(
		map[string]bool{"name": true},
		map[string]int{"name": 20, "hearts": 2},
		map[string]int{}, 0)
	if _, ok := got["hearts"]; ok {
		t.Error("a field the customer cannot edit was given a limit")
	}
}

func TestAFileCanStateItsOwnLimit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		want   int
	}{
		{"MAX_CHARS", "MAX_CHARS = 20;\n", 20},
		{"the MAX_LETTERS spelling", "MAX_LETTERS = 16;\n", 16},
		{"the TEXT_MAX_CHARS spelling", "TEXT_MAX_CHARS = 10;\n", 10},
		{"a trailing comment", "MAX_CHARS = 20; // what the box holds\n", 20},
		// An expression cannot be evaluated here, and a guess truncates a
		// customer's name - so it is ignored and the default stands.
		{"an expression", "MAX_CHARS = BOX_W / 6;\n", 0},
		{"a nonsense value", "MAX_CHARS = 10000;\n", 0},
		{"zero", "MAX_CHARS = 0;\n", 0},
		{"no declaration at all", "MAX_LINES = 3;\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := declaredLimit([]byte(tc.source)); got != tc.want {
				t.Errorf("declaredLimit = %d, want %d", got, tc.want)
			}
		})
	}
}
