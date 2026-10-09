package httpapi

// Who the scheduler may ring, and - far more importantly - who it may not.
//
// Every assertion here is a person's phone. A bug in this function is not a
// wrong number on a page; it is a stranger being telephoned about a basket
// they already paid for, or being telephoned twice.

import (
	"log/slog"
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

func sweeper(only ...string) *WinbackCallWorker {
	return &WinbackCallWorker{
		server: &Server{cfg: config.Settings{
			WinbackCallDelayMinutes: 10,
			WinbackCallMaxAgeHours:  24,
			WinbackCallOnly:         only,
		}},
		logger: slog.Default(),
	}
}

func checkout(name string, age time.Duration, phone string) shopify.AbandonedCheckout {
	return shopify.AbandonedCheckout{
		ID:        "gid://shopify/AbandonedCheckout/" + name,
		Name:      name,
		CreatedAt: time.Now().Add(-age),
		Phone:     phone,
	}
}

// longAgo is a watch mark old enough that it gates nothing, so each test
// below exercises the one rule it is named for.
func longAgo() time.Time { return time.Now().Add(-365 * 24 * time.Hour) }

func candidateNames(found []candidate) []string {
	out := make([]string, 0, len(found))
	for _, c := range found {
		out = append(out, c.checkout.Name)
	}
	return out
}

func TestACartIsOnlyCalledOnceItHasGoneQuiet(t *testing.T) {
	w := sweeper()
	got := w.eligible([]shopify.AbandonedCheckout{
		checkout("#toosoon", 4*time.Minute, "9799931864"),
		checkout("#ready", 11*time.Minute, "9799931865"),
	}, nil, longAgo())

	if len(got) != 1 || got[0].checkout.Name != "#ready" {
		// Four minutes in, the customer is very likely still typing their card
		// number. The delay is the difference between a win-back and an
		// interruption.
		t.Fatalf("eligible = %v, want only #ready", candidateNames(got))
	}
}

func TestACartThatWasPaidForIsNeverCalled(t *testing.T) {
	paid := checkout("#recovered", time.Hour, "9799931864")
	completed := time.Now().Add(-30 * time.Minute)
	paid.CompletedAt = &completed

	// Shopify leaves recovered checkouts in the abandoned list. Without this
	// rule the agent rings somebody about a basket they already bought, which
	// is the single most embarrassing call this system can place.
	if got := sweeper().eligible([]shopify.AbandonedCheckout{paid}, nil, longAgo()); len(got) != 0 {
		t.Fatalf("a paid checkout was eligible: %v", candidateNames(got))
	}
}

func TestOneCallPerPersonNotPerBasket(t *testing.T) {
	// The same number on two different carts. Deduplicating per CHECKOUT would
	// let both through and ring one person twice.
	already := map[string]bool{"+919799931864": true}
	got := sweeper().eligible([]shopify.AbandonedCheckout{
		checkout("#first", time.Hour, "9799931864"),
		checkout("#second", 30*time.Minute, "09799931864"),
		checkout("#other", 30*time.Minute, "9876543210"),
	}, already, longAgo())

	if len(got) != 1 || got[0].checkout.Name != "#other" {
		t.Fatalf("eligible = %v, want only #other - the other two are the same person", candidateNames(got))
	}
}

func TestAStaleBasketIsNotACallItIsAColdCall(t *testing.T) {
	if got := sweeper().eligible([]shopify.AbandonedCheckout{
		checkout("#ancient", 40*24*time.Hour, "9799931864"),
	}, nil, longAgo()); len(got) != 0 {
		t.Fatalf("a six-week-old cart was eligible: %v", candidateNames(got))
	}
}

func TestACheckoutWithNoNumberIsSkipped(t *testing.T) {
	if got := sweeper().eligible([]shopify.AbandonedCheckout{
		checkout("#nophone", time.Hour, ""),
		checkout("#rubbish", time.Hour, "n/a"),
	}, nil, longAgo()); len(got) != 0 {
		t.Fatalf("a checkout with no usable number was eligible: %v", candidateNames(got))
	}
}

func TestTheAllowListBeatsEveryOtherRule(t *testing.T) {
	// The testing gate. While it names anything, nothing else is ever called -
	// which is what makes it safe to run this against the live store.
	w := sweeper("#66850294530261")
	got := w.eligible([]shopify.AbandonedCheckout{
		checkout("#66850294530261", time.Hour, "9799931864"),
		checkout("#perfectlyeligible", time.Hour, "9876543210"),
		checkout("#alsoeligible", 2*time.Hour, "9876543211"),
	}, nil, longAgo())

	if len(got) != 1 || got[0].checkout.Name != "#66850294530261" {
		t.Fatalf("eligible = %v, want only the allow-listed checkout", candidateNames(got))
	}
}

func TestTheNumberIsNormalisedBeforeItIsComparedOrDialled(t *testing.T) {
	// "09799931864" and "+91 97999 31864" are one person. Compared raw they
	// are three, and that person is rung three times.
	got := sweeper().eligible([]shopify.AbandonedCheckout{
		checkout("#a", time.Hour, "+91 97999 31864"),
	}, nil, longAgo())
	if len(got) != 1 {
		t.Fatalf("eligible = %v", candidateNames(got))
	}
	if got[0].phone != "+919799931864" {
		t.Errorf("phone = %q, want E.164", got[0].phone)
	}
}

func TestEveryProductInTheBasketIsNamed(t *testing.T) {
	// A call naming one item out of two sounds like it is about somebody
	// else's basket.
	c := shopify.AbandonedCheckout{LineItems: []shopify.AbandonedLineItem{
		{Title: "Dual Name Plank"}, {Title: "Soulmate Combo"}, {Title: "Dual Name Plank"},
	}}
	if got := productPhrase(c); got != "Dual Name Plank and Soulmate Combo" {
		t.Errorf("phrase = %q", got)
	}
}

func TestTheCartTotalIsSaidAsWholeRupees(t *testing.T) {
	// Shopify sends "3499.0"; a speech engine reads that as "point zero".
	if got := spokenAmount("3499.0"); got != "3499" {
		t.Errorf("amount = %q, want 3499", got)
	}
}

func TestABasketAbandonedBeforeWatchingBeganIsLeftAlone(t *testing.T) {
	// The whole backlog question. Shopify's list is thirty days deep, so a
	// worker starting for the first time sees hundreds of carts that have sat
	// there for weeks. Ringing those is a cold call, not a win-back - and it
	// would happen on startup, to everybody at once.
	watchFrom := time.Now().Add(-time.Hour)
	got := sweeper().eligible([]shopify.AbandonedCheckout{
		checkout("#backlog", 6*time.Hour, "9799931864"),
		checkout("#sincewatching", 20*time.Minute, "9876543210"),
	}, nil, watchFrom)

	if len(got) != 1 || got[0].checkout.Name != "#sincewatching" {
		t.Fatalf("eligible = %v, want only the cart abandoned since watching began",
			candidateNames(got))
	}
}

func TestTheAllowListStillReachesAnOlderCheckout(t *testing.T) {
	// The one thing that reaches past the watch mark, because it names a
	// single checkout deliberately and is how the rules get tested against a
	// real one. Everything else stays gated.
	got := sweeper("#66850294530261").eligible([]shopify.AbandonedCheckout{
		checkout("#66850294530261", 20*time.Hour, "9799931864"),
		checkout("#anotherold", 20*time.Hour, "9876543210"),
	}, nil, time.Now().Add(-time.Minute))

	if len(got) != 1 || got[0].checkout.Name != "#66850294530261" {
		t.Fatalf("eligible = %v, want the allow-listed checkout despite the watch mark",
			candidateNames(got))
	}
}
