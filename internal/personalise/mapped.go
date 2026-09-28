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

// FieldMap is one mapped field: the customer's answer feeding one variable.
type FieldMap struct {
	// PropertyKey is NORMALISED, as normalisePropKey produces it and as the
	// registry stores it - "step 4 first name", never "STEP 4-First Name-:".
	PropertyKey string
	// ScadVariable is the variable to set. The template must declare it;
	// DeclaresAll is what checks that, because OpenSCAD accepts an unknown -D
	// in silence and simply never reads it.
	ScadVariable string
	// Numeric writes the value unquoted. Not cosmetic: an unquoted string is a
	// syntax error, and a quoted number is a string that fails every
	// arithmetic comparison the script makes against it without erroring.
	Numeric bool
	// Optional lets the order not answer this field. The variable is then not
	// passed at all and the template's own default stands.
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
		value, ok := byKey[NormaliseKey(m.PropertyKey)]
		if !ok || value == "" {
			// Absent and blank are the same thing to a renderer, and a blank
			// is the commoner one: the storefront sends the property with an
			// empty value when a customer skips a box rather than omitting it.
			if m.Optional {
				continue
			}
			return nil, &MissingFieldError{
				PropertyKey: m.PropertyKey, ScadVariable: m.ScadVariable,
			}
		}
		if m.Numeric {
			// Refused rather than passed through. OpenSCAD would take
			// `-D HEARTS=two` as an undefined variable reference and render
			// something, exit 0, and leave nobody any the wiser.
			if _, err := strconv.ParseFloat(value, 64); err != nil {
				return nil, fmt.Errorf(
					"%s expects a number for %s and this order says %q",
					m.ScadVariable, m.PropertyKey, value)
			}
			args[m.ScadVariable] = value
			continue
		}
		args[m.ScadVariable] = Quote(value)
	}
	return args, nil
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
