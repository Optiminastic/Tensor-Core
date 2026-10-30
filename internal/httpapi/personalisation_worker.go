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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/whatsapp"
	"github.com/Optiminastic/tensor-core/internal/obs"
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
	return w.server.brandForOrder(ctx, orderID)
}

// brandForOrder is on the SERVER for the reason personalisationEligible is:
// three places now have to reach the same answer - the sender, the renderer
// deciding whether to hold, and job creation deciding whether to open a window
// at all. One of them was building a throwaway worker just to call this.
func (s *Server) brandForOrder(
	ctx context.Context, orderID uuid.UUID,
) (string, error) {
	if slug, err := s.store.Q.GetOrderBrandSlug(ctx, orderID); err == nil {
		if trimmed := strings.TrimSpace(slug); trimmed != "" {
			return trimmed, nil
		}
	}

	slugs, err := s.store.Q.ListConnectedShopifyBrandSlugs(ctx)
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
		w.noteSkip(ctx, order.ID, err.Error())
		return nil
	}

	phone, reason := w.server.personalisationEligible(ctx, slug, order)
	if reason != "" {
		w.logger.Info("personalisation: not asking",
			"brand", slug, "order", order.OrderNumber, "reason", reason)
		w.noteSkip(ctx, order.ID, reason)
		// Anything the renderer held for a link that is not coming has to go
		// to the floor now. Without this an order refused here waits for an
		// expiry sweep that will never see it, because the sweep walks links
		// and this order has none.
		w.releaseUnasked(ctx, order.ID, "system (not asked)")
		return nil
	}

	// EVERY MODEL, not the first. A combo renders three times and a line of
	// five renders five; each one triggers this, and sending on the first
	// would open the customer's window on a page still building most of what
	// they came to look at. The later renders trigger it again, so skipping
	// here costs nothing.
	if pending, err := w.unrenderedJobs(ctx, order.ID); err != nil || pending > 0 {
		if err == nil {
			w.logger.Info("personalisation: waiting for the rest of the models",
				"brand", slug, "order", order.OrderNumber, "pending", pending)
			// Recorded like a refusal although it is not one: this is a
			// waiting state that CAN strand, and while it lasts the orders
			// page draws the same dash as a rule that said no. Cleared the
			// moment the last model lands and the link is claimed.
			w.noteSkip(ctx, order.ID, fmt.Sprintf(
				"still waiting for %d of this order's models to finish rendering", pending))
		}
		return nil
	}

	// CONSENT, CHECKED LAST, so a dry run exercises every rule above it and
	// says exactly whose order it would have asked about. That is how the
	// rules get checked against a real store before anybody's phone buzzes.
	if !w.server.cfg.PersonalisationCheckEnabled {
		w.logger.Info("personalisation: WOULD ask (this check is disabled)",
			"brand", slug, "order", order.OrderNumber)
		w.noteSkip(ctx, order.ID,
			"the personalisation check was switched off when this order came in")
		return nil
	}

	messenger := w.server.whatsappFor(ctx, slug)
	// CHECKED ONLY WHEN SOMETHING IS BEING DELIVERED. With delivery off the
	// link is minted and nobody is messaged, so a brand with no WhatsApp
	// connected is not a reason to release the order - and releasing it would
	// take away the very page this mode exists to let somebody open.
	if w.server.cfg.PersonalisationSendEnabled && !messenger.Configured() {
		w.logger.Info("personalisation: not asking",
			"brand", slug, "order", order.OrderNumber,
			"reason", "WhatsApp is not connected for this brand")
		w.noteSkip(ctx, order.ID, "WhatsApp is not connected for this brand")
		w.releaseUnasked(ctx, order.ID, "system (not asked)")
		return nil
	}

	// EVERY MESSAGE GOES TO ONE PHONE while a test recipient is configured.
	// The customer's own number is resolved and then deliberately discarded,
	// so the eligibility rules above are still exercised in full against a
	// real order while nobody but the tester is reached.
	to, redirected := personalisationRecipient(w.server.cfg.PersonalisationTestRecipient, phone)
	if redirected {
		if to == "" {
			// Set but unreadable. Nothing is sent, and emphatically not to the
			// customer: a typo in the test number must not become a message to
			// a stranger.
			w.logger.Error("personalisation: PERSONALISATION_TEST_RECIPIENT is not a usable number, so nothing was sent",
				"brand", slug, "order", order.OrderNumber)
			w.noteSkip(ctx, order.ID,
				"PERSONALISATION_TEST_RECIPIENT is set but is not a usable number, "+
					"so nothing was sent - and emphatically not to the customer")
			w.releaseUnasked(ctx, order.ID, "system (test recipient unusable)")
			return nil
		}
		w.logger.Warn("personalisation: REDIRECTED to the test recipient, the customer was not messaged",
			"brand", slug, "order", order.OrderNumber, "to", to)
	}

	return w.ask(ctx, slug, order, to, redirected, messenger)
}

