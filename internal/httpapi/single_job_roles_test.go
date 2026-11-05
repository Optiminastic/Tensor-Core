package httpapi

import (
	"fmt"
	"testing"
)

// The Desk Name Plank: one part, named for what it is rather than "body".
// It used to collapse to the default part and render through the Dual Name
// Plank template - "a plank needs both names" on a desk plate.
func TestASinglePartWithItsOwnNameKeepsIt(t *testing.T) {
	got := singleJobRoles([]string{"desk plank"})
	if fmt.Sprint(got) != "[desk plank]" {
		t.Fatalf("a single named part became %q; want [desk plank]", got)
	}
}

// Every product that existed before parts could be named prints one part
// called body, and must keep getting the default role exactly as before.
func TestASingleBodyPartIsUnchanged(t *testing.T) {
	for _, role := range []string{"body", "Body", " BODY "} {
		if got := singleJobRoles([]string{role}); len(got) != 1 || got[0] != "" {
			t.Fatalf("%q became %q; a body part must stay the default \"\"", role, got)
		}
	}
}

// A product the registry does not describe is the single job it always was.
func TestNoPartsIsTheSingleDefaultJob(t *testing.T) {
	got := singleJobRoles(nil)
	if len(got) != 1 || got[0] != "" {
		t.Fatalf("no parts became %q; want one default job", got)
	}
}

// A combo is untouched: one job per part.
func TestSeveralPartsAreEachTheirOwnJob(t *testing.T) {
	got := singleJobRoles([]string{"plank", "rose", "keychain"})
	if fmt.Sprint(got) != "[plank rose keychain]" {
		t.Fatalf("a three-part combo became %q", got)
	}
}
