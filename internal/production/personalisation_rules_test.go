package production

import "testing"

func sp(s string) *string { return &s }

func enabledRules() PersonalisationRules {
	return PersonalisationRules{
		Enabled: true,
		Name:    NameRule{Required: true, MaxLength: 10},
		Font:    EnumRule{Required: true, Allowed: []string{"Poppins", "Serif"}},
		Colour:  EnumRule{Allowed: []string{"White", "Black"}},
	}
}

func TestValidateAgainstRules(t *testing.T) {
	t.Run("disabled product is not_required", func(t *testing.T) {
		status, _, issues := ValidateAgainstRules(PersonalisationRules{Enabled: false}, LineItem{})
		if status != PersonalisationNotRequired {
			t.Errorf("status = %q, want %q", status, PersonalisationNotRequired)
		}
		if len(issues) != 0 {
			t.Errorf("issues = %v, want none", issues)
		}
	})

	t.Run("allowed values validate", func(t *testing.T) {
		li := LineItem{PersonalisationName: sp("ARKIT"), PersonalisationFont: sp("Poppins")}
		status, confirms, issues := ValidateAgainstRules(enabledRules(), li)
		if status != PersonalisationValidated {
			t.Errorf("status = %q, want validated (issues: %v)", status, issues)
		}
		if !confirms.AllTrue() {
			t.Errorf("confirms = %+v, want all true", confirms)
		}
	})

	t.Run("disallowed font is rejected", func(t *testing.T) {
		li := LineItem{PersonalisationName: sp("ARKIT"), PersonalisationFont: sp("Comic")}
		status, confirms, issues := ValidateAgainstRules(enabledRules(), li)
		if status != PersonalisationPending {
			t.Errorf("status = %q, want pending", status)
		}
		if confirms.Font {
			t.Error("Font should not be confirmed for a disallowed value")
		}
		if len(issues) == 0 {
			t.Error("expected an issue for the disallowed font")
		}
	})

	t.Run("over-long name is rejected", func(t *testing.T) {
		li := LineItem{PersonalisationName: sp("CHRISTOPHER"), PersonalisationFont: sp("Serif")}
		status, confirms, _ := ValidateAgainstRules(enabledRules(), li)
		if status != PersonalisationPending || confirms.Name {
			t.Errorf("11-char name over a 10 limit should be pending/unconfirmed; got %q %+v", status, confirms)
		}
	})

	t.Run("missing required name is pending", func(t *testing.T) {
		li := LineItem{PersonalisationFont: sp("Poppins")}
		status, confirms, _ := ValidateAgainstRules(enabledRules(), li)
		if status != PersonalisationPending || confirms.Name {
			t.Errorf("missing required name should be pending/unconfirmed; got %q %+v", status, confirms)
		}
	})

	t.Run("optional colour absent still validates", func(t *testing.T) {
		li := LineItem{PersonalisationName: sp("ADA"), PersonalisationFont: sp("Serif")}
		status, confirms, _ := ValidateAgainstRules(enabledRules(), li)
		if status != PersonalisationValidated || !confirms.Colour {
			t.Errorf("absent optional colour should validate; got %q %+v", status, confirms)
		}
	})
}