// eligible returns the number to message, or the one rule that refused.
//
// One named rule per branch, deliberately not a compound condition: of an
// order whose customer was messaged, somebody will eventually need to know
// which rule let it through.
// personalisationEligible is on the SERVER, not the worker, because two
// different places now have to reach the same answer: the renderer deciding
// whether to hold a finished job, and the sender deciding whether to message
// anybody. If those two ever disagreed, a job would be held for a link that
// never comes - and nothing would release it.
func (s *Server) personalisationEligible(
	ctx context.Context, slug string, order gen.Order,
) (phone string, reason skipReason) {
	only := s.cfg.PersonalisationOnly

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
	//    delivered. The rule is: ONLY ORDERS PLACED AFTER THE SHOP BEGAN
	//    ASKING.
	if len(only) == 0 {
		watchFrom, ok := s.personalisationWatchFrom(ctx, slug)
		if !ok {
			// Fail closed. Not knowing when watching began is not a licence
			// to message a month of customers.
			return "", "the watermark could not be read"
		}
		if placed := orderPlacedAt(order); !placed.After(watchFrom) {
			return "", "this order was placed before the shop began asking"
		}
	}

	// 5. THE CUSTOMER HAS ALREADY HAD THEIR SAY.
	//
	// A confirmed or expired link means this order's window is over, and
	// nothing should re-open it. Without this rule it was re-opened routinely:
	// every finished render calls the sender, a combo renders three times and
	// a colour change on the link renders again - so a render landing AFTER
	// the confirm sent a second link, and sending HOLDS the order's jobs.
	//
	// Measured on T3DPS-990102: confirmed at 16:03:19, a render in flight
	// finished at 16:03:20, the link was sent again, and the job went from
	// validated straight back to awaiting_customer and off its bed. The
	// customer had finished; the order waited for a window that had already
	// closed, for another thirty minutes.
	//
	// Read by the RENDERER too, through personalisationWillAsk, so the same
	// rule also stops it withholding the print file from a job whose customer
	// is done - which is the other half of the same stall.
	if link, err := s.store.Q.GetPersonalisationLinkByOrder(ctx, order.ID); err == nil {
		switch strings.ToLower(strings.TrimSpace(link.Status)) {
		case "confirmed":
			return "", "this customer has already confirmed"
		case "expired":
			return "", "this customer's window has already closed"
		}
	}

	// 6. The product's own switch. A SKU may be configured not to ask at all -
	//    a design that cannot change after it is bought, or one whose preview
	//    is not ready - and then there is nothing to interrupt the customer
	//    for. One enabled SKU on the order is enough, because an order is the
	//    unit a link covers.
	if !s.orderAsksTheCustomer(ctx, order) {
		return "", "every personalised SKU on this order has its link switched off"
	}

	// 4. Somewhere to send it.
	phone = normaliseIndianMobile(deref(order.CustomerPhone))
	if phone == "" {
		return "", "the order carries no usable phone number"
	}
	return phone, ""
}

// orderAsksTheCustomer reports whether any personalised SKU on this order
// still has its personalisation link switched on.
//
// FAILS OPEN, unlike the watermark above. The watermark fails closed because
// not knowing when watching began could message a month of customers at once;
// this is a per-product preference, and a database hiccup turning it into
// "ask nobody" would quietly stop every customer being asked to check their
// own name. Asking somebody who did not need asking is recoverable.
func (s *Server) orderAsksTheCustomer(ctx context.Context, order gen.Order) bool {
	skus := make([]string, 0, 2)
	for _, li := range orderLineItems(order) {
		if !lineIsPersonalised(li) {
			continue
		}
		if sku := strings.ToLower(strings.TrimSpace(deref(li.SKU))); sku != "" {
			skus = append(skus, sku)
		}
	}
	if len(skus) == 0 {
		return true
	}
	asks, err := s.store.Q.AnySKUAsksTheCustomer(ctx, skus)
	if err != nil {
		obs.FromContext(ctx).Warn(
			"personalisation: could not read the SKUs' link setting, asking anyway",
			"order", order.OrderNumber, "error", err)
		return true
	}
	// A nullable bool from EXISTS ... OR NOT EXISTS: null cannot happen, and
	// if it ever did, the fail-open rule above is the one to follow.
	return asks == nil || *asks
}

