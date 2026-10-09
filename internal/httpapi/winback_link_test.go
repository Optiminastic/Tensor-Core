package httpapi

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

// A real recovery URL from the live store, for checkout #66850294530261.
const liveRecoveryURL = "https://the3dprintingstore.in/39868563611/checkouts/ac/" +
	"hWNGDJJmX6QaE40VVCxtIMRY/recover?key=79deba923c322d82b18764a8483b63bf&locale=en-IN"

const liveHost = "the3dprintingstore.in"

// The exact string the approved template expects as {{1}}: path and query
// after the host, no leading slash. Verified end to end - Meta accepted this
// as a button parameter and Shopify 302s it to the restored cart.
func TestTheButtonParameterIsThePathTheTemplateExpects(t *testing.T) {
	got := buildWinbackLink(liveRecoveryURL, "BACK10", liveHost)
	if got.Why != "" {
		t.Fatalf("refused a good link: %s", got.Why)
	}
	want := "discount/BACK10?redirect=" +
		"%2F39868563611%2Fcheckouts%2Fac%2FhWNGDJJmX6QaE40VVCxtIMRY%2Frecover" +
		"%3Fkey%3D79deba923c322d82b18764a8483b63bf%26locale%3Den-IN"
	if got.Param != want {
		t.Errorf("param\n got %q\nwant %q", got.Param, want)
	}
	if strings.HasPrefix(got.Param, "/") {
		t.Error("no leading slash - the template supplies the host and the slash")
	}
}

// Losing `key` turns the recovery URL into a 404, and the link still looks
// right, so nothing upstream would notice.
func TestTheRecoveryKeyAndLocaleSurviveTheEncoding(t *testing.T) {
	got := buildWinbackLink(liveRecoveryURL, "BACK10", liveHost)
	redirect := got.Param[strings.Index(got.Param, "redirect=")+len("redirect="):]
	decoded, err := url.QueryUnescape(redirect)
	if err != nil {
		t.Fatalf("redirect does not decode: %v", err)
	}
	for _, want := range []string{
		"/39868563611/checkouts/ac/hWNGDJJmX6QaE40VVCxtIMRY/recover",
		"key=79deba923c322d82b18764a8483b63bf",
		"locale=en-IN",
	} {
		if !strings.Contains(decoded, want) {
			t.Errorf("decoded redirect %q lost %q", decoded, want)
		}
	}
}

// Double-encoding gives %252F, which Shopify reads as a literal path segment:
// it drops the redirect and lands the customer on the homepage with the cart
// lost, having tapped a link that "worked".
func TestTheRedirectIsEncodedExactlyOnce(t *testing.T) {
	got := buildWinbackLink(liveRecoveryURL, "BACK10", liveHost)
	if strings.Contains(got.Param, "%252F") || strings.Contains(got.Param, "%253F") {
		t.Errorf("double-encoded: %q", got.Param)
	}
	// One decode must yield a plain path; a second must change nothing more.
	once, _ := url.QueryUnescape(got.Param[strings.Index(got.Param, "redirect=")+9:])
	if !strings.HasPrefix(once, "/") {
		t.Errorf("one decode should give a path, got %q", once)
	}
}

// If the shop changes its primary domain in Shopify, the recovery URL's host
// moves and the approved template's cannot. Refuse rather than send a button
// pointing at the wrong site.
func TestALinkOnAHostTheTemplateCannotSendIsRefused(t *testing.T) {
	other := "https://3d-printing-store-india.myshopify.com/1/checkouts/ac/T/recover?key=K"
	got := buildWinbackLink(other, "BACK10", liveHost)
	if got.Param != "" {
		t.Errorf("sent a link on the wrong host: %q", got.Param)
	}
	for _, want := range []string{"3d-printing-store-india.myshopify.com", liveHost} {
		if !strings.Contains(got.Why, want) {
			t.Errorf("the reason should name both hosts, got %q", got.Why)
		}
	}
}

// A shop whose recovery URLs carry www and whose template does not is the
// same site, not a different one.
func TestWWWIsNotAHostDisagreement(t *testing.T) {
	withWWW := "https://www." + liveHost + "/1/checkouts/ac/T/recover?key=K"
	if got := buildWinbackLink(withWWW, "BACK10", liveHost); got.Param == "" {
		t.Errorf("www should not count as a different host: %s", got.Why)
	}
}

