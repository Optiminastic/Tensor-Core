package httpapi

// Job numbers that say which part they print.
//
// A Soulmate Combo is three printed things sold as one line, and they were
// JOB-116456, -2 and -3: unique, and silent about which was the plank and
// which the keychain. An operator holding one of three near-identical numbers
// had to open each one to find out.

import "testing"

func TestAPartNamedJobNumberReadsAsItsPart(t *testing.T) {
	for _, c := range []struct {
		order, role, want string
	}{
		{"T3DPS-116456", "dnp", "JOB-116456-DNP"},
		{"T3DPS-116456", "rose", "JOB-116456-ROSE"},
		{"T3DPS-116456", "keychain", "JOB-116456-KEYCHAIN"},
		// Upper-cased and stripped, so a part somebody named with a space
		// cannot put a second hyphen into an identifier split on hyphens.
		{"T3DPS-116456", "Heart Keychain", "JOB-116456-HEARTKEYCHAIN"},
		{"T3DPS-116456", "part-2", "JOB-116456-PART2"},
		// Nothing usable: the caller numbers it the old way instead.
		{"T3DPS-116456", "   ", ""},
		{"T3DPS-116456", "--", ""},
		// The store's prefix carries a digit of its own, so only the trailing
		// segment can be read - otherwise every order becomes JOB-3.
		{"T3PS-114743", "rose", "JOB-114743-ROSE"},
		{"no-digits-here", "rose", ""},
	} {
		if got := jobNumberForPart(c.order, c.role); got != c.want {
			t.Errorf("jobNumberForPart(%q, %q) = %q, want %q", c.order, c.role, got, c.want)
		}
	}
}

func TestAPartNamedJobNumberStillNamesItsOrder(t *testing.T) {
	// THE ONE THING DOWNSTREAM THAT READS A JOB NUMBER rather than storing
	// it: the plate name, which puts the orders on a bed into the filename.
	// A new suffix format that this could not read would silently drop every
	// combo job from the name of the plate it prints on.
	for _, c := range []struct{ in, want string }{
		{"JOB-116456-DNP", "116456"},
		{"JOB-116456-ROSE", "116456"},
		{"JOB-116456-KEYCHAIN", "116456"},
		{"JOB-116456-ROSE-2", "116456"},
		{"JOB-116456", "116456"},
		{"JOB-116456-2", "116456"},
	} {
		if got := orderNumberFromJobNumber(c.in); got != c.want {
			t.Errorf("orderNumberFromJobNumber(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
