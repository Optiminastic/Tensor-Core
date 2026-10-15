package httpapi

// What a customer may put on a name.
//
// Letters and spaces, and nothing else. A name is cut into plastic by a
// machine using one font: a digit is not a name, a dash or a full stop prints
// as a stray mark, and an emoji has no glyph in the typeface at all - it
// renders as a hollow box or silently as nothing, and the customer gets a
// plank with a gap where they expected a heart. The shop has 19 orders with
// one in already, "AJ❤️" and "ARUN♥️" among them.
//
// SPACES ARE LETTERS' BUSINESS TOO, which is why "A to Z only" cannot be read
// literally. 183 of the 1,119 names on this store have a space in them - "A N
// SWAMY", "AFNAN HAMNA" - and a rule that refused them would turn one name in
// six away at checkout. The intent is "no numbers, no symbols", and that is
// what this is.
//
// ONLY NAME FIELDS. The same order can carry "Number of Members: 4 MEMBERS",
// which is a real answer to a real question and has a digit in it. A rule
// applied to every field would make that item unsubmittable.

import (
	"fmt"
	"strings"
	"unicode"
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

// errNotALetter says which field, and says what is allowed rather than only
// what is not - "invalid characters" leaves a customer guessing which of the
// things they typed was the problem.
func errNotALetter(label string) error {
	return fmt.Errorf("%s can only contain letters and spaces", label)
}

// validName reports whether every character is a letter or a space.
//
// unicode.IsLetter rather than a range check on A-Z: the shop already prints
// names in other scripts, and refusing them would be a narrower product
// rather than a safer one. What it excludes is what was asked for - digits,
// punctuation, symbols and emoji - and it excludes them in every script
// rather than only in this one.
func validName(value string) bool {
	for _, r := range value {
		if r == ' ' || unicode.IsLetter(r) {
			continue
		}
		return false
	}
	return true
}
