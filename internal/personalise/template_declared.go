package personalise

// Reading a template's own parameters back out of it.
//
// A product is configured by saying "the customer's First Name feeds NAME_L".
// Somebody has to know that NAME_L exists, and the only place that is written
// down is the .scad itself. Typing the name from memory is how a mapping ends
// up pointing at a variable the script does not read - which OpenSCAD accepts
// silently, because an unknown -D is a legal assignment nothing consumes. That
// is the same successful-failure MissingTemplateParams exists to prevent, one
// step earlier.
//
// So this reads the script and offers what it declares.
//
// The format is OpenSCAD's own customizer convention, which these templates
// already follow: top-level assignments, optionally grouped under a
// `/* [Section] */` header, with the author's comment after the value. That
// comment is the most useful thing on screen when choosing between HEART_DROP
// and HEART_SCALE, so it is carried through rather than discarded.

import (
	"regexp"
	"strconv"
	"strings"
)

// ParamType is how a value must be written into a -D flag.
//
// It decides quoting, and quoting is not cosmetic: an unquoted string is an
// OpenSCAD syntax error, and a quoted number is a string that silently fails
// every arithmetic comparison the script makes against it.
type ParamType string

const (
	ParamString ParamType = "string"
	ParamNumber ParamType = "number"
	// ParamOther is a boolean, vector or expression - anything this does not
	// confidently recognise. Reported rather than guessed at, so a mapping UI
	// can show it and let a person decide rather than quoting it wrongly.
	ParamOther ParamType = "other"
)

// Param is one variable a template declares.
type Param struct {
	Name string `json:"name"`
	// Section is the /* [..] */ group it sits under, or "" when ungrouped.
	// Purely for presentation: these files declare ~90 parameters and an
	// ungrouped list of ninety is not a thing anyone can choose from.
	Section string    `json:"section"`
	Type    ParamType `json:"type"`
	// Default is the value as written, so a person can see that PART is
	// "all" rather than guessing at its shape.
	Default string `json:"default"`
	// Note is the author's own trailing comment, verbatim and untrimmed of
	// meaning. It is the documentation these templates actually have.
	Note string `json:"note"`
}

// declarationLine matches the start of a `module foo()` or `function bar()`.
// Everything past the first one is the script computing with its parameters
// rather than declaring them - see DeclaredParams.
var declarationLine = regexp.MustCompile(`^\s*(module|function)\s+[A-Za-z_]`)

// sectionLine matches `/* [Output size] */`, the customizer's group header.
var sectionLine = regexp.MustCompile(`^\s*/\*\s*\[(.+?)\]\s*\*/`)

// assignment matches a top-level `NAME = value;` with an optional trailing
// comment. Anchored at the start of the line deliberately: an assignment
// indented inside a module or an if is local to it and setting it from -D
// would do nothing, so offering it would be offering a variable that cannot
// be mapped.
var assignment = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*([^;]+);\s*(?://+\s*(.*))?$`)

// DeclaredParams lists the parameters a template declares, in file order.
//
// File order, not sorted: the author grouped them, and the grouping is the
// only guidance anyone has about which of fifty variables matter.
//
// Stops at the first module or function, which is OpenSCAD's own customizer
// rule rather than an approximation of it. What follows is the script
// computing WITH its parameters rather than declaring them - `NAME_LN =
// norm(NAME_L);` and `HEART_CH = chr(1);` are working values, and there are
// forty of them in each plank template. Offering one for mapping would be
// worse than useless: -D would set it and the script would reassign it on the
// way past, so the mapping would appear to work and do nothing at all.
func DeclaredParams(source []byte) []Param {
	var (
		out     []Param
		section string
		seen    = map[string]bool{}
	)
	for _, line := range strings.Split(string(source), "\n") {
		if declarationLine.MatchString(line) {
			break
		}
		if m := sectionLine.FindStringSubmatch(line); m != nil {
			section = strings.TrimSpace(m[1])
			continue
		}
		m := assignment.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		name := m[1]
		// First declaration wins. These templates reassign a few variables
		// further down as working values; the first is the one the customizer
		// shows and the one -D overrides.
		if seen[name] {
			continue
		}
		seen[name] = true
		value := strings.TrimSpace(m[2])
		out = append(out, Param{
			Name:    name,
			Section: section,
			Type:    paramTypeOf(value),
			Default: value,
			Note:    strings.TrimSpace(m[3]),
		})
	}
	return out
}

// DeclaresAll reports which of the given variable names the template does not
// declare.
//
// The mapping-aware counterpart to MissingTemplateParams: a product's template
// must declare the variables its own mapping names, which is a different set
// per product and cannot be a package-level constant.
func DeclaresAll(source []byte, names []string) []string {
	declared := map[string]bool{}
	for _, p := range DeclaredParams(source) {
		declared[strings.ToLower(p.Name)] = true
	}
	var missing []string
	for _, n := range names {
		if !declared[strings.ToLower(strings.TrimSpace(n))] {
			missing = append(missing, n)
		}
	}
	return missing
}

// paramTypeOf classifies a written default.
//
// Conservative: anything not plainly a quoted string or a number is reported
// as ParamOther rather than guessed. A wrong guess here is a render that
// either fails to parse or silently compares a string to a number.
func paramTypeOf(value string) ParamType {
	switch {
	case strings.HasPrefix(value, `"`):
		return ParamString
	case isNumeric(value):
		return ParamNumber
	default:
		return ParamOther
	}
}

func isNumeric(value string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return err == nil
}
