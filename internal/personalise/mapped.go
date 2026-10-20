package personalise

// Building render arguments from a product's own field mapping.
//
// ParamsFromProperties knows that "STEP 4-First Name-" means NAME_L because it
// says so in Go. That is workable for one product family and impossible for a
// second: a keychain has different fields, a photo frame different again, and
// each one would be another branch and another deploy.
//
// This is the same job driven by data instead. A product says which of the
// customer's fields feeds which variable in its .scad, and this turns an
// order's properties into the -D flags that render it.
//
// It stays pure - properties in, arguments out, no database - so the rules
// below are testable without a product, a template or a printer.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/production"
)

// ValueKind is how a mapped answer is READ out of the order and written into
// a -D flag. It decides quoting, and quoting is not cosmetic: an unquoted
// string is an OpenSCAD syntax error, and a quoted number is a string that
// fails every arithmetic comparison the script makes against it in silence.
type ValueKind string

const (
	// ValueString is the customer's answer, quoted, as they typed it.
	ValueString ValueKind = "string"
	// ValueNumber is a value the order already states as a number.
	ValueNumber ValueKind = "number"
	// ValueCount is a number read out of the storefront's WORDING.
	//
	// The storefront does not sell "1"; it sells "1 RED HEART", and renames
	// the option freely. Without this a heart count could be mapped as a
	// string - reaching OpenSCAD as side_hearts="1 RED HEART", which is not a
	// number - or as a number, which refuses it outright. Neither renders the
	// plank, which is why the shop's highest-volume product could not be
	// described in the registry at all.
	//
	// Resolved through CountFromWording, the same function the Go plank path
	// uses, so the two cannot disagree about what "NO HEART" means.
	ValueCount ValueKind = "count"
)

// KindFor reads a stored value_type. An unrecognised one is a string, which is
// the safe reading: it reaches the template quoted and visibly wrong rather
// than unquoted and a syntax error.
func KindFor(valueType string) ValueKind {
	switch ValueKind(strings.TrimSpace(strings.ToLower(valueType))) {
	case ValueNumber:
		return ValueNumber
	case ValueCount:
		return ValueCount
	default:
		return ValueString
	}
}

// Unquoted reports whether this kind reaches OpenSCAD without quotes.
func (k ValueKind) Unquoted() bool { return k == ValueNumber || k == ValueCount }

// FieldMap is one mapped field: the customer's answer feeding one variable.
type FieldMap struct {
	// PropertyKey is NORMALISED, as normalisePropKey produces it and as the
	// registry stores it - "step 4 first name", never "STEP 4-First Name-:".
	PropertyKey string
	// ScadVariable is the variable to set. The template must declare it;
	// DeclaresAll is what checks that, because OpenSCAD accepts an unknown -D
	// in silence and simply never reads it.
	ScadVariable string
	// Kind is how the answer is read and written. The zero value is
	// ValueString, so a caller that sets nothing gets the quoted reading.
	Kind ValueKind
	// Fixed is a value that does not come from the order. When set, the order
	// is not consulted at all and PropertyKey is unused.
	//
	// Not every variable a template needs is the customer's. The plank
	// templates default OUT_X/Y/Z to 0, meaning "natural size, whatever the
	// name needs", and without 200/50/40 a plank renders 377 mm wide - the
	// wrong product, successfully, which is only caught because no bed is
	// that big.
	Fixed string
	// Default is passed when the order does not answer an OPTIONAL field.
	//
	// Different from Fixed, which ignores the order entirely: this is the
	// answer for an order that is silent, not for every order. The heart
	// count is why it exists. The 2-STEP plank sells no heart option at all,
	// so nothing on those lines names one, and the Go path reads that as
	// zero - while the joinery template's own default is 1. Left to the
	// template, every plank sold without the option would grow a heart
	// nobody bought.
	//
	// Empty means "pass nothing", which is the old behaviour and still the
	// right one for a rose the customer did not name.
	Default string
	// Optional lets the order not answer this field. The variable is then not
	// passed at all and the template's own default stands - unless Default
	// says otherwise.
	//
	// Not every mapped field is a plank's name. Two of the seven Soulmate
	// Combos in the database carry no rose name, and holding those orders
	// because a customer skipped an optional box would be the feature's first
	// act on the issues board.
	Optional bool
}

// MissingFieldError names the field an order did not carry.
//
// A type rather than a string so the caller can tell "this order is missing
// something" from "the renderer fell over", and so the message reaching the
// issues board names the field a person has to go and find.
type MissingFieldError struct {
	PropertyKey  string
	ScadVariable string
}

func (e *MissingFieldError) Error() string {
	return fmt.Sprintf("this order carries no %q, which this product needs for %s",
		e.PropertyKey, e.ScadVariable)
}

