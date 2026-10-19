package httpapi

// The token is the only thing keeping one customer's order away from everyone
// else's. Every assertion here is that property.

import (
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/config"
)

func TestEveryTokenIsDifferent(t *testing.T) {
	// A repeat would mean two customers sharing a link, which is the worst
	// available outcome: each would see and be able to confirm the other's
	// order.
	seen := make(map[string]bool, 500)
	for range 500 {
		token, _, err := newPersonalisationToken()
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		if seen[token] {
			t.Fatal("the same token was minted twice")
		}
		seen[token] = true
	}
}

func TestTheTokenIsLongEnoughToBeUnguessable(t *testing.T) {
	token, _, err := newPersonalisationToken()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	// 32 bytes in base64url with no padding is 43 characters. Shorter would
	// mean somebody shortened the entropy, which is the one change here that
	// looks harmless and is not.
	if len(token) != 43 {
		t.Errorf("token is %d characters, want 43 (256 bits)", len(token))
	}
	// URL-safe alphabet only: a '+' or '/' would be mangled in a path.
	if strings.ContainsAny(token, "+/=") {
		t.Errorf("token %q is not URL-safe", token)
	}
}

func TestTheStoredValueIsNotTheToken(t *testing.T) {
	// The whole point of hashing: a database dump must not open anybody's
	// link.
	token, hash, err := newPersonalisationToken()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if hash == token {
		t.Fatal("the raw token is being stored")
	}
	if strings.Contains(hash, token) {
		t.Fatal("the stored hash contains the token")
	}
	if len(hash) != 64 {
		t.Errorf("hash is %d characters, want 64 hex", len(hash))
	}
}

func TestTheSameTokenAlwaysHashesTheSame(t *testing.T) {
	// The lookup depends on it: a hash that varied would make every link
	// unopenable, and the page would refuse valid tokens with the message
	// meant for expired ones.
	token, hash, err := newPersonalisationToken()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if again := hashPersonalisationToken(token); again != hash {
		t.Errorf("hash changed between calls: %q then %q", hash, again)
	}
	// Whitespace from a copied-and-pasted URL must not change the answer.
	if padded := hashPersonalisationToken("  " + token + "\n"); padded != hash {
		t.Error("surrounding whitespace changed the hash")
	}
}

func TestTheLinkPutsTheTokenInThePath(t *testing.T) {
	// Not in a query string: that reaches analytics, access logs and the
	// Referer header of every outbound link on the page.
	got := personalisationLinkURL("https://tensor.optiminastic.com/", "abc123")
	if got != "https://tensor.optiminastic.com/p/abc123" {
		t.Errorf("url = %q", got)
	}
	if strings.Contains(got, "?") {
		t.Error("the token is in a query string")
	}
}

func TestNoHostOrNoTokenMeansNoLink(t *testing.T) {
	// Empty rather than a half-built address. A link to "/p/abc" with no host
	// would be sent to a customer and go nowhere.
	if got := personalisationLinkURL("", "abc"); got != "" {
		t.Errorf("url = %q, want empty", got)
	}
	if got := personalisationLinkURL("https://x.test", " "); got != "" {
		t.Errorf("url = %q, want empty", got)
	}
}

// Customer-facing links go out under the shop's own domain.
//
// THE DASHBOARD'S ADDRESS IS NOT THE CUSTOMER'S. tensor.optiminastic.com is
// the shop's internal tool; a link arriving in WhatsApp from an unfamiliar
// domain is what a scam looks like. Both public pages must use the same
// configured host, or a customer has no way to tell which one is really the
// shop.
func TestCustomerLinksUseTheCustomerDomain(t *testing.T) {
	s := &Server{cfg: config.Settings{
		FrontendURL:     "https://tensor.optiminastic.com",
		CustomerLinkURL: "https://design.the3dprintingstore.in",
	}}
	base := s.customerLinkBase()

	if got := personalisationLinkURL(base, "TOKEN"); got != "https://design.the3dprintingstore.in/p/TOKEN" {
		t.Errorf("personalisation link = %q", got)
	}
	if got := damageLinkURL(base, "TOKEN"); got != "https://design.the3dprintingstore.in/d/TOKEN" {
		t.Errorf("damage link = %q", got)
	}
}

// Unset, it falls back to the dashboard - which is what keeps local
// development and any brand without a second domain working unchanged.
func TestCustomerLinksFallBackToTheDashboard(t *testing.T) {
	s := &Server{cfg: config.Settings{FrontendURL: "http://localhost:3001"}}
	if got := s.customerLinkBase(); got != "http://localhost:3001" {
		t.Fatalf("base = %q", got)
	}
}
