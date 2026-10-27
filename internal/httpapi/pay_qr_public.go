package httpapi

// Serving the reverse-pickup payment QR.
//
// PUBLIC AND UNAUTHENTICATED, because the caller is Meta. A WhatsApp image
// message carries either a media id Meta already holds or a `link` Meta
// fetches for itself, and a link behind a session is a link Meta cannot read -
// the send fails and the customer, who has just agreed to pay, gets silence.
//
// Public is the right shape for this particular image anyway: it is a business
// UPI QR for ALT OPTI MEDIA PRIVATE LIMITED, the sort of thing printed on an
// invoice. It names a payee and nothing else - no customer, no order, no
// amount. Anyone who fetches it learns who the shop banks with.
//
// It is NOT a damage link. Those carry a 32-byte token because they open one
// customer's form; this is one fixed file with nothing in it to protect, so it
// is addressed by name and rate-limited like every other public route.

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/whatsappbot/assets"
)

// payQRPath is where the image lives, and what whatsappPayQRURL builds.
const payQRPath = "/public/pay-qr.jpg"

func (s *Server) registerPayQRPublic(r *gin.Engine) {
	// NO RequireUser: Meta is the caller. Rate-limited like the other public
	// routes, so a scraper cannot make this the cheapest way to load the box.
	r.GET(payQRPath, s.publicRateLimit(), s.servePayQR)
}

func (s *Server) servePayQR(c *gin.Context) {
	// Cacheable, and deliberately so. The file is compiled into the binary
	// and changes only on deploy, so Meta re-fetching it for every customer
	// is pure waste - and a cached copy is one fewer thing between a customer
	// agreeing to pay and seeing how.
	c.Header("Cache-Control", "public, max-age=86400")
	c.Header("Content-Length", strconv.Itoa(len(assets.ReversePickupQR)))
	c.Data(http.StatusOK, assets.ReversePickupQRType, assets.ReversePickupQR)
}

// payQRURL is the address to hand Meta.
//
// Built from the service's own public base URL rather than the frontend's: the
// image is served by THIS binary, and pointing at the frontend would be
// pointing at a host that has never heard of it.
func (s *Server) payQRURL() string {
	// Already trimmed of a trailing slash at load (config.Load), so joining
	// is a concatenation rather than a path-building exercise.
	// PAY_QR_BASE_URL FIRST, because PublicBaseURL is not free to be this
	// binary's address: it also builds the Shopify OAuth redirect, which must
	// match what is registered with Shopify, and on this deployment that is
	// the frontend. The frontend does not serve this image.
	//
	// WHEN THAT IS WRONG, NOTHING SAYS SO. Meta accepts a send whose media
	// link it cannot fetch, so the API call succeeds, the timeline records
	// that the customer was asked, and the customer receives nothing at all.
	// Seen on production on 2026-10-27.
	base := s.cfg.PayQRBaseURL
	if base == "" {
		base = s.cfg.PublicBaseURL
	}
	if base == "" {
		return ""
	}
	return base + payQRPath
}
