package httpapi

import (
	"strings"
	"testing"
)

// The naming the shop asked for: "Seetha Chinna" becomes SEETHA10.
func TestTheCodeIsTheCustomersFirstName(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"Seetha Chinna", "SEETHA10"},
		{"Tushar Suthar", "TUSHAR10"},
		{"  madhavi   Gampa  ", "MADHAVI10"},
		{"Yogesh", "YOGESH10"},
		// Punctuation a code cannot carry: it is typed and read aloud.
		{"Anne-Marie Dubois", "ANNEMARIE10"},
		{"D'Souza Peter", "DSOUZA10"},
	} {
		got := winbackCodeCandidates(tc.name, 3)
		if got[0] != tc.want {
			t.Errorf("%q -> %q, want %q", tc.name, got[0], tc.want)
		}
	}
}

// A very long first name must not become a very long code - somebody may have
// to type it.
func TestALongNameIsShortened(t *testing.T) {
	got := winbackCodeCandidates("Venkatanarasimharajuvaripeta Rao", 1)[0]
	if len(got) > maxCodeNameLen+len(winbackCodeSuffix) {
		t.Errorf("code %q is %d characters", got, len(got))
	}
	if !strings.HasSuffix(got, winbackCodeSuffix) {
		t.Errorf("code %q lost the discount suffix", got)
	}
}

// Shopify holds a code name forever, including after the discount expires, so
// the second Seetha to abandon a cart collides with the first. Every
// candidate after the clean one must differ.
func TestCollisionCandidatesAreUniqueAndStillReadable(t *testing.T) {
	got := winbackCodeCandidates("Seetha Chinna", 3)
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3", len(got))
	}
	if got[0] != "SEETHA10" {
		t.Errorf("the first candidate should be the clean one, got %q", got[0])
	}
	seen := map[string]bool{}
	for _, c := range got {
		if seen[c] {
			t.Errorf("duplicate candidate %q - a retry would hit the same conflict", c)
		}
		seen[c] = true
		if !strings.HasPrefix(c, "SEETHA10") {
			t.Errorf("candidate %q stopped looking like the customer's code", c)
		}
	}
}

// Somebody who reached the contact step with a phone number and no name.
// Shopify gives us whatever they typed, which is sometimes nothing.
func TestAnUnnamedCustomerGetsAUniqueCode(t *testing.T) {
	for _, name := range []string{"", "   ", "\t", "7416586558", "— —"} {
		got := winbackCodeCandidates(name, 3)
		if len(got) != 3 {
			t.Fatalf("%q: got %d candidates", name, len(got))
		}
		seen := map[string]bool{}
		for _, c := range got {
			if seen[c] {
				t.Errorf("%q: duplicate candidate %q", name, c)
			}
			seen[c] = true
			if !strings.HasPrefix(c, "SAVE10") {
				t.Errorf("%q: candidate %q should fall back to the generic code", name, c)
			}
			// No clean "SAVE10" - there is nothing to personalise, so every
			// candidate must already be unique.
			if c == "SAVE10" {
				t.Errorf("%q: an unnamed customer must not get a shareable code", name)
			}
		}
	}
}

// A single letter is not a name worth personalising with: "A10" reads as a
// serial number, not as somebody's own code.
func TestASingleLetterNameFallsBackToTheGenericCode(t *testing.T) {
	got := winbackCodeCandidates("A Kumar", 1)[0]
	if !strings.HasPrefix(got, "SAVE10") {
		t.Errorf("got %q, want the generic code", got)
	}
}

// A name in a script a Latin keyboard cannot type reduces to nothing, and must
// not produce a code that is just the suffix.
func TestANonLatinNameFallsBackRatherThanProducingJustTheSuffix(t *testing.T) {
	got := winbackCodeCandidates("तुषार", 1)[0]
	if got == winbackCodeSuffix || got == "10" {
		t.Fatalf("got %q, which is not a code", got)
	}
	if !strings.HasPrefix(got, "SAVE10") {
		t.Errorf("got %q, want the generic code", got)
	}
}

// These are single-use money-off codes. A guessable one is a code somebody
// else can spend before its owner does.
func TestTheRandomPartIsNotPredictable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c := winbackCodeCandidates("", 1)[0]
		if seen[c] {
			t.Fatalf("repeated code %q within 200 draws", c)
		}
		seen[c] = true
	}
}

// Characters people misread when a code is spoken down a phone or copied off
// a screen.
func TestTheAlphabetAvoidsAmbiguousCharacters(t *testing.T) {
	for _, bad := range []string{"O", "0", "I", "1", "S", "5"} {
		if strings.Contains(codeAlphabet, bad) {
			t.Errorf("%q is in the alphabet and is misread for something else", bad)
		}
	}
}

// The code says 10 and the discount must be ten per cent. Shopify wants the
// fraction, and passing 10 would be a 1000% discount if it were accepted.
func TestTheCodeAndTheDiscountAgree(t *testing.T) {
	if winbackCodeSuffix != "10" || winbackDiscountFraction != 0.10 {
		t.Errorf("code says %q but the discount is %v", winbackCodeSuffix, winbackDiscountFraction)
	}
	if winbackDiscountFraction <= 0 || winbackDiscountFraction >= 1 {
		t.Error("Shopify wants a fraction, not a percentage")
	}
}
