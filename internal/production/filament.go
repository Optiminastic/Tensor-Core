package production

import "strings"

// FilamentType is the slicer's name for a material Tensor records.
//
// Tensor stores what the shop calls a material - "PLA Basics" is the name on
// the shelf and in the design catalogue - while a 3MF's filament_type wants the
// polymer alone. Declaring "PLA Basics" in a plate's AMS slots asks BambuBuddy
// for a filament by a name its own profiles do not use.
//
// Deliberately a small, stated mapping rather than a clever one. The live
// vocabulary is short (see internal/slicing/profiles.go, which knows PLA and
// PA-CF), and a guess here reaches the printer as a spool request: a material
// nobody stocks is a plate that cannot be loaded.
//
// An unrecognised material falls through unchanged rather than being replaced
// by a default. A name this does not know is more likely to be a real filament
// nobody has listed here yet than a mistake, and passing it on lets BambuBuddy
// say so; silently substituting PLA would print the wrong plastic.
func FilamentType(material string) string {
	name := strings.TrimSpace(material)
	if name == "" {
		return ""
	}
	// Matched on the leading word, because the shelf qualifies a polymer with
	// its range - "PLA Basics", "PLA Matte", "PETG HF" - and the qualifier is a
	// product line, not a different plastic.
	switch strings.ToUpper(strings.Fields(name)[0]) {
	case "PLA":
		return "PLA"
	case "PETG":
		return "PETG"
	case "ABS":
		return "ABS"
	case "TPU":
		return "TPU"
	case "PA-CF", "PACF":
		return "PA-CF"
	}
	return name
}
