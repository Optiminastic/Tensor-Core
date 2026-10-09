package httpapi

// Turning Shopify's recovery URL into the one variable the WhatsApp template
// takes, with the discount already applied.
//
// ONE LINK DOES BOTH. Measured against the live store:
//
//	GET https://the3dprintingstore.in/discount/<CODE>?redirect=<path>
//	  -> 302, Set-Cookie: discount_code=<CODE>, Location: <path>
//
// so a single tap puts the customer in their own restored basket with the
// money already off. The alternative is a code to type, which is a second
// instruction in a message that has one job.
//
// THE HOST IS BAKED INTO AN APPROVED TEMPLATE. The button's URL is
// "https://<host>/{{1}}" and changing it needs Meta to re-approve the
// template, so the variable is path + query only, with no leading slash.

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

// maxProductPhrase bounds the product names said in the message.
//
// The message names EVERY product in the basket, so this has to be generous
// - clipping at sixty characters turned a three-item cart into one item and
// an ellipsis. It is still bounded, because a template parameter has a hard
// limit at Meta and a basket of ten personalised planks would reach it; a
// truncated list is better than a refused send.
const maxProductPhrase = 220

// winbackLink is the WhatsApp URL button's one variable, or the reason there
// is not one.
type winbackLink struct {
	// Param is everything after the host. Empty when no link was possible.
	Param string
	// Why is what to record in the ledger when Param is empty. It is written
	// to a row a person reads, so it names both sides of whatever disagreed.
	Why string
}

// buildWinbackLink turns a Shopify recovery URL into the template's variable.
func buildWinbackLink(recoveryURL, discountCode, templateHost string) winbackLink {
	recoveryURL = strings.TrimSpace(recoveryURL)
	if recoveryURL == "" {
		// Shopify normally fills abandonedCheckoutUrl, but it is a plain
		// string field with no non-null guarantee, and a message whose only
		// button is broken is worse than no message.
		return winbackLink{Why: "Shopify returned no recovery URL for this checkout"}
	}

	parsed, err := url.Parse(recoveryURL)
	if err != nil {
		return winbackLink{Why: fmt.Sprintf("the recovery URL could not be read: %v", err)}
	}

	// REFUSE A HOST DISAGREEMENT, do not rewrite it.
	//
	// Stitching the path onto the template's host sends the customer to that
	// path on a DIFFERENT site: it 404s, or - if the shop owns both domains -
	// restores nothing while looking like a bug in the shop.
	//
	// This is the rule that catches the slowest, worst failure. If the shop
	// changes its primary domain in Shopify, abandonedCheckoutUrl's host moves
	// and the approved template's cannot, so every button would quietly point
	// at the old domain forever. Here it fails loudly on the first send.
	want := trimWWW(strings.TrimSpace(templateHost))
	if want == "" {
		return winbackLink{Why: "no link host is configured, so the button cannot be addressed"}
	}
	if got := trimWWW(parsed.Host); !strings.EqualFold(got, want) {
		return winbackLink{Why: fmt.Sprintf(
			"the recovery link is on %s but the approved template can only send links on %s",
			got, want)}
	}

	// RequestURI keeps the original escaping byte for byte. Re-escaping the
	// path would corrupt `key=`, and a recovery URL without its key is a 404.
	target := parsed.RequestURI()

	code := strings.TrimSpace(discountCode)
	if code == "" {
		// The restored cart, no discount. Correct for a template with no
		// discount in its copy.
		return winbackLink{Param: strings.TrimPrefix(target, "/")}
	}

	// ENCODED EXACTLY ONCE. Double-encoding gives %252F, which Shopify reads
	// as a literal path segment: it drops the redirect and lands the customer
	// on the homepage with their cart lost, having tapped a link that
	// "worked" and raised no error anywhere.
	return winbackLink{
		Param: "discount/" + url.PathEscape(code) + "?redirect=" + url.QueryEscape(target),
	}
}

// trimWWW so a shop whose recovery URLs carry www and whose template does not
// is not treated as a different site.
func trimWWW(host string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www.")
}

// winbackBodyParams is the five values the win-back template says, in the
// template's own order: name, product, quantity, amount, code.
//
// EVERY ONE HAS A NON-EMPTY FALLBACK, because Meta refuses an empty parameter
// outright - and a blank name is common here. abandoned.go takes the customer
// name off the billing address falling back to shipping, and somebody who
// reached the contact step with an email alone has neither.
func winbackBodyParams(c shopify.AbandonedCheckout, discountCode string) []string {
	name := oneLine(c.CustomerName)
	if name == "" {
		name = "there"
	}

	product := oneLine(productPhrase(c))
	if product == "" {
		product = "your order"
	}
	if len(product) > maxProductPhrase {
		product = strings.TrimSpace(product[:maxProductPhrase]) + "…"
	}

	count := c.ItemCount
	if count <= 0 {
		count = len(c.LineItems)
	}
	if count <= 0 {
		count = 1
	}

	amount := oneLine(spokenAmount(c.TotalAmount))
	if amount == "" {
		amount = c.TotalAmount
	}
	if strings.TrimSpace(amount) == "" {
		amount = "0"
	}

	code := oneLine(discountCode)
	if code == "" {
		code = "-"
	}

	return []string{name, product, strconv.Itoa(count), amount, code}
}

// oneLine flattens whitespace.
//
// META REFUSES a template parameter containing a newline, a tab, or four or
// more consecutive spaces. A Shopify product title with a line break in it -
// which a merchant can absolutely type - would otherwise fail every send for
// that basket with an error that names neither the field nor the reason.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
