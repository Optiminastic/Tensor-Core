package httpapi

// Minting a link and messaging nobody.
//
// Every other way of not sending RELEASES the order - and must, because an
// order held for a link that is never coming would never be released by
// anything. That makes "check the page without a phone buzzing" impossible:
// switching the check off, or disconnecting WhatsApp, sends the order to the
// floor with no page to open.
//
// This is the opposite: the link IS coming, by hand, from the orders page.

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/config"
)

func TestIntegrationDeliveryOffStillMintsALinkAndHoldsTheOrder(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	s := registryServer(t, store)
	s.cfg.PersonalisationSendEnabled = false
	s.cfg.FrontendURL = "https://design.example.test"
	s.cfg.PersonalisationLinkMinutes = 30

	brand := "the-3d-printing-store-india"
	orderID := uuid.New()
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO orders (id, brand_slug, shopify_order_id, order_number, customer_phone,
		    financial_status, total_price, currency, line_items, status, source)
		 VALUES ($1, $2, 909090909, 'T3DPS-909090', '+919000000000', 'paid', 599, 'INR',
		         '[]'::jsonb, 'queued', 'shopify_webhook')`,
		orderID, brand); err != nil {
		t.Fatalf("order: %v", err)
	}
	order, err := store.Q.GetOrderByID(ctx, orderID)
	if err != nil {
		t.Fatalf("read order: %v", err)
	}

	w := &PersonalisationLinkWorker{server: s, logger: slog.Default()}

	// NIL MESSENGER ON PURPOSE. Nothing is being delivered, so nothing may
	// touch one - and a brand with no WhatsApp connected has to reach this
	// same path rather than having its order released.
	if err := w.ask(ctx, brand, order, "+919000000000", false, nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	var status, detail, messageID string
	if err := store.Pool.QueryRow(ctx,
		`SELECT status, coalesce(detail, ''), coalesce(message_id, '')
		   FROM personalisation_links WHERE order_id = $1`, orderID).
		Scan(&status, &detail, &messageID); err != nil {
		t.Fatalf("no link was minted: %v", err)
	}
	// "sent" is what every rule downstream reads - the window sweep, the
	// customer's Confirm, the orders page. A sixth status would have to be
	// taught to all of them to mean the same thing.
	if status != "sent" {
		t.Errorf("status = %q, want sent so the link is live and openable", status)
	}
	if !strings.Contains(detail, "not delivered") {
		t.Errorf("detail = %q; the row has to say nothing was actually sent", detail)
	}
	if messageID != "" {
		t.Errorf("message_id = %q, want empty - there is no message", messageID)
	}
}

func TestTheSwitchDefaultsToDelivering(t *testing.T) {
	// A default of false would silently stop messaging every customer, which
	// is the one failure this must never have.
	t.Setenv("DATABASE_URL", "postgres://x/y")
	cfg := config.Load()
	if !cfg.PersonalisationSendEnabled {
		t.Error("PERSONALISATION_SEND_ENABLED defaults to false; " +
			"an unset variable must keep messaging customers")
	}
}

func TestTheSwitchCanBeTurnedOff(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x/y")
	t.Setenv("PERSONALISATION_SEND_ENABLED", "false")
	if config.Load().PersonalisationSendEnabled {
		t.Error("PERSONALISATION_SEND_ENABLED=false did not switch delivery off")
	}
}
