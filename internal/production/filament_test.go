package production

import "testing"

// The plate's filament_type is a spool request: BambuBuddy matches it against
// what is loaded, so a name its profiles do not use is a slot nothing can fill.
func TestFilamentTypeMapsShelfNamesToSlicerTokens(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		// The shelf qualifies a polymer with its product line. The qualifier is
		// marketing, not chemistry.
		{"PLA Basics", "PLA"},
		{"PLA Matte", "PLA"},
		{"PLA", "PLA"},
		{"pla basics", "PLA"},
		{"  PLA Basics  ", "PLA"},
		{"PETG HF", "PETG"},
		{"PA-CF", "PA-CF"},

		// Nothing recorded means nothing declared: the writer falls back to its
		// own default rather than this inventing one.
		{"", ""},
		{"   ", ""},

		// An unknown material passes through rather than being replaced. A name
		// this does not know is far more likely to be a real filament nobody has
		// listed here yet than a mistake, and letting BambuBuddy refuse it is
		// better than silently printing the wrong plastic.
		{"ASA Aero", "ASA Aero"},
	} {
		if got := FilamentType(c.in); got != c.want {
			t.Errorf("FilamentType(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