// ask mints the link, claims the order and sends it.
func (w *PersonalisationLinkWorker) ask(
	ctx context.Context, slug string, order gen.Order, phone string, redirected bool,
	messenger *whatsapp.Client,
) error {
	token, hash, err := newPersonalisationToken()
	if err != nil {
		return err
	}
	url := personalisationLinkURL(w.server.customerLinkBase(), token)
	if url == "" {
		w.logger.Error("personalisation: no public address is configured, so the link would go nowhere",
			"brand", slug, "order", order.OrderNumber)
		w.noteSkip(ctx, order.ID,
			"no public address is configured for this service, so the link would go nowhere")
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
	params := gen.ClaimPersonalisationLinkParams{
		ID: uuid.New(), BrandSlug: slug, OrderID: order.ID,
		TokenHash: hash, Phone: phone,
		ExpiresAt: timestamptzAt(time.Now().UTC().Add(window)),
	}
	// Sealed, not stored plain, and only while the window is open - the sweep
	// nulls it when the window closes. It exists so somebody taking a phone
	// call can send the customer back to the page they are describing; the
	// hash is still what the link is looked up by.
	//
	// A service with no encryption key simply does not keep it. The link
	// still works for the customer, and the floor cannot copy it, which is
	// better than keeping somebody's live link in plain text.
	if w.server.secrets != nil {
		if sealed, sealErr := w.server.secrets.Seal(token); sealErr == nil {
			params.TokenSealed = &sealed
		} else {
			w.logger.Warn("personalisation: the link could not be sealed, so the floor cannot copy it",
				"order", order.OrderNumber, "error", sealErr)
		}
	}

	claim, err := w.server.store.Q.ClaimPersonalisationLink(ctx, params)
	if err != nil {
		// No row back means this order already has a link - another worker
		// won, or this is simply the same order arriving on another sync.
		// Said out loud rather than swallowed: silence here looks exactly
		// like a rule refusing, and the two need different answers.
		w.logger.Info("personalisation: this order already has a link",
			"brand", slug, "order", order.OrderNumber)
		return nil
	}
	// The order has a live claim, so whatever reason it last carried for not
	// being asked is now false. Cleared here rather than after the send: the
	// claim is the moment the two states become mutually exclusive, and a row
	// that said both at once would be read as the more alarming one.
	if err := w.server.store.Q.ClearPersonalisationSkip(ctx, order.ID); err != nil {
		w.logger.Warn("personalisation: could not clear this order's skip reason",
			"order", order.OrderNumber, "error", err)
	}

	// MINTED, AND DELIVERED TO NOBODY.
	//
	// Settled as "sent" because that is the state every rule downstream reads
	// - the window sweep, the customer's Confirm, the orders page - and a
	// sixth status would have to be taught to all of them to mean the same
	// thing. What actually happened is in the detail, which is the column for
	// saying so, and no message id is recorded because there is no message.
	if !w.server.cfg.PersonalisationSendEnabled {
		w.logger.Warn("personalisation: link minted, NOBODY was messaged",
			"brand", slug, "order", order.OrderNumber)
		w.settleSent(ctx, claim.ID, "",
			"link minted but not delivered: PERSONALISATION_SEND_ENABLED is false",
			frozenMessage{})
		w.holdTheRest(ctx, slug, order)
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
			// The jobs are released as well. A message that will refuse
			// identically forever is not worth holding an order for, and the
			// row keeps the reason.
			w.releaseUnasked(ctx, order.ID, "system (could not ask)")
			w.logger.Error("personalisation: refused, and it will refuse again",
				"brand", slug, "order", order.OrderNumber,
				"code", metaErr.Code, "error", sendErr)
			return nil
		}
		_ = w.server.store.Q.ReleasePersonalisationLink(ctx, claim.ID)
		// The claim is given back, so until the retry lands this order reads
		// as one nobody asked. Say which it is: a retry in progress and a
		// rule that said no look identical on the orders page otherwise.
		w.noteSkip(ctx, order.ID, "WhatsApp refused the message and Tensor is "+
			"trying again: "+sendErr.Error())
		return fmt.Errorf("send the personalisation link: %w", sendErr)
	}

	detail := result.Status
	if redirected {
		// The row must not read like a message to the customer. Somebody
		// reading this log later needs to know this one went to a tester.
		detail = "sent to the test recipient, not the customer"
	}
	w.settleSent(ctx, claim.ID, result.MessageID, detail, w.frozen(ctx, order, messenger))

	w.holdTheRest(ctx, slug, order)

	// The customer's number is NOT logged. The order number finds them.
	w.logger.Info("personalisation link sent",
		"brand", slug, "order", order.OrderNumber,
		"message", result.MessageID, "expires_in", window.String())
	return nil
}

// holdTheRest holds the items of this order that nobody renders.
//
// The renderer holds what it renders. It cannot hold what it never renders -
// a Bone Tag, a DUOSNAP, a Couple's Date Plank - so those jobs reached the
// customer's page with every field locked, the edit path being guarded on
// awaiting_customer. They are the items with nothing BUT fields to check, so
// a page that cannot change them is no check at all.
//
// Called once the link exists rather than at job creation, which is what
// keeps the invariant the hold rests on: a job is only ever held while a link
// is live. Both the expiry sweep and the customer's own Confirm release the
// whole order.
//
// Not fatal, and deliberately not a release: the link is live and the
// rendered jobs are held correctly. A failure here costs the customer the
// ability to correct the items nobody renders, which is a worse page rather
// than a wrong product.
func (w *PersonalisationLinkWorker) holdTheRest(
	ctx context.Context, slug string, order gen.Order,
) {
	held, err := w.server.store.Q.HoldOrderPersonalisationForCustomer(ctx, &order.ID)
	if err != nil {
		w.logger.Warn("personalisation: could not hold the rest of the order",
			"brand", slug, "order", order.OrderNumber, "error", err)
		return
	}
	if len(held) == 0 {
		return
	}
	w.logger.Info("personalisation: holding the order's unrendered items too",
		"brand", slug, "order", order.OrderNumber, "jobs", len(held))
	w.server.replateBedsLeftBy(ctx, held)
}

