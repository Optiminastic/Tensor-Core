package httpapi

// What a customer may put on a name.
//
// ENGLISH LETTERS AND SPACES. Nothing else: not a digit, not a dash or a full
// stop, not an accented letter, not another script, and above all not an
// emoji. A name is cut into plastic by a machine using one font, and anything
// that font has no glyph for comes out as a hollow box or silently as a gap -
// the shop has 19 orders carrying one already, "AJ❤️" and "ARUN♥️" among them.
//
// SPACES ARE THE ONE EXCEPTION, and they have to be. 183 of the 1,119 names
// on this store have a space in them - "A N SWAMY", "AFNAN HAMNA" - so a rule
// that read "A to Z" literally would turn one name in six away at checkout.
//
// ONLY NAME FIELDS. The same order can carry "Number of Members: 4 MEMBERS",
// which is a real answer with a digit in it. A rule applied to every field
// would make that item unsubmittable.

import (
	"fmt"
	"strings"
)

// isNameField reports whether a property is somewhere a person's name goes.
//
// Matched on the label, because that is the only thing the storefront and
// Tensor agree on: the plank asks "STEP 4-First Name-", a DUOSNAP asks "TOP
// NAME", Family Blocks asks "MAIN NAME" and "NAME 1". A list of exact labels
// would be out of date the next time somebody edits the storefront.
func isNameField(label string) bool {
	return strings.Contains(strings.ToLower(label), "name")
}

// errNotALetter says which field, and says what IS allowed rather than only
// what is not - "invalid characters" leaves a customer guessing which of the
// things they typed was the problem.
func errNotALetter(label string) error {
	return fmt.Errorf("%s can only contain English letters and spaces", label)
}

// validName reports whether every character is an English letter or a space.
//
// A plain byte range, deliberately. An earlier version of this allowed any
// Unicode letter, on the reasoning that refusing other scripts made for a
// narrower product - but the font on the machine is the constraint, not
// Tensor's generosity, and a name it cannot cut is not a name the shop can
// make. One rule, one alphabet, and the customer finds out while typing
// rather than when the plank arrives blank.
//
// This also closes the hole that reasoning opened: a combining mark is not a
// letter but had to be allowed with one, and a variation selector is a
// combining mark - so stripping the heart out of "AJ❤️" left U+FE0F behind,
// invisible in every log and text box and still on its way to a printer.
func validName(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c == ' ', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
			continue
		}
		return false
	}
	return true
}
