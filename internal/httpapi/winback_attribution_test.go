package httpapi

// Whether a recovered cart came back because of us.
//
// The three states are not symmetrical and the asymmetry is the point: "we
// cannot tell" must never be rendered as "not ours", or every checkout
// contacted before the code was recorded would count against the campaign.

import "testing"

func TestACheckoutCarryingOurCodeIsOurs(t *testing.T) {
	// The only way SATYAVATHI10 reaches this cart is the customer following
	// the link we sent her: the code was minted for her checkout alone.
	if got := attributeRecovery("SATYAVATHI10", []string{"SATYAVATHI10"}); got != AttributionOurs {
		t.Errorf("attributeRecovery = %q, want %q", got, AttributionOurs)
	}
}

func TestCaseDoesNotDecideWhoseSaleItWas(t *testing.T) {
	// Shopify upper-cases codes on some surfaces and echoes them as entered
	// on others. A case difference would read as somebody else's sale.
	if got := attributeRecovery("Kiran10", []string{"KIRAN10"}); got != AttributionOurs {
		t.Errorf("attributeRecovery = %q, want %q - case decided the attribution", got, AttributionOurs)
	}
}

func TestARecoveryWithoutOurCodeIsNotOurs(t *testing.T) {
	// They came back and bought, with a code from somewhere else. The sale is
	// real and the credit is not ours.
	if got := attributeRecovery("KIRAN10", []string{"DIWALI20"}); got != AttributionOther {
		t.Errorf("attributeRecovery = %q, want %q", got, AttributionOther)
	}
	if got := attributeRecovery("KIRAN10", nil); got != AttributionOther {
		t.Errorf("attributeRecovery with no codes = %q, want %q", got, AttributionOther)
	}
}

func TestNoCodeSentMeansWeCannotTell(t *testing.T) {
	// EVERY CHECKOUT CONTACTED BEFORE THIS EXISTED LANDS HERE. Reading them
	// as "not ours" would understate the campaign against its own history,
	// which is exactly the error this distinction exists to prevent.
	if got := attributeRecovery("", []string{"DIWALI20"}); got != AttributionUnknown {
		t.Errorf("attributeRecovery = %q, want %q", got, AttributionUnknown)
	}
	if got := attributeRecovery("   ", nil); got != AttributionUnknown {
		t.Errorf("a blank code = %q, want %q", got, AttributionUnknown)
	}
}

func TestAWhitespacedCodeStillMatches(t *testing.T) {
	// Shopify has returned codes with trailing space before now, and a
	// trimmed comparison costs nothing.
	if got := attributeRecovery("KIRAN10", []string{" KIRAN10 "}); got != AttributionOurs {
		t.Errorf("attributeRecovery = %q, want %q", got, AttributionOurs)
	}
}
