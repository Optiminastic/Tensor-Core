package duosnap

import "testing"

func TestNamesAreWrittenAsTheDesignIs(t *testing.T) {
	for in, want := range map[string]string{
		"SAVITA": "Savita", "shubham": "Shubham", "URS HARSHA": "Urs Harsha", "  raja  ": "Raja", "Sandeep": "Sandeep",
	} {
		if got := NameCase(in); got != want {
			t.Errorf("NameCase(%q) = %q, want %q", in, got, want)
		}
	}
}
