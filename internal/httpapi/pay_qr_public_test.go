package httpapi

// Where the payment QR is fetched from.
//
// THE FAILURE THIS GUARDS IS SILENT. Meta accepts a send whose media link it
// cannot fetch: the API call succeeds, the timeline records that the customer
// was asked, and the customer receives nothing. Production spent a morning in
// that state on 2026-10-27 because PublicBaseURL pointed at the frontend,
// which does not serve this image.

import (
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/config"
)

func TestThePayQRIsServedFromTheBinaryThatHasIt(t *testing.T) {
	// PublicBaseURL is NOT free to be this binary's address - it also builds
	// the Shopify OAuth redirect, which must match what Shopify has
	// registered. So the QR takes its own setting when they differ.
	s := &Server{cfg: config.Settings{
		PublicBaseURL: "https://tensor.example.com",
		PayQRBaseURL:  "https://api.tensor.example.com",
	}}
	if got := s.payQRURL(); !strings.HasPrefix(got, "https://api.tensor.example.com") {
		t.Errorf("QR served from %q, want the API's own host", got)
	}

	// Where the API IS its own public host, one setting is enough and
	// nothing has to be configured twice.
	s = &Server{cfg: config.Settings{PublicBaseURL: "https://api.tensor.example.com"}}
	if got := s.payQRURL(); got != "https://api.tensor.example.com"+payQRPath {
		t.Errorf("fallback gave %q", got)
	}

	// Neither set means no image rather than a broken link: the caller sends
	// the text-only version of the ask instead.
	s = &Server{cfg: config.Settings{}}
	if got := s.payQRURL(); got != "" {
		t.Errorf("with nothing configured the QR URL is %q, want empty", got)
	}
}
