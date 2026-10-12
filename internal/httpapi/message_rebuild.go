package httpapi

// Rebuilding what a win-back message said, for the ones sent before Tensor
// started writing it down.
//
// WHAT IS RECOVERABLE AND WHAT IS NOT. The basket is: Shopify keeps an
// abandoned checkout for about thirty days with its line items, its count and
// its total, so the same function the sender used can compute the same four
// values from the same source. The discount code is not stored anywhere, but
// it is not random either - it is the customer's first name and "10", with a
// short suffix only when that name was already taken - so the shop can be
// asked whether that code exists.
//
// THIS IS RECONSTRUCTION AND IT SAYS SO. Everything here is marked
// `reconstructed` on the way out, and the page labels it. A value that cannot
// be recovered is left as the template's own {{n}} rather than filled with
// something plausible: this panel is read while deciding whether to honour a
// discount a customer is quoting, and a confident guess there is a forged
// record.
//
// IT EXPIRES. Once Shopify drops the checkout, these rows go back to showing
// the bare template. That is correct - the information is gone - and it is
// why the sender now records the words at send time instead.

import (
	"context"
	"strings"
	"time"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/integrations/whatsapp"
)

// maxRebuilds bounds the work one page load can ask for.
//
// Each row beyond the shared checkout list costs one more Shopify query for
// its discount code, and this runs while somebody waits for a page. Older
// rows are the ones least likely to still be in Shopify's window anyway.
const maxRebuilds = 40

// rebuildWindowDays is how far back to ask Shopify for checkouts.
//
// Shopify retains abandoned checkouts for about thirty days and silently
// returns nothing older, so asking for more is a longer query for the same
// answer.
const rebuildWindowDays = 30

// rebuilt is one recovered message.
type rebuilt struct {
	Body string
	Link string
}

// rebuildMessages fills in what it can for rows that have no recorded body.
//
// Returns a map keyed by checkout id. A row missing from it could not be
// rebuilt, which the caller renders as the bare template - saying nothing is
// the honest answer when the basket has aged out of Shopify.
//
// EVERY FAILURE IS SILENT AND PARTIAL. This decorates a page whose rows have
// already loaded; a brand with no Shopify token, an expired template, a
// Shopify timeout must each cost the decoration and nothing else.
func (s *Server) rebuildMessages(
	ctx context.Context, slug string, rows []gen.AbandonedCheckoutCall,
) map[string]rebuilt {
	wanted := make([]gen.AbandonedCheckoutCall, 0, len(rows))
	for _, r := range rows {
		if r.Status == "sent" && (r.MessageBody == nil || *r.MessageBody == "") {
			wanted = append(wanted, r)
		}
		if len(wanted) >= maxRebuilds {
			break
		}
	}
	if len(wanted) == 0 {
		return nil
	}

	messenger := s.whatsappFor(ctx, slug)
	if !messenger.Configured() {
		return nil
	}
	tmpl, err := messenger.Template(ctx, "", "")
	if err != nil || strings.TrimSpace(tmpl.Body) == "" {
		return nil
	}

	conn, connErr := s.store.Q.GetConnectionWithToken(ctx,
		gen.GetConnectionWithTokenParams{BrandSlug: slug, Provider: shopifyProvider})
	shop, token, connected := shopifyCredentials(conn, connErr)
	if !connected {
		return nil
	}
	linkHost := s.winbackLinkFor(ctx, slug)

	// ONE list for every row. The alternative - a query per checkout - is the
	// same data fetched forty times, and Shopify rate-limits by cost.
	checkouts, err := s.shopify.ListAbandonedCheckouts(ctx, shop, token, 0,
		time.Now().AddDate(0, 0, -rebuildWindowDays))
	if err != nil {
		return nil
	}
	byID := make(map[string]shopify.AbandonedCheckout, len(checkouts))
	for _, c := range checkouts {
		byID[c.ID] = c
	}

	out := make(map[string]rebuilt, len(wanted))
	for _, r := range wanted {
		checkout, found := byID[r.CheckoutID]
		if !found {
			// Aged out of Shopify's window. Nothing to rebuild from, and
			// nothing invented.
			continue
		}

		code := s.rebuildDiscountCode(ctx, shop, token, r)
		params := winbackBodyParams(checkout, code)
		if code == "" {
			// The one value that could not be recovered stays a gap. Shown as
			// "discount code" rather than as a code somebody might honour.
			params[len(params)-1] = "{{5}}"
		}

		entry := rebuilt{Body: whatsapp.Fill(tmpl.Body, params)}
		if link := buildWinbackLink(checkout.RecoveryURL, code, linkHost); link.Param != "" {
			entry.Link = winbackFullLink(linkHost, link.Param)
		}
		out[r.CheckoutID] = entry
	}
	return out
}

// rebuildDiscountCode asks the shop whether the code this send would have
// minted still exists.
//
// ONLY THE FIRST CANDIDATE IS ASKED FOR, because only the first is
// deterministic. winbackCodeCandidates yields FIRSTNAME10 and then collision
// variants carrying four random letters - generating those again produces
// different names than the sender used, so looking them up would be guessing
// at random strings. A customer whose code collided therefore shows a gap,
// which is the right answer: Tensor cannot know which variant they got.
//
// A CODE FOUND THIS WAY BELONGS TO THIS CUSTOMER even if a later send minted
// it: the name is derived from theirs and nobody else's message could have
// carried it. So a match is trusted without comparing timestamps, which would
// only reject the right code for having been reused.
func (s *Server) rebuildDiscountCode(
	ctx context.Context, shop, token string, r gen.AbandonedCheckoutCall,
) string {
	// An unnamed customer's code is SAVE10 plus five random letters, which is
	// not reproducible at all, so there is nothing to ask for.
	if firstNameForCode(r.CustomerName) == "" {
		return ""
	}
	candidates := winbackCodeCandidates(r.CustomerName, 1)
	if len(candidates) == 0 {
		return ""
	}
	found, err := s.shopify.FindDiscountCode(ctx, shop, token, candidates[0])
	if err != nil {
		return ""
	}
	return found.Code
}
