package httpapi

// Reconstructing which win-back code went to which checkout.
//
// The code was minted, sent and forgotten: until migration 0138 nothing
// recorded it, so every checkout contacted before that shows no verdict on
// the Recovered tab - correctly, because "we cannot tell" is the truth.
//
// But Shopify kept the link all along. CreateDiscountCode titles every
// win-back "Win-back #<checkout name>", and that name is exactly what
// abandoned_checkout_calls stores. So the history can be rebuilt precisely
// rather than guessed: walk the discounts, read the checkout out of each
// title, write the code onto the row that has none.
//
// ONE SHOT, AND SAFE TO REPEAT. It writes only where discount_code IS NULL,
// so a second run changes nothing and can never overwrite what the sender
// recorded with something inferred afterwards.

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

type winbackBackfillResponse struct {
	// Discounts found in Shopify carrying a readable checkout name.
	Discounts int `json:"discounts"`
	// Rows actually filled in. Lower than Discounts whenever a code was
	// minted for a checkout this brand never recorded contacting - which
	// happens, because a send can fail after the code exists.
	Filled int `json:"filled"`
	// AlreadyKnown is how many were left alone because the sender had
	// recorded the code itself.
	AlreadyKnown int `json:"already_known"`
}

// backfillWinbackCodes rebuilds the code history for one brand.
func (s *Server) backfillWinbackCodes(c *gin.Context) {
	slug := c.Param("slug")
	ctx := c.Request.Context()
	log := obs.FromContext(ctx)

	conn, err := s.store.Q.GetConnectionWithToken(ctx,
		gen.GetConnectionWithTokenParams{BrandSlug: slug, Provider: shopifyProvider})
	shop, token, connected := shopifyCredentials(conn, err)
	if !connected {
		detail(c, http.StatusConflict,
			"This brand has no connected Shopify store, so there are no discounts to read.")
		return
	}

	discounts, err := s.shopify.ListWinbackDiscounts(ctx, shop, token)
	if err != nil {
		// Shopify's own sentence: its refusals name the scope or field that
		// was wrong, and that is what the reader can act on.
		detail(c, http.StatusBadGateway, err.Error())
		return
	}

	out := winbackBackfillResponse{Discounts: len(discounts)}
	for _, d := range discounts {
		filled, updErr := s.store.Q.BackfillWinbackCode(ctx, gen.BackfillWinbackCodeParams{
			DiscountCode: &d.Code, BrandSlug: slug, CheckoutName: d.CheckoutName,
		})
		if updErr != nil {
			// One bad row must not abandon the rest: this is a repair, and a
			// partial repair is strictly better than none.
			log.Warn("could not backfill a win-back code",
				"brand", slug, "checkout", d.CheckoutName, "error", updErr)
			continue
		}
		if filled > 0 {
			out.Filled += int(filled)
		} else {
			out.AlreadyKnown++
		}
	}

	log.Info("win-back codes backfilled",
		"brand", slug, "discounts", out.Discounts,
		"filled", out.Filled, "already_known", out.AlreadyKnown)
	c.JSON(http.StatusOK, out)
}