func TestAnEmptyRecoveryURLProducesNoLinkAndSaysSo(t *testing.T) {
	got := buildWinbackLink("", "BACK10", liveHost)
	if got.Param != "" {
		t.Error("no recovery URL means no link")
	}
	if !strings.Contains(got.Why, "no recovery URL") {
		t.Errorf("reason %q", got.Why)
	}
}

func TestNoHostConfiguredIsRefused(t *testing.T) {
	if got := buildWinbackLink(liveRecoveryURL, "BACK10", "  "); got.Param != "" {
		t.Error("without a link host the button cannot be addressed")
	}
}

// A template with no discount in its copy still gets the restored cart.
func TestNoDiscountCodeStillLinksToTheCart(t *testing.T) {
	got := buildWinbackLink(liveRecoveryURL, "", liveHost)
	if got.Why != "" {
		t.Fatalf("refused: %s", got.Why)
	}
	if strings.HasPrefix(got.Param, "discount/") {
		t.Errorf("no code means no /discount/ prefix, got %q", got.Param)
	}
	if !strings.HasPrefix(got.Param, "39868563611/checkouts/") {
		t.Errorf("should be the bare path, got %q", got.Param)
	}
}

// Meta refuses an empty parameter, and a blank billing AND shipping name is a
// real case - abandoned.go says so in its own comment.
func TestEveryTemplateParameterIsNonEmpty(t *testing.T) {
	for _, c := range []shopify.AbandonedCheckout{
		{},
		{CustomerName: "   ", TotalAmount: "", ItemCount: 0},
		{CustomerName: "Tushar", TotalAmount: "948.0", ItemCount: 1},
	} {
		params := winbackBodyParams(c, "BACK10")
		if len(params) != 5 {
			t.Fatalf("got %d params, want 5", len(params))
		}
		for i, p := range params {
			if strings.TrimSpace(p) == "" {
				t.Errorf("%+v: parameter %d is empty, which Meta refuses", c, i+1)
			}
		}
	}
}

// Meta refuses a parameter containing a newline, a tab, or 4+ spaces. A
// merchant can type a line break into a product title.
func TestAParameterWithALineBreakIsFlattened(t *testing.T) {
	c := shopify.AbandonedCheckout{
		CustomerName: "Tushar\nSuthar",
		LineItems:    []shopify.AbandonedLineItem{{Title: "Dual Name\tPlank    with  Light"}},
		TotalAmount:  "948.0", ItemCount: 1,
	}
	for i, p := range winbackBodyParams(c, "BACK10") {
		if strings.ContainsAny(p, "\n\r\t") {
			t.Errorf("parameter %d still has whitespace Meta refuses: %q", i+1, p)
		}
		if strings.Contains(p, "    ") {
			t.Errorf("parameter %d has a run of spaces Meta refuses: %q", i+1, p)
		}
	}
}

// A basket of six personalised planks makes a very long phrase, and template
// parameters have a length limit.
func TestALongProductPhraseIsTruncated(t *testing.T) {
	items := make([]shopify.AbandonedLineItem, 0, 6)
	for _, name := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot"} {
		items = append(items, shopify.AbandonedLineItem{Title: name + " Name Plank with Light"})
	}
	params := winbackBodyParams(shopify.AbandonedCheckout{
		CustomerName: "Tushar", LineItems: items, ItemCount: 6, TotalAmount: "5688.0",
	}, "BACK10")
	if len([]rune(params[1])) > maxProductPhrase+1 {
		t.Errorf("product phrase is %d chars: %q", len(params[1]), params[1])
	}
}

// The count falls back to the line items, then to 1 - never to "0", which
// would read as an empty basket in the message.
func TestTheItemCountNeverReadsAsZero(t *testing.T) {
	got := winbackBodyParams(shopify.AbandonedCheckout{
		LineItems: []shopify.AbandonedLineItem{{Title: "A"}, {Title: "B"}},
	}, "X")
	if got[2] != "2" {
		t.Errorf("count = %q, want the line-item count", got[2])
	}
	if winbackBodyParams(shopify.AbandonedCheckout{}, "X")[2] != "1" {
		t.Error("with nothing to go on the count must still not be 0")
	}
}