// frozenMessage is what was said, captured at the moment it was said.
type frozenMessage struct {
	template, body string
	buttons        int
}

// frozen reads the approved copy and fills it with this order's own values.
//
// THE SAME PARAMETERS THE SEND JUST USED, from the same row, so the stored
// body cannot disagree with the message. The template read is cached per
// process, so this costs a round trip once rather than once per order.
//
// Best effort, and deliberately after the send: a customer who has the
// message must not be told again because Tensor could not write down what it
// said. An empty result leaves both columns null and the tab falls back to
// rebuilding the message on read.
func (w *PersonalisationLinkWorker) frozen(
	ctx context.Context, order gen.Order, messenger *whatsapp.Client,
) frozenMessage {
	name := strings.TrimSpace(w.server.cfg.PersonalisationTemplate)
	out := frozenMessage{template: name}
	tpl, err := messenger.Template(ctx, name, "")
	if err != nil {
		w.logger.Warn("personalisation: the message was sent but its copy could not be read",
			"order", order.OrderNumber, "template", name, "error", err)
		return out
	}
	// tpl.Name rather than the configured one: if the name was blank and the
	// client fell back to its default, what we record has to be the template
	// that was actually read, not the one we meant to ask for.
	out.template = tpl.Name
	out.body = whatsapp.Fill(tpl.Body, personalisationBodyParams(order))
	out.buttons = len(tpl.ButtonURLs)
	return out
}

// noteSkip writes down why this order's customer was not asked.
//
// Every refusal in send() names its rule to the log, and the log goes with
// the container. This is the same sentence kept where the orders page can
// show it - see migration 0105. Best effort: a refusal that cannot be
// recorded is still a refusal, and failing the job over it would only arrive
// at the same refusal again.
func (w *PersonalisationLinkWorker) noteSkip(
	ctx context.Context, orderID uuid.UUID, reason string,
) {
	if err := w.server.store.Q.RecordPersonalisationSkip(ctx,
		gen.RecordPersonalisationSkipParams{Reason: &reason, ID: orderID}); err != nil {
		w.logger.Warn("personalisation: could not record why this order was skipped",
			"order", orderID, "reason", reason, "error", err)
	}
}

// settleSent records a send, with the words it sent.
func (w *PersonalisationLinkWorker) settleSent(
	ctx context.Context, id uuid.UUID, messageID, detail string, said frozenMessage,
) {
	w.settleWith(ctx, id, "sent", messageID, detail, said)
}

func (w *PersonalisationLinkWorker) settle(
	ctx context.Context, id uuid.UUID, status, messageID, detail string,
) {
	w.settleWith(ctx, id, status, messageID, detail, frozenMessage{})
}

