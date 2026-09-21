package httpapi

import "testing"

// The precedence rule is the whole point of the colour map, so it is pinned
// here rather than left to an integration test: the map describes the spool
// physically in a machine, and it has to beat both the synced shelf and the
// built-in table. Getting this order wrong is not a visible failure - it is a
// plate that declares a colour no printer holds, which BambuBuddy then refuses
// with a message about filament rather than about precedence.
func TestPickColourHex(t *testing.T) {
	str := func(s string) *string { return &s }

	cases := []struct {
		name          string
		mapped, shelf *string
		canonical     string
		want          string
		wantOK        bool
	}{
		{
			// The case this table exists for. The shelf carries BambuBuddy's
			// catalogue blue; the operator confirmed the spool actually loaded.
			name:   "the map beats the shelf",
			mapped: str("#2850E0"), shelf: str("#1560BD"),
			canonical: "BLUE", want: "#2850E0", wantOK: true,
		},
		{
			name:   "the map beats the built-in table",
			mapped: str("#2850E0"), shelf: nil,
			canonical: "BLUE", want: "#2850E0", wantOK: true,
		},
		{
			name:   "the shelf beats the built-in table",
			mapped: nil, shelf: str("#0086D6"),
			canonical: "BLUE", want: "#0086D6", wantOK: true,
		},
		{
			name:   "falls back to the built-in table when nothing is recorded",
			mapped: nil, shelf: nil,
			canonical: "BLUE", want: "#1560BD", wantOK: true,
		},
		{
			// A typo in one row must not hold a job when a good answer sits
			// behind it.
			name:   "an unreadable map hex falls through instead of failing",
			mapped: str("not-a-colour"), shelf: str("#0086D6"),
			canonical: "BLUE", want: "#0086D6", wantOK: true,
		},
		{
			name:   "an 8-digit AMS value is normalised, alpha dropped",
			mapped: str("2850E0FF"), shelf: nil,
			canonical: "BLUE", want: "#2850E0", wantOK: true,
		},
		{
			name:   "an unknown colour with nothing recorded is an error, not a default",
			mapped: nil, shelf: nil,
			canonical: "CHARTREUSE", want: "", wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pickColourHex(tc.mapped, tc.shelf, tc.canonical)
			if ok != tc.wantOK {
				t.Fatalf("pickColourHex(%v, %v, %q) ok = %v, want %v",
					deref(tc.mapped), deref(tc.shelf), tc.canonical, ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("pickColourHex(%v, %v, %q) = %q, want %q",
					deref(tc.mapped), deref(tc.shelf), tc.canonical, got, tc.want)
			}
		})
	}
}
