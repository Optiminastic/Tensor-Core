package httpapi

import "testing"

// The rule that decides whether a customer gets the plank they ordered.
//
// Every field is compared, and the heart count is why: JOB-115059-2 was built
// as NAVYA & KRISHNA with 2 hearts against an order for APRAJITA & AJAY with 1,
// and a check that looked only at names would have caught that one by luck
// while missing the next plank whose names happened to match.
func TestCompareRenderParams(t *testing.T) {
	ordered := storedRenderParams{
		Template: "dnp_with_one_heart", Hearts: 1,
		NameLeft: "APRAJITA", NameRight: "AJAY",
	}

	for _, c := range []struct {
		name  string
		built storedRenderParams
		want  modelState
	}{
		{"identical", ordered, modelCorrect},

		// The bug this exists for: a whole other customer's plank.
		{"both names differ", storedRenderParams{
			Template: "dnp_with_one_heart", Hearts: 1,
			NameLeft: "NAVYA", NameRight: "KRISHNA",
		}, modelWrong},

		{"one name differs", storedRenderParams{
			Template: "dnp_with_one_heart", Hearts: 1,
			NameLeft: "APRAJITA", NameRight: "AJAYA",
		}, modelWrong},

		// Right names, wrong plank. The filename could never have shown this.
		{"hearts differ", storedRenderParams{
			Template: "dnp_with_one_heart", Hearts: 2,
			NameLeft: "APRAJITA", NameRight: "AJAY",
		}, modelWrong},

		// A frame and a plank carry the same names and print at different
		// sizes, so the template is part of what "the same model" means.
		{"template differs", storedRenderParams{
			Template: "dnpf_without_heart", Hearts: 1,
			NameLeft: "APRAJITA", NameRight: "AJAY",
		}, modelWrong},

		// Nothing recorded at all, which is what every model built before
		// migration 0073 looks like.
		{"empty", storedRenderParams{}, modelWrong},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, reason := compareRenderParams(c.built, ordered)
			if got != c.want {
				t.Errorf("state = %v, want %v (reason %q)", got, c.want, reason)
			}
			if got == modelWrong && reason == "" {
				t.Error("a mismatch must say what differs; an operator acts on the reason")
			}
			if got == modelCorrect && reason != "" {
				t.Errorf("a match should carry no reason, got %q", reason)
			}
		})
	}
}
