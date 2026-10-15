package httpapi

import "testing"

// A name is cut into plastic by a machine using one font. An emoji has no
// glyph in it and prints as a hollow box or as nothing at all - this store
// already has 19 orders carrying one, "AJ❤️" and "ARUN♥️" among them.
func TestANameIsLettersAndSpaces(t *testing.T) {
	cases := map[string]bool{
		"RAKESH":      true,
		"A N SWAMY":   true, // 183 of 1,119 real names have a space
		"AFNAN HAMNA": true,
		"Priya":       true,
		"":            true, // emptiness is caught before this, by errBlankField
		"AJ❤️":        false,
		"ARUN♥️":      false,
		"AJ🫰":         false,
		"RAHUL123":    false,
		"O'BRIEN":     false,
		"ANN-MARIE":   false,
		"R.K.":        false,
		"NAME<b>":     false,
	}
	for value, want := range cases {
		if got := validName(value); got != want {
			t.Errorf("validName(%q) = %v, want %v", value, got, want)
		}
	}
}

// The same order can ask "Number of Members" and be answered "4 MEMBERS".
// A rule applied to every field would make that item unsubmittable.
func TestOnlyNameFieldsAreRestricted(t *testing.T) {
	names := []string{
		"TOP NAME", "BOTTOM NAME", "STEP 4-First Name-", "STEP 5-Second Name",
		"MAIN NAME", "NAME 1", "Name On 3D Rose",
	}
	for _, label := range names {
		if !isNameField(label) {
			t.Errorf("%q should be treated as a name field", label)
		}
	}
	others := []string{"Number of Members", "BASE COLOUR", "TEXT COLOUR", "SUB TEXT", "PHOTO"}
	for _, label := range others {
		if isNameField(label) {
			t.Errorf("%q is not a name field and must keep taking any answer", label)
		}
	}
}
