package httpapi

// Asking the customer to check what they ordered, the moment it arrives.
//
// SENT ON ARRIVAL, not on a timer. The trigger is enqueued inside the order's
// own transaction beside job creation, so "the order exists" and "the customer
// will be asked" commit together. There is no delay to wait out and no sweep
// to miss one.
//
// THE RENDER WILL NOT BE READY, AND THAT IS FINE. A model is 20-45 seconds of
// OpenSCAD and the customer takes longer than that to notice a message. The
// page opens on the fields - which come straight off the order and are ready
// immediately - with the viewer building. The fields are what they came to
// check.
//
// SAFE BY DEFAULT: with PERSONALISATION_CHECK_ENABLED unset this runs every
// rule, logs exactly whose order it would have asked about, and messages
// nobody.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/whatsapp"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// personalisationSendTimeout bounds one send. Generous: it is a database read,
// a Shopify-free path and one Meta call.
const personalisationSendTimeout = 2 * time.Minute

// PersonalisationLinkWorker asks one order's customer to check it.
type PersonalisationLinkWorker struct {
	river.WorkerDefaults[production.SendPersonalisationLinkArgs]
	server *Server
	logger *slog.Logger
}

func NewPersonalisationLinkWorker(s *Server, logger *slog.Logger) *PersonalisationLinkWorker {
	return &PersonalisationLinkWorker{server: s, logger: logger}
}

func (w *PersonalisationLinkWorker) Timeout(
	*river.Job[production.SendPersonalisationLinkArgs],
) time.Duration {
	return personalisationSendTimeout
}

func (w *PersonalisationLinkWorker) Work(
	ctx context.Context, job *river.Job[production.SendPersonalisationLinkArgs],
) error {
	return w.send(ctx, job.Args.OrderID)
}

// brandFor works out whose shop an order came from.
//
// THE OBVIOUS JOIN IS NOT ENOUGH, and measuring it is the only way anybody
// would know. orders.shop_connection_id points at a brand_connections row, and
// reconnecting Shopify REPLACES that row with a new id - so every order
// imported before the reconnect points at a connection that no longer exists.
// On this database that is all of them: 202 orders carry a connection id and
// not one of them resolves, because the shop was reconnected after they
// arrived. Relying on the join alone would mean this feature silently never
// fired, with "the order has no brand" in the log and nothing to suggest the
// log was wrong.
//
// So the join is tried first - it is exact when it works, and it is the only
// thing that can be right on a multi-brand install - and the fallback is the
// single connected Shopify brand. With exactly one, there is no ambiguity to
// resolve. With several, an orphaned order genuinely cannot be attributed and
// is refused rather than guessed at: messaging the wrong shop's customer about
// an order they did not place is worse than not messaging anybody.
func (w *PersonalisationLinkWorker) brandFor(
	ctx context.Context, orderID uuid.UUID,
) (string, error) {
	if slug, err := w.server.store.Q.GetOrderBrandSlug(ctx, orderID); err == nil {
		if trimmed := strings.TrimSpace(slug); trimmed != "" {
			return trimmed, nil
		}
	}

	slugs, err := w.server.store.Q.ListConnectedShopifyBrandSlugs(ctx)
	if err != nil {
		return "", fmt.Errorf("the connected brands could not be read")
	}
	switch len(slugs) {
	case 0:
		return "", fmt.Errorf("no brand has Shopify connected")
	case 1:
		return slugs[0], nil
	default:
		return "", fmt.Errorf(
			"this order's connection is gone and %d brands are connected, so it cannot be attributed",
			len(slugs))
	}
}

// skipReason says why an order is not being asked about, or "" when it is.
//
// A string rather than a bool so the log line names the rule. "No link was
// sent" with no reason is the shape of question nobody can answer three days
// later, and every one of these refusals is ordinary rather than exceptional.
type skipReason = string

func (w *PersonalisationLinkWorker) send(ctx context.Context, orderID uuid.UUID) error {
	order, err := w.server.store.Q.GetOrderByID(ctx, orderID)
	if err != nil {
		// A deleted order is not an error worth retrying three times.
		w.logger.Info("personalisation: the order is gone", "order", orderID)
		return nil
	}

	slug, err := w.brandFor(ctx, orderID)
	if err != nil {
		w.logger.Info("personalisation: not asking",
			"order", order.OrderNumber, "reason", err.Error())
		return nil
	}

	phone, reason := w.eligible(ctx, slug, order)
	if reason != "" {
		w.logger.Info("personalisation: not asking",
			"brand", slug, "order", order.OrderNumber, "reason", reason)
		return nil
	}

	// CONSENT, CHECKED LAST, so a dry run exercises every rule above it and
	// says exactly whose order it would have asked about. That is how the
	// rules get checked against a real store before anybody's phone buzzes.
	if !w.server.cfg.PersonalisationCheckEnabled {
		w.logger.Info("personalisation: WOULD ask (this check is disabled)",
			"brand", slug, "order", order.OrderNumber)
		return nil
	}

	messenger := w.server.whatsappFor(ctx, slug)
	if !messenger.Configured() {
		w.logger.Info("personalisation: not asking",
			"brand", slug, "order", order.OrderNumber,
			"reason", "WhatsApp is not connected for this brand")
		return nil
	}

	return w.ask(ctx, slug, order, phone, messenger)
}