func (w *PersonalisationLinkWorker) settleWith(
	ctx context.Context, id uuid.UUID, status, messageID, detail string, said frozenMessage,
) {
	params := gen.SettlePersonalisationLinkParams{ID: id, Status: status}
	if messageID != "" {
		params.MessageID = &messageID
	}
	if detail != "" {
		params.Detail = &detail
	}
	if said.template != "" {
		params.TemplateName = &said.template
	}
	if said.body != "" {
		params.MessageBody = &said.body
	}
	if said.buttons > 0 {
		n := int16(said.buttons)
		params.ButtonCount = &n
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

// stalledRenderAfter is how long a job may wait for a model that was queued
// before the backstop assumes it is never coming.
//
// A render is one to two seconds, so ten minutes is not a slow day - it is a
// worker that died between queueing the rebuild and storing its result. The
// thing that would normally clear the hold is the thing that did not happen,
// so without this the job waits for ever.
var stalledRenderAfter = pgtype.Interval{Microseconds: 10 * 60 * 1_000_000, Valid: true}

// strandedHoldAfter is how long a job may wait for a link before the backstop
// assumes none is coming.
//
// Comfortably longer than a render plus a send, so it cannot race them, and
// short enough that a lost trigger costs one order a few minutes rather than a
// shift.
var strandedHoldAfter = pgtype.Interval{Microseconds: 15 * 60 * 1_000_000, Valid: true}

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
	// A claim that was written and never sent. The loop above cannot see it -
	// it walks sent and opened links - and the unique index on order_id means
	// the order can never be asked again while the row sits there. Freeing it
	// lets the next render or sync ask properly.
	if stale, err := w.server.store.Q.ReleaseStalePersonalisationClaims(ctx); err != nil {
		w.logger.Error("personalisation: could not free stale claims", "error", err)
	} else {
		for _, claim := range stale {
			w.logger.Warn("personalisation: freed an order whose link was claimed and never sent",
				"brand", claim.BrandSlug, "order", claim.OrderID)
			// Whatever hold that claim left behind goes with it. Ordinarily
			// there is none - the crash happened before the send - but a
			// job left at awaiting_customer for a link nobody will ever
			// receive is the worst of the two outcomes.
			w.release(ctx, claim.OrderID, "system (link never sent)")
		}
	}

	// The backstop, run on the same tick. A job held for a link that never
	// arrived is invisible to the loop above, because that walks links.
	if freed, err := w.server.store.Q.ReleaseStrandedPersonalisationHolds(ctx,
		strandedHoldAfter); err != nil {
		w.logger.Error("personalisation: could not free stranded holds", "error", err)
	} else if freed > 0 {
		w.logger.Warn("personalisation: freed jobs held for a link that never arrived",
			"jobs", freed)
		w.server.triggerBatchPlan(ctx)
	}

	// And the same shape for a model rebuild that was queued and never landed.
	// A different hold with the same failure mode: the thing that clears it is
	// the thing that did not happen, so only an age can find it.
	if freed, err := w.server.store.Q.ClearStalledModelRenderHolds(ctx,
		stalledRenderAfter); err != nil {
		w.logger.Error("personalisation: could not free stalled render holds", "error", err)
	} else if freed > 0 {
		w.logger.Warn("personalisation: freed jobs waiting on a model that never arrived",
			"jobs", freed)
		w.server.triggerBatchPlan(ctx)
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
	w.server.closePersonalisationWindow(ctx, orderID, by)
}

// --- deciding to hold, at the moment a model lands ----------------------------

// personalisationWillAsk reports whether this finished job should wait for its
// customer rather than go to the floor.
//
// ASKED BEFORE THE LINK EXISTS, which is the whole difficulty. The link is now
// sent after the model is built, so at the moment this is called there is
// nothing to look up - the decision has to be made from the same rules the
// sender will apply, or a job is held for a message that never comes and
// nothing releases it.
//
// Both callers therefore go through personalisationEligible. The remaining
// gap is a failure the sender hits and this cannot predict - Meta refusing,
// the template missing - and that is handled on the other side: a send that
// cannot happen releases the order's jobs rather than leaving them held.
func (s *Server) personalisationWillAsk(ctx context.Context, jobID uuid.UUID) bool {
	job, err := s.store.Q.GetProductionJobByID(ctx, jobID)
	if err != nil || job.OrderID == nil {
		return false
	}

	// A LIVE LINK OUTRANKS THE SWITCH, and this has to be asked first.
	//
	// The switch decides whether to START holding orders. It must not decide
	// whether to release one already held: a customer with the page open in
	// front of them is mid-edit, and turning the feature off - or re-rendering
	// a job for any other reason - would send their order to the floor while
	// their link still worked and still accepted changes that would never be
	// made.
	//
	// Found by a job that re-rendered with the switch off and came back
	// 'validated by system' while its window was still open. Releasing is the
	// expiry sweep's job and nobody else's.
	if _, err := s.store.Q.GetOpenPersonalisationLinkForOrder(ctx, *job.OrderID); err == nil {
		return true
	}

	if !s.cfg.PersonalisationCheckEnabled {
		return false
	}
	order, err := s.store.Q.GetOrderByID(ctx, *job.OrderID)
	if err != nil {
		return false
	}
	slug, err := s.brandForOrder(ctx, *job.OrderID)
	if err != nil {
		return false
	}
	_, reason := s.personalisationEligible(ctx, slug, order)
	return reason == ""
}

// enqueuePersonalisationLink asks for the order's link to be considered.
//
// Best effort and never fatal: a render that produced a model has done its
// job, and failing to schedule a message must not fail it. The stranded-hold
// sweep is what catches an order whose trigger was lost.
func (s *Server) enqueuePersonalisationLink(ctx context.Context, jobID uuid.UUID) {
	if s.personalisationEnqueuer == nil {
		return
	}
	job, err := s.store.Q.GetProductionJobByID(ctx, jobID)
	if err != nil || job.OrderID == nil {
		return
	}
	if err := s.enqueuePersonalisationLinkForOrder(ctx, *job.OrderID); err != nil {
		obs.FromContext(ctx).Warn("personalisation: could not schedule the link",
			"order", *job.OrderID, "error", err)
	}
}

// enqueuePersonalisationLinkForOrder asks for an order's link by order.
//
// The caller that opens a window has the order and no job in particular; the
// one that has just rendered a model has a job and wants the same thing. This
// is the half they share, and it REPORTS its failure rather than logging it,
// because opening a window and then failing to ask for the link leaves the
// order held and has to be undone.
func (s *Server) enqueuePersonalisationLinkForOrder(ctx context.Context, orderID uuid.UUID) error {
	if s.personalisationEnqueuer == nil {
		return fmt.Errorf("the personalisation link queue is not configured")
	}
	return s.personalisationEnqueuer.Enqueue(ctx,
		production.SendPersonalisationLinkArgs{OrderID: orderID})
}

// unrenderedJobs counts the order's held jobs that have no model yet.
func (w *PersonalisationLinkWorker) unrenderedJobs(
	ctx context.Context, orderID uuid.UUID,
) (int, error) {
	jobs, err := w.server.store.Q.ListProductionJobsForOrder(ctx, &orderID)
	if err != nil {
		return 0, err
	}
	pending := 0
	for _, j := range jobs {
		if j.PersonalisationStatus != production.PersonalisationAwaitingCustomer ||
			j.PrintFileID != nil {
			continue
		}
		// ONLY A MODEL THAT IS ACTUALLY COMING. Every personalisable job is
		// held from the moment it is created now, including the ones Tensor
		// never renders - a Bone Tag, a Couple's Date Plank. Counting those
		// would make the order read as permanently mid-render and the link
		// would never be sent at all.
		//
		// So the rule says what it always meant: wait for every model that is
		// on its way. An order with nothing to render answers zero and is sent
		// straight away, which is right - its page is fields only.
		if !w.server.rendersProduct(ctx, deref(j.Sku), deref(j.ProductName)) {
			continue
		}
		// A JOB WHOSE PICTURE IS READY IS NOT STILL RENDERING.
		//
		// A product with a personalisation preview file has its print model
		// deliberately withheld until the window closes, so print_file_id
		// alone would read as "still rendering" forever - and this worker
		// would never send the link that opens the window that releases it.
		// What the customer's page actually needs is the picture, and the
		// picture is here.
		if _, err := w.server.store.Q.GetProductionJobPreviewFile(ctx, j.ID); err == nil {
			continue
		}
		pending++
	}
	return pending, nil
}

// releaseUnasked sends an order's held jobs to the floor when no link is
// coming.
//
// THE INVARIANT THIS KEEPS: a job is only ever held while a link is coming or
// already live. The renderer decides to hold before the sender runs, so every
// path out of the sender that does not produce a live link has to undo that -
// otherwise the order waits for an expiry sweep that walks links and will
// never see it.
func (w *PersonalisationLinkWorker) releaseUnasked(
	ctx context.Context, orderID uuid.UUID, by string,
) {
	// NEVER RELEASE AN ORDER WHOSE CUSTOMER IS STILL HOLDING A LINK.
	//
	// "No link is coming" and "a link is already open" are different states,
	// and the rules above cannot tell them apart: they answer whether this
	// order WOULD be asked now, which is false for an order already asked
	// whose circumstances have since changed - a phone number cleared, the
	// switch turned off, the watermark moved.
	//
	// Found the hard way. A re-render enqueues a send; the send found an order
	// with no phone, concluded nobody could be asked, and sent six jobs to the
	// floor while their customer had the page open and was editing them. The
	// window is the expiry sweep's to close, and nobody else's.
	if _, err := w.server.store.Q.GetOpenPersonalisationLinkForOrder(ctx, orderID); err == nil {
		return
	}

	w.server.closePersonalisationWindow(ctx, orderID, by)
	w.server.triggerBatchPlan(ctx)
}

// openPersonalisationWindow holds an order's personalisable jobs and asks for
// its link, at the moment the jobs are created.
//
// EVERY ORDER GETS A WINDOW, which is what this exists to make true. The send
// used to be triggered only by a render finishing, so an order of things Tensor
// does not render - a Bone Tag, a DUOSNAP, a Couple's Date Plank - was never
// asked about at all: no link, no window, and a job that could reach a bed with
// nobody having checked the spelling.
//
// Held HERE rather than when the link is sent, and that is not a detail. A
// non-DNP job whose SKU already matched an approved design is batchable the
// instant it exists, and the planner runs on its own schedule - so between
// creation and the send there is a window in which the bed is formed, and
// HoldOrderPersonalisationForCustomer deliberately skips a job already on one.
// Holding first closes it.
//
// The send is only ASKED for. The sender re-checks every rule and waits for any
// model still coming (see unrenderedJobs), so a DNP order still gets its link
// after its previews exist. Anything it then refuses releases the order through
// releaseUnasked, so nothing is held for a message that is not coming.
func (s *Server) openPersonalisationWindow(ctx context.Context, order gen.Order) {
	log := obs.FromContext(ctx)

	// The switch first, because it decides whether to START holding orders -
	// and a service with the feature off must behave exactly as it did before
	// this existed, not hold every order for the fifteen minutes the stranded
	// backstop takes to notice.
	if !s.cfg.PersonalisationCheckEnabled {
		return
	}
	slug, err := s.brandForOrder(ctx, order.ID)
	if err != nil {
		log.Info("personalisation: not opening a window",
			"order", order.OrderNumber, "reason", err.Error())
		return
	}
	if _, reason := s.personalisationEligible(ctx, slug, order); reason != "" {
		log.Info("personalisation: not opening a window",
			"brand", slug, "order", order.OrderNumber, "reason", reason)
		return
	}

	held, err := s.store.Q.HoldOrderPersonalisationForCustomer(ctx, &order.ID)
	if err != nil {
		// Not fatal, and deliberately no return: the send is still worth
		// asking for. A job that was not held is one the customer's edit will
		// be refused on, which is a worse page rather than a wrong product.
		log.Error("personalisation: could not hold the order for its customer",
			"brand", slug, "order", order.OrderNumber, "error", err)
	}
	if len(held) == 0 {
		// Nothing on this order is personalised, so there is nothing to check
		// and no link worth sending.
		return
	}
	s.replateBedsLeftBy(ctx, held)

	if err := s.enqueuePersonalisationLinkForOrder(ctx, order.ID); err != nil {
		// The jobs are held and nothing will ask about them. Release now
		// rather than leaving the order waiting on the backstop.
		log.Error("personalisation: could not ask for the link, releasing the hold",
			"brand", slug, "order", order.OrderNumber, "error", err)
		worker := NewPersonalisationLinkWorker(s, s.baseLogger())
		worker.releaseUnasked(ctx, order.ID, "system (link could not be queued)")
		return
	}
	log.Info("personalisation window opened",
		"brand", slug, "order", order.OrderNumber, "jobs", held)
}

// closePersonalisationWindow is what happens when a customer's thirty minutes
// are over, however they ended.
//
// THREE PATHS END A WINDOW and they must end it identically: the customer
// pressed Confirm, the sweep found the link expired, or the sender decided
// nobody could be asked. Each one used to call
// ReleaseOrderPersonalisationToPrint for itself, so a rule added to the end of
// a window had three places to be added to and two of them would be found
// later, by somebody wondering why confirming behaved differently from waiting.
//
// Two things happen here, in this order. The order's jobs are released to the
// floor. Then anything on it that a designer has to remake is flagged, which
// is only possible now: while the window was open the customer could still
// change their mind, so a design file marked stale mid-window could be remade
// from values that then moved again.
//
// Returns nothing. Every failure is logged and none of them is worth refusing
// the caller for: a confirm that released the jobs and could not flag a stale
// file has still done the thing the customer asked for.
func (s *Server) closePersonalisationWindow(ctx context.Context, orderID uuid.UUID, by string) {
	log := obs.FromContext(ctx)
	if err := s.store.Q.ReleaseOrderPersonalisationToPrint(ctx,
		gen.ReleaseOrderPersonalisationToPrintParams{OrderID: &orderID, ValidatedBy: &by}); err != nil {
		log.Error("personalisation: could not release the order's jobs",
			"order", orderID, "error", err)
		return
	}

	// THE MODEL IS BUILT NOW, from what the customer finally said.
	//
	// While the window was open the render deliberately did not attach a
	// print file - see windowIsOpenFor - because a model attached then is a
	// model built from values that could still move. The window has closed,
	// so the values are final and this is the first moment the model can be
	// right.
	//
	// AFTER the release, not before. Releasing is a single statement over the
	// order's jobs; rendering is minutes of OpenSCAD per job, enqueued. Doing
	// it first would leave the jobs held if the enqueue failed, which is the
	// state nothing sweeps up. A released job with no model is simply not
	// batchable yet, which is a state the floor already understands.
	s.renderReleasedJobs(ctx, orderID)

	s.flagStaleDesignFiles(ctx, orderID)
}

// renderReleasedJobs builds the models for an order whose window has closed.
//
// Only the ones Tensor renders and only the ones still without a file: a job
// whose render beat the link already has its model, and re-rendering it would
// spend minutes of OpenSCAD to produce the same bytes.
//
// Best effort and logged. The window has closed either way, and a job left
// without a model is visibly not batchable rather than silently wrong - the
// design queue and the job's own issue reason both say so.
func (s *Server) renderReleasedJobs(ctx context.Context, orderID uuid.UUID) {
	log := obs.FromContext(ctx)
	jobs, err := s.store.Q.ListProductionJobsForOrder(ctx, &orderID)
	if err != nil {
		log.Error("personalisation: could not read the order's jobs to render them",
			"order", orderID, "error", err)
		return
	}
	pending := make([]gen.ProductionJob, 0, len(jobs))
	for _, j := range jobs {
		if j.PrintFileID != nil {
			continue
		}
		pending = append(pending, gen.ProductionJob(j))
	}
	if len(pending) == 0 || s.modelEnqueuer == nil {
		return
	}
	log.Info("personalisation: the window closed, building the final models",
		"order", orderID, "jobs", len(pending))

	// AT MOST ONE RENDER PER JOB, however many times this runs.
	//
	// A window can end twice in the same second: the customer presses Confirm
	// exactly as the expiry sweep reaches their link. Both endings are
	// correct and both ask for the order's models, and without this that is
	// two OpenSCAD runs per job writing the same bytes to the same storage
	// key and racing to attach them.
	for _, j := range pending {
		if !s.rendersProduct(ctx, deref(j.Sku), deref(j.ProductName)) {
			continue
		}
		if err := s.modelEnqueuer.EnqueueOnce(ctx, j.ID); err != nil {
			log.Error("personalisation: could not schedule the final model",
				"job", j.JobNumber, "error", err)
			continue
		}
		log.Info("final model scheduled", "job", j.JobNumber, "sku", deref(j.Sku))
	}
}

// flagStaleDesignFiles marks the order's hand-made items whose design file no
// longer matches what the customer asked for.
//
// Runs once, as the window closes, and only over the products Tensor does NOT
// render - a plank rebuilds itself and Part A's hold covers it. What is left is
// the job a person makes by hand from a 3MF somebody drew, where the only way
// the file catches up is for a designer to draw it again.
//
// Three jobs are skipped, each for its own reason:
//
//   - nothing was changed: personalisation_original is null, so the file still
//     describes the order and the designer has nothing to do;
//   - there is no file yet: the job already says so through stl_missing or
//     no_approved_design, and the design queue finds it by that;
//   - Tensor renders it: not a designer's work at any point.
func (s *Server) flagStaleDesignFiles(ctx context.Context, orderID uuid.UUID) {
	log := obs.FromContext(ctx)
	jobs, err := s.store.Q.ListProductionJobsForOrder(ctx, &orderID)
	if err != nil {
		log.Error("personalisation: could not read the order's jobs to flag stale designs",
			"order", orderID, "error", err)
		return
	}
	for _, j := range jobs {
		if len(j.PersonalisationOriginal) == 0 || j.PrintFileID == nil {
			continue
		}
		if s.rendersProduct(ctx, deref(j.Sku), deref(j.ProductName)) {
			continue
		}
		flagged, err := s.store.Q.MarkJobDesignFileStale(ctx, j.ID)
		if err != nil {
			log.Error("personalisation: could not flag a stale design file",
				"job", j.JobNumber, "error", err)
			continue
		}
		if flagged > 0 {
			log.Info("personalisation: the design file no longer matches the order",
				"job", j.JobNumber, "order", orderID)
		}
	}
}

// replateBedsLeftBy rebuilds every bed a hold just took a job off.
//
// Withdrawing a job from an approved bed without this leaves the bed's merged
// plate still containing it: the machine would print an item whose customer is
// at that moment being asked whether it is right. A Draft would be dissolved
// by the next plan anyway, but an approved bed is nobody's to dissolve, so its
// plate is rebuilt from what is actually on it.
//
// rebuildBatchAfterRemoval rather than RebuildBatchPlate, because the job
// taken off may have been the only one: that path withdraws the old plate from
// the printer queue first and closes a bed it has emptied, where
// RebuildBatchPlate would simply fail on a bed with no jobs.
func (s *Server) replateBedsLeftBy(ctx context.Context, held []*uuid.UUID) {
	seen := make(map[uuid.UUID]bool, len(held))
	for _, bed := range held {
		if bed == nil || seen[*bed] {
			continue
		}
		seen[*bed] = true
		s.rebuildBatchAfterRemoval(ctx, *bed)
	}
}

// personalisationWatchFrom is the instant this brand began asking customers.
//
// IT IS ONLY LAID DOWN WHILE THE CHECK IS ON, and that distinction is the
// difference between "every order from now on" and "every order since
// somebody first tried this out".
//
// Eligibility is deliberately evaluated with the switch off, so a dry run
// exercises every rule and logs whose order it WOULD have asked about. The
// watermark used to be created by that pass, and it is never moved once it
// exists - so a dry run on 13 October set the mark there, and switching the
// check on two days later would have reached back and messaged the customers
// of the 26 orders placed in between, about orders already in production or
// posted.
//
// With the check off this only reads. When no mark exists yet, the answer is
// NOW: nothing placed before this moment would be asked about, which is the
// honest thing for a dry run to report and exactly what the first live pass
// will then write down.
func (s *Server) personalisationWatchFrom(
	ctx context.Context, slug string,
) (time.Time, bool) {
	if !s.cfg.PersonalisationCheckEnabled {
		mark, err := s.store.Q.GetPersonalisationWatermark(ctx, slug)
		return watchFromOrNow(mark.WatchFrom, err), true
	}

	mark, err := s.store.Q.EnsurePersonalisationWatermark(ctx, slug)
	if err != nil {
		return time.Time{}, false
	}
	if !mark.WatchFrom.Valid {
		// A row with no instant is not something to interpret generously.
		return time.Time{}, false
	}
	return mark.WatchFrom.Time, true
}

// watchFromOrNow reads a watermark that may not be there yet.
//
// AN ABSENT MARK MEANS NOW, never "the beginning of time". The rule it feeds
// refuses an order placed at or before the mark, so a zero instant would make
// every order the shop has ever taken eligible at once - which is the exact
// outcome the watermark exists to prevent, arrived at by treating its absence
// as permission.
func watchFromOrNow(mark pgtype.Timestamptz, err error) time.Time {
	if err != nil || !mark.Valid {
		return time.Now()
	}
	return mark.Time
}
