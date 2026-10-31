package httpapi

// Which recovered carts came back BECAUSE we called and messaged.
//
// The shop can already see that a cart was recovered - Shopify sets
// completedAt - but not whether its own outreach is why. Those are different
// numbers and only one of them says whether the win-back campaign is worth
// running.
//
// THE DISCOUNT CODE IS THE PROOF, and it is proof rather than inference. Every
// win-back message mints a code for ONE customer, valid for a short window,
// sent to one phone and existing nowhere else - see mintWinbackCode. A code
// reaches a checkout only by that customer following our link, which applies
// it to the restored cart. Measured on the live store: two checkouts carried
// SATYAVATHI10 and KIRAN10 while completedAt was still null - the cart
// restored with our code applied and the purchase not yet made. So the code
// alone proves the click, and the code with completedAt proves the recovery.
//
// No extra Shopify call, no tracking redirect, and no new approved template.
// The alternative - routing the link through Tensor to count clicks - cannot
// work here anyway: the WhatsApp button's host is baked into the template Meta
// approved, and buildWinbackLink refuses a host it cannot honour.

import "strings"

// Attribution states, as the page reads them.
const (
	// AttributionOurs: recovered, carrying the code we sent. Green.
	AttributionOurs = "ours"
	// AttributionOther: recovered without it. Red. The sale happened and we
	// cannot claim it.
	AttributionOther = "other"
	// AttributionUnknown: no code was ever recorded for this checkout, so
	// there is nothing to compare. Shown as "we cannot tell", NOT as a
	// failure to convert - everything contacted before this existed is here,
	// and reading those as "not ours" would understate the campaign against
	// its own history.
	AttributionUnknown = "unknown"
)

// attributeRecovery decides one checkout's state.
//
// sentCode is the code minted for THIS checkout, empty when none was. codes
// are the discount codes Shopify reports on the checkout now.
func attributeRecovery(sentCode string, codes []string) string {
	sent := strings.TrimSpace(sentCode)
	if sent == "" {
		return AttributionUnknown
	}
	for _, code := range codes {
		// Case-insensitive: Shopify upper-cases codes on some surfaces and
		// echoes them as entered on others, and a case difference here would
		// read as "somebody else's sale".
		if strings.EqualFold(strings.TrimSpace(code), sent) {
			return AttributionOurs
		}
	}
	return AttributionOther
}
