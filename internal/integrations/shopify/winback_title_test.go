package shopify

// Reading a checkout out of a discount's title.
//
// This is the only durable link between a minted code and the cart it was
// minted for - the code itself says who (SATYAVATHI10) and never which cart.
// Getting it wrong attaches somebody's code to another customer's checkout
// and reports a sale as won by a message that never reached them.

import "testing"

func TestTheTitleNamesTheCheckout(t *testing.T) {
	if got := winbackCheckoutName("Win-back #67129291899093"); got != "#67129291899093" {
		t.Errorf("winbackCheckoutName = %q, want #67129291899093", got)
	}
}

func TestSurroundingSpaceDoesNotHideTheCheckout(t *testing.T) {
	if got := winbackCheckoutName("  Win-back #67127973544149  "); got != "#67127973544149" {
		t.Errorf("winbackCheckoutName = %q, want #67127973544149", got)
	}
}

func TestADiscountSomebodyNamedByHandIsNotAWinback(t *testing.T) {
	// A merchant's own "Win-back October" names no checkout. Guessing would
	// attach a manual campaign code to a real cart and credit the wrong
	// thing with the sale.
	for _, title := range []string{
		"Win-back October",
		"Win-back #",
		"Win-back #not-a-number",
		"Win-back #6712929189909a",
		"Diwali 20%",
		"",
	} {
		if got := winbackCheckoutName(title); got != "" {
			t.Errorf("winbackCheckoutName(%q) = %q, want empty", title, got)
		}
	}
}

func TestThePrefixMustBeAtTheStart(t *testing.T) {
	// "Autumn Win-back #123" is somebody else's naming, not ours.
	if got := winbackCheckoutName("Autumn Win-back #123"); got != "" {
		t.Errorf("winbackCheckoutName = %q, want empty", got)
	}
}