// MappedArgs turns an order line's properties into OpenSCAD -D arguments.
//
// A required field that the order does not answer stops the render. A model
// built with one missing is a product the customer did not order - a plank
// with one name on it - and it looks like a successful render right up until
// somebody opens the box. Holding the job names the field instead, which is a
// five-second fix; printing it is scrap.
//
// An optional field that the order does not answer is simply not passed, so
// the template's own default stands. It is NOT passed as an empty string:
// `-D TEXT=""` engraves nothing where the .scad might have left the surface
// plain, and those are different objects.
//
// PART is deliberately not set here. It changes per render - the base and the
// lettering are two passes over the same arguments - so the caller adds it.
func MappedArgs(props []production.LineProp, maps []FieldMap) (map[string]string, error) {
	// Indexed once: a product with twelve mapped fields against an order with
	// twenty properties is otherwise 240 comparisons, each normalising the
	// same keys again.
	byKey := make(map[string]string, len(props))
	for _, p := range props {
		if p.Hidden() {
			// Shopify's own plumbing ("_has_gpo"), not something a person
			// entered. Mapping one would be mapping an implementation detail
			// of the storefront's options app.
			continue
		}
		key := NormaliseKey(p.Name)
		// First wins, matching lookupRaw: a line carrying the same label twice
		// is the storefront repeating itself, and the first is what the
		// customer saw at the top of the form.
		if _, seen := byKey[key]; !seen {
			byKey[key] = strings.TrimSpace(p.Value)
		}
	}

	args := make(map[string]string, len(maps))
	for _, m := range maps {
		if m.Fixed != "" {
			// Still typed: a fixed string is quoted and a fixed number is
			// not, for the same reason a mapped one is. A quoted 200 would
			// fail every arithmetic comparison the script makes against it.
			if m.Kind.Unquoted() {
				// A fixed count is checked as a plain number, not through the
				// wording reader: it is written by whoever configured the
				// product, not by the storefront, so "2" is what it should
				// say and "2 PINK HEARTS" in that box is a mistake worth
				// naming rather than quietly interpreting.
				if _, err := strconv.ParseFloat(m.Fixed, 64); err != nil {
					return nil, fmt.Errorf("%s is set to %q, which is not a number",
						m.ScadVariable, m.Fixed)
				}
				args[m.ScadVariable] = m.Fixed
			} else {
				args[m.ScadVariable] = Quote(m.Fixed)
			}
			continue
		}
		value, ok := byKey[NormaliseKey(m.PropertyKey)]
		if !ok || value == "" {
			// Absent and blank are the same thing to a renderer, and a blank
			// is the commoner one: the storefront sends the property with an
			// empty value when a customer skips a box rather than omitting it.
			if m.Optional {
				if m.Default == "" {
					continue
				}
				// The configured answer for a silent order. Typed exactly as
				// a mapped value is - a count default is read through the
				// same wording reader, so "no heart" in that box means what
				// it means everywhere else.
				written, err := writeValue(m, m.Default)
				if err != nil {
					return nil, fmt.Errorf("the default for %s: %w", m.ScadVariable, err)
				}
				args[m.ScadVariable] = written
				continue
			}
			return nil, &MissingFieldError{
				PropertyKey: m.PropertyKey, ScadVariable: m.ScadVariable,
			}
		}
		written, err := writeValue(m, value)
		if err != nil {
			return nil, fmt.Errorf("%w for %s and this order says %q",
				err, m.PropertyKey, value)
		}
		args[m.ScadVariable] = written
	}
	return args, nil
}

// writeValue turns one answer into the text that follows `-D name=`.
//
// Shared by the mapped value and the default so the two cannot be typed
// differently: a count default of "no heart" has to mean zero exactly as a
// customer's "NO HEART" does, and a number default that is not a number has
// to be refused exactly as a customer's would be.
func writeValue(m FieldMap, value string) (string, error) {
	switch m.Kind {
	case ValueNumber:
		// Refused rather than passed through. OpenSCAD would take
		// `-D HEARTS=two` as an undefined variable reference and render
		// something, exit 0, and leave nobody any the wiser.
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return "", fmt.Errorf("%s expects a number", m.ScadVariable)
		}
		return value, nil
	case ValueCount:
		// "1 RED HEART" is the storefront's wording for 1. Wording that names
		// no number at all is refused for the same reason a non-number is: a
		// count guessed from an option nobody anticipated renders the wrong
		// product and exits 0.
		n, err := CountFromWording(value)
		if err != nil {
			return "", fmt.Errorf("%s expects a count", m.ScadVariable)
		}
		return strconv.Itoa(n), nil
	default:
		return Quote(value), nil
	}
}

// MatchesKey reports whether a property label means a given canonical key.
//
// THE RULE THE RENDERER ALREADY USES, exported so that whoever decides a
// customer MAY EDIT a field and whoever decides the model WILL PICK IT UP
// cannot disagree. They disagreed for months: lookupRaw matches a label that
// merely CONTAINS the key - which is what let "First Name on Plank" render as
// a first name - while the personalisation page compared labels for equality,
// so the very same field was drawn read-only with no input box beside it. A
// Dual Name Plank with Light showed the customer both their names and let
// them change neither. Measured 17 Oct 2026.
//
// Whole words, in order: "first name on plank" means "first name", and
// "step 6 whatsapp number" means neither "name" nor "number" on its own.
//
// A caller asking the BROAD question - "is this label any of these fields?" -
// must weigh how specific each key is before trusting a match: a one-word key
// like "name" matches half the catalogue. The renderer gets away with it
// because it asks the narrow question, with one product's key list in hand.
// See httpapi.editableProperty for the other side of that.
func MatchesKey(label, key string) bool {
	normalised := NormaliseKey(label)
	if normalised == key {
		return true
	}
	return containsPhrase(normalised, key)
}

// NormaliseKey reduces a property label to its matchable form: lower case,
// punctuation collapsed to single spaces.
//
// The same rule shopify_import.go applies when it stores a property, and it
// has to be, or a mapping written against the stored key would not match the
// key it was written from. The storefront writes the same field three ways
// across three products - "STEP 4-First Name-", "STEP 2 - First Name-",
// "First Name on Plank" - and edits the punctuation freely, so matching on it
// would break the first time somebody tidied a label.
//
// Letters and digits only: an earlier version of this rule kept a-z and
// silently DELETED uppercase, so "STEP 6 - WhatsApp Number:" became
// "6 hats pp umber" - still a plausible-looking key, and one that would never
// match anything.
func NormaliseKey(key string) string {
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.ToLower(key) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSpace = false
		default:
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}