// eligible returns the number to message, or the one rule that refused.
//
// One named rule per branch, deliberately not a compound condition: of an
// order whose customer was messaged, somebody will eventually need to know
// which rule let it through.
func (w *PersonalisationLinkWorker) eligible(
	ctx context.Context, slug string, order gen.Order,
) (phone string, reason skipReason) {
	only := w.server.cfg.PersonalisationOnly

	// 1. THE TESTING GATE outranks everything. While it names any order,
	//    nothing else is asked about whatever the rules allow - which is what
	//    makes it safe to point this at the live store with one real order.
	if len(only) > 0 && !matchesOrderNumber(only, order.OrderNumber) {
		return "", "the allow-list names other orders"
	}

	// 2. Nothing to check. An order of plain stock carries no names and no
	//    colours, and asking its customer to confirm nothing wastes the one
	//    message they will read.
	if !orderNeedsPersonalisation(order) {
		return "", "nothing on this order is personalised"
	}

	// 3. The backlog. Every sync re-imports a month of orders, so without a
	//    watermark switching this on would message everyone who has bought
	//    anything since August - all at once, about orders long since
	//    delivered.
	if len(only) == 0 {
		mark, err := w.server.store.Q.EnsurePersonalisationWatermark(ctx, slug)
		if err != nil {
			// Fail closed. Not knowing when watching began is not a licence
			// to message a month of customers.
			return "", "the watermark could not be read"
		}
		if placed := orderPlacedAt(order); mark.WatchFrom.Valid &&
			!placed.After(mark.WatchFrom.Time) {
			return "", "this order was placed before the shop began asking"
		}
	}

	// 4. Somewhere to send it.
	phone = normaliseIndianMobile(deref(order.CustomerPhone))
	if phone == "" {
		return "", "the order carries no usable phone number"
	}
	return phone, ""
}

// ask mints the link, claims the order and sends it.
func (w *PersonalisationLinkWorker) ask(
	ctx context.Context, slug string, order gen.Order, phone string,
	messenger *whatsapp.Client,
) error {
	token, hash, err := newPersonalisationToken()
	if err != nil {
		return err
	}
	url := personalisationLinkURL(w.server.cfg.FrontendURL, token)
	if url == "" {
		w.logger.Error("personalisation: no public address is configured, so the link would go nowhere",
			"brand", slug, "order", order.OrderNumber)
		return nil
	}

	window := time.Duration(w.server.cfg.PersonalisationLinkMinutes) * time.Minute
	if window <= 0 {
		window = 30 * time.Minute
	}

	// CLAIMED BEFORE IT IS SENT. A row written afterwards leaves a window in
	// which this process dies with the customer's phone already buzzing and
	// nothing recorded - and the next re-import of the same order messages
	// them again. Claiming first means a crash costs a missed link, which is
	// recoverable, rather than a duplicate, which is not.
	claim, err := w.server.store.Q.ClaimPersonalisationLink(ctx,
		gen.ClaimPersonalisationLinkParams{
			ID: uuid.New(), BrandSlug: slug, OrderID: order.ID,
			TokenHash: hash, Phone: phone,
			ExpiresAt: timestamptzAt(time.Now().UTC().Add(window)),
		})
	if err != nil {
		// No row back means this order already has a link - another worker
		// won, or this is simply the same order arriving on another sync.
		// Said out loud rather than swallowed: silence here looks exactly
		// like a rule refusing, and the two need different answers.
		w.logger.Info("personalisation: this order already has a link",
			"brand", slug, "order", order.OrderNumber)
		return nil
	}

	result, sendErr := messenger.SendTemplate(ctx, whatsapp.TemplateMessage{
		To:   phone,
		Name: w.server.cfg.PersonalisationTemplate,
		// The customer, their order, and the one path the button carries.
		BodyParams:   personalisationBodyParams(order),
		ButtonParams: []string{"p/" + token},
	})
	if sendErr != nil {
		// TRANSIENT GOES BACK, PERMANENT STAYS. A rate limit must not mark an
		// order asked-about over a message that never left; a template that
		// does not exist will refuse identically forever, and the row is then
		// the only record of why this customer was never asked.
		var metaErr *whatsapp.Error
		if errors.As(sendErr, &metaErr) && metaErr.Permanent() {
			w.settle(ctx, claim.ID, "failed", "", sendErr.Error())
			w.logger.Error("personalisation: refused, and it will refuse again",
				"brand", slug, "order", order.OrderNumber,
				"code", metaErr.Code, "error", sendErr)
			return nil
		}
		_ = w.server.store.Q.ReleasePersonalisationLink(ctx, claim.ID)
		return fmt.Errorf("send the personalisation link: %w", sendErr)
	}

	w.settle(ctx, claim.ID, "sent", result.MessageID, result.Status)
	// The customer's number is NOT logged. The order number finds them.
	w.logger.Info("personalisation link sent",
		"brand", slug, "order", order.OrderNumber,
		"message", result.MessageID, "expires_in", window.String())
	return nil
}

func (w *PersonalisationLinkWorker) settle(
	ctx context.Context, id uuid.UUID, status, messageID, detail string,
) {
	params := gen.SettlePersonalisationLinkParams{ID: id, Status: status}
	if messageID != "" {
		params.MessageID = &messageID
	}
	if detail != "" {
		params.Detail = &detail
	}
	if _, err := w.server.store.Q.SettlePersonalisationLink(ctx, params); err != nil {
		w.logger.Error("personalisation: could not record the outcome",
			"id", id, "status", status, "error", err)
	}
}

// --- closing the window ------------------------------------------------------

// expiredLinkBatch bounds one sweep. A backlog after an outage must not become
// one enormous transaction, and the next tick is a minute away.
const expiredLinkBatch = 200

// PersonalisationExpiryWorker releases jobs whose customer's window has closed.
//
// THIS IS WHAT MAKES THE WHOLE FEATURE SAFE TO SHIP. Everything else holds
// work back; this is the only thing that lets it go again. If it stops running,
// every order whose customer did not answer waits forever for a confirmation
// that is never coming - so it is registered where the River leader will run
// it, and it treats "the customer said nothing" exactly as it treats "the
// customer said yes".
type PersonalisationExpiryWorker struct {
	river.WorkerDefaults[production.ExpirePersonalisationLinksArgs]
	server *Server
	logger *slog.Logger
}

func NewPersonalisationExpiryWorker(s *Server, logger *slog.Logger) *PersonalisationExpiryWorker {
	return &PersonalisationExpiryWorker{server: s, logger: logger}
}

func (w *PersonalisationExpiryWorker) Work(
	ctx context.Context, _ *river.Job[production.ExpirePersonalisationLinksArgs],
) error {
	return w.Sweep(ctx)
}

// Sweep closes every window whose time is up. Exported so an operator can ask
// for one now, and so a test can drive it without River.
func (w *PersonalisationExpiryWorker) Sweep(ctx context.Context) error {
	due, err := w.server.store.Q.ListExpiredPersonalisationLinks(ctx, expiredLinkBatch)
	if err != nil {
		return fmt.Errorf("list expired personalisation links: %w", err)
	}
	for _, link := range due {
		// Released FIRST, closed second. The other order would leave a window
		// in which the link reads 'expired' while its jobs still wait for a
		// customer who can no longer reach them - and nothing would ever come
		// back for those jobs, because the next sweep would not find the link.
		w.release(ctx, link.OrderID, "system (window closed)")
		if err := w.server.store.Q.ExpirePersonalisationLink(ctx, link.ID); err != nil {
			w.logger.Error("personalisation: could not close the window",
				"order", link.OrderID, "error", err)
			continue
		}
		w.logger.Info("personalisation window closed, the order proceeds as placed",
			"brand", link.BrandSlug, "order", link.OrderID)
	}
	if len(due) > 0 {
		// The planner is not told per link: one trigger after the batch is
		// the same work and a fraction of the churn.
		w.server.triggerBatchPlan(ctx)
	}
	return nil
}

// release lets an order's held jobs through to batching.
func (w *PersonalisationExpiryWorker) release(
	ctx context.Context, orderID uuid.UUID, by string,
) {
	if err := w.server.store.Q.ReleaseOrderPersonalisationToPrint(ctx,
		gen.ReleaseOrderPersonalisationToPrintParams{
			OrderID: &orderID, ValidatedBy: &by,
		}); err != nil {
		w.logger.Error("personalisation: could not release the order's jobs",
			"order", orderID, "error", err)
	}
}
