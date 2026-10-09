package httpapi

// The scheduler that rings a customer a few minutes after they walk away.
//
// It sweeps every connected store's abandoned checkouts, picks the ones that
// have gone quiet long enough, and places one call each. The sweep is
// deliberately dull: the interesting decisions are all about who NOT to call,
// and each of them is a separate, named rule below.
//
// SAFE BY DEFAULT. With WINBACK_CALLS_ENABLED unset the scheduler runs in full
// - reads Shopify, applies every rule, logs exactly who it would ring - and
// places no calls. That is not a debug mode bolted on; it is how you check the
// rules against a real store before anybody's phone rings, and it is the state
// the system ships in.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/sarvam"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/integrations/whatsapp"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// WinbackCallWorker sweeps abandoned checkouts and calls the ones that qualify.
type WinbackCallWorker struct {
	river.WorkerDefaults[production.WinbackCallsArgs]

	server *Server
	logger *slog.Logger
}

func NewWinbackCallWorker(server *Server, logger *slog.Logger) *WinbackCallWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &WinbackCallWorker{server: server, logger: logger}
}

// Timeout bounds one sweep. A Shopify page, then at most a handful of calls.
func (w *WinbackCallWorker) Timeout(*river.Job[production.WinbackCallsArgs]) time.Duration {
	return 5 * time.Minute
}

// Work runs one sweep. An empty BrandSlug means every connected store.
func (w *WinbackCallWorker) Work(
	ctx context.Context, job *river.Job[production.WinbackCallsArgs],
) error {
	return w.sweep(ctx, job.Args.BrandSlug)
}

// Sweep runs one pass outside River, for `winbackworker -once`.
//
// The periodic tick only fires on the River leader, so a worker started beside
// the API never sweeps on its own - which makes "what would the rules do right
// now?" unanswerable without this. With calling disabled it is a read.
func (w *WinbackCallWorker) Sweep(ctx context.Context) error {
	return w.sweep(ctx, "")
}

func (w *WinbackCallWorker) sweep(ctx context.Context, brandSlug string) error {
	if slug := strings.TrimSpace(brandSlug); slug != "" {
		return w.sweepBrand(ctx, slug)
	}

	conns, err := w.server.store.Q.ListConnectedShopifyBrands(ctx)
	if err != nil {
		return fmt.Errorf("list connected shopify stores: %w", err)
	}
	for _, conn := range conns {
		// One store failing does not abandon the others: brands are
		// independent, and stopping at the first bad token would leave the
		// rest unswept for no reason anybody can see.
		if err := w.sweepBrand(ctx, conn.BrandSlug); err != nil {
			w.logger.Error("win-back sweep failed for a brand",
				"brand", conn.BrandSlug, "error", err)
		}
	}
	return nil
}

// candidate is one checkout that passed every rule, ready to be rung.
type candidate struct {
	checkout shopify.AbandonedCheckout
	phone    string
}

// Channels, as the ledger stores them.
const (
	channelVoice    = "voice"
	channelWhatsApp = "whatsapp"
)

// winbackChannel is one way of reaching a customer in this sweep.
type winbackChannel struct {
	Name string
	// Enabled is CONSENT, not configuration. False means this pass runs every
	// rule and logs who it WOULD have reached, contacting nobody.
	Enabled   bool
	MaxPerRun int
}

// winbackChannels decides which channels this sweep will run.
//
// A PURE FUNCTION, and the reason is the bug it replaces: sweepBrand used to
// return at the top when Sarvam was unconfigured, so a brand with WhatsApp and
// no Sarvam was skipped ENTIRELY - and the line it logged ("Sarvam is not
// connected") is also what a brand with nothing connected logs, so the two
// could not be told apart.
//
// Returns only the connected channels. Empty means this brand has no way to
// reach anybody, which is the one case worth returning early on.
func winbackChannels(cfg config.Settings, voice, whatsapp bool) []winbackChannel {
	var out []winbackChannel
	if voice {
		out = append(out, winbackChannel{
			Name: channelVoice, Enabled: cfg.WinbackCallsEnabled,
			MaxPerRun: cfg.WinbackCallMaxPerRun,
		})
	}
	if whatsapp {
		out = append(out, winbackChannel{
			Name: channelWhatsApp, Enabled: cfg.WinbackWhatsAppEnabled,
			MaxPerRun: cfg.WinbackWhatsAppMaxPerRun,
		})
	}
	return out
}

// doneFor is the set of numbers this channel has already been through.
//
// Pure, so the rule can be tested without a database. Keyed on the normalised
// phone, which is what the ledger stores and what eligible compares against.
func doneFor(contacts []gen.ListWinbackContactsRow, channel string) map[string]bool {
	out := make(map[string]bool, len(contacts))
	for _, row := range contacts {
		if row.Channel == channel {
			out[row.Phone] = true
		}
	}
	return out
}

func (w *WinbackCallWorker) sweepBrand(ctx context.Context, slug string) error {
	cfg := w.server.cfg

	// Resolved per brand: a brand that has entered its own credentials in
	// Settings uses those, and one that has not keeps whatever the process was
	// started with.
	voice := w.server.sarvamFor(ctx, slug)
	messenger := w.server.whatsappFor(ctx, slug)
	discount, linkHost := w.server.winbackLinkFor(ctx, slug)

	// A message needs somewhere to send somebody. Without a link host there is
	// no button, and a win-back message whose whole job is to reopen the
	// basket is not worth sending without one.
	canMessage := messenger.Configured() && linkHost != ""

	channels := winbackChannels(cfg, voice.Configured(), canMessage)
	if len(channels) == 0 {
		w.logger.Info("win-back sweep skipped: no channel is connected", "brand", slug)
		return nil
	}

	conn, err := w.server.store.Q.GetConnectionWithToken(ctx,
		gen.GetConnectionWithTokenParams{BrandSlug: slug, Provider: shopifyProvider})
	shop, token, connected := shopifyCredentials(conn, err)
	if !connected {
		return nil
	}

	// When this brand started being watched. Set to now on the very first
	// sweep, so the backlog already sitting in Shopify is skipped once and
	// forever rather than contacted on startup.
	mark, err := w.server.store.Q.EnsureWinbackWatermark(ctx, slug)
	if err != nil {
		return fmt.Errorf("read the watch mark for %s: %w", slug, err)
	}

	// Read from the LATER of the watch mark and the age limit. Before the mark
	// there is nothing to contact, and a worker watching for a month should
	// not ask Shopify for a month of history every five minutes.
	//
	// EXCEPT when an allow list is set. That gate reaches past the watch mark
	// by design - it names one checkout deliberately - and narrowing the fetch
	// here would mean the named checkout never arrives to be allowed, so the
	// testing gate would silently do nothing. Found exactly that way.
	watchFrom := mark.WatchFrom.Time
	since := time.Now().Add(-time.Duration(cfg.WinbackCallMaxAgeHours) * time.Hour)
	if len(cfg.WinbackCallOnly) == 0 && watchFrom.After(since) {
		since = watchFrom
	}
	// ONE Shopify read for every channel. Both fire at the same T+10 off the
	// same list, so a second fetch would be a second page to answer the same
	// question.
	checkouts, err := w.server.shopify.ListAbandonedCheckouts(ctx, shop, token, 0, since)
	if err != nil {
		return fmt.Errorf("list abandoned checkouts for %s: %w", slug, err)
	}

	contacts, err := w.server.store.Q.ListWinbackContacts(ctx, slug)
	if err != nil {
		return fmt.Errorf("read the win-back history for %s: %w", slug, err)
	}

	// ONE PASS PER CHANNEL, and a failure in one is logged rather than
	// returned - the same shape sweep() already uses for brands, for the same
	// reason. The channels are independent: no credit at Sarvam must not stop
	// the messages, and Meta refusing a template must not stop the calls.
	for _, ch := range channels {
		if err := w.runChannel(ctx, slug, ch, checkouts, contacts, watchFrom,
			voice, messenger, discount, linkHost); err != nil {
			w.logger.Error("win-back channel failed",
				"brand", slug, "channel", ch.Name, "error", err)
		}
	}
	return nil
}

// runChannel is one channel's whole pass over this brand's abandoned carts.
func (w *WinbackCallWorker) runChannel(
	ctx context.Context, slug string, ch winbackChannel,
	checkouts []shopify.AbandonedCheckout, contacts []gen.ListWinbackContactsRow,
	watchFrom time.Time, voice *sarvam.Client, messenger *whatsapp.Client,
	discount, linkHost string,
) error {
	// "Already done" is PER CHANNEL now: a customer who was rung is still due
	// a message.
	ready := w.eligible(checkouts, doneFor(contacts, ch.Name), watchFrom)
	if len(ready) == 0 {
		w.logger.Info("win-back: nothing due",
			"brand", slug, "channel", ch.Name, "checkouts", len(checkouts))
		return nil
	}

	// The dry run is the default, and it is PER CHANNEL - which is the whole
	// point of two switches. Every rule has run, the list is real, and nobody
	// is contacted. Switching a channel on is an act.
	if !ch.Enabled {
		for _, c := range ready {
			w.logger.Info("win-back: WOULD contact (this channel is disabled)",
				"brand", slug, "channel", ch.Name, "checkout", c.checkout.Name,
				"customer", c.checkout.CustomerName, "value", c.checkout.TotalAmount)
		}
		w.logger.Info("win-back pass complete, nothing sent",
			"brand", slug, "channel", ch.Name, "due", len(ready), "enabled", false)
		return nil
	}

	// A SEPARATE counter per channel. A shared one would let a full voice pass
	// consume the budget and send no messages at all, with the only evidence a
	// "reached the cap" line attributed to the wrong channel.
	sent := 0
	for _, c := range ready {
		if sent >= ch.MaxPerRun {
			w.logger.Info("win-back: reached the per-sweep cap",
				"brand", slug, "channel", ch.Name, "cap", ch.MaxPerRun)
			break
		}
		var ok bool
		switch ch.Name {
		case channelVoice:
			ok = w.call(ctx, slug, c, voice)
		case channelWhatsApp:
			ok = w.message(ctx, slug, c, messenger, discount, linkHost)
		}
		if ok {
			sent++
		}
	}
	w.logger.Info("win-back pass complete",
		"brand", slug, "channel", ch.Name, "due", len(ready), "sent", sent)
	return nil
}

// eligible applies every rule about who may be rung, in order of cost.
//
// Each is a separate test on purpose. A single compound condition would be
// shorter and would make it impossible to say, of a customer who was called,
// which rule let them through.
func (w *WinbackCallWorker) eligible(
	checkouts []shopify.AbandonedCheckout, alreadyCalled map[string]bool, watchFrom time.Time,
) []candidate {
	cfg := w.server.cfg
	now := time.Now()
	delay := time.Duration(cfg.WinbackCallDelayMinutes) * time.Minute
	oldest := now.Add(-time.Duration(cfg.WinbackCallMaxAgeHours) * time.Hour)

	only := make(map[string]bool, len(cfg.WinbackCallOnly))
	for _, name := range cfg.WinbackCallOnly {
		only[strings.TrimPrefix(strings.TrimSpace(name), "#")] = true
	}

	out := make([]candidate, 0, len(checkouts))
	for _, checkout := range checkouts {
		// NEW CARTS ONLY. Anything already in Shopify's list when this brand
		// was first swept is somebody who walked away days or weeks ago and
		// has heard nothing since - a call now is a cold call, not a win-back.
		// The allow list is the one thing that reaches past this, because it
		// names a single checkout deliberately and is how the rules get tested
		// against a real one.
		if len(only) == 0 && !checkout.CreatedAt.After(watchFrom) {
			continue
		}
		// A cart the customer came back and paid for. Shopify leaves these in
		// the abandoned list, so without this the agent rings somebody about a
		// basket they bought days ago.
		if checkout.CompletedAt != nil {
			continue
		}
		// Too soon. Ringing at four minutes catches somebody still typing
		// their card number.
		if now.Sub(checkout.CreatedAt) < delay {
			continue
		}
		// Too old. A basket left three weeks ago is not a win-back, it is a
		// cold call.
		if checkout.CreatedAt.Before(oldest) {
			continue
		}
		phone := normaliseIndianMobile(checkout.Phone)
		if phone == "" {
			continue
		}
		// One call per PERSON, ever - not per checkout. Somebody who abandons
		// three carts in a fortnight is one person.
		if alreadyCalled[phone] {
			continue
		}
		// The testing gate. While WINBACK_CALL_ONLY names anything, NOTHING
		// else is ever called, whatever the rules above allow - so the
		// scheduler can be run against the live store with one real checkout
		// and no risk to the rest.
		if len(only) > 0 && !only[strings.TrimPrefix(checkout.Name, "#")] {
			continue
		}
		out = append(out, candidate{checkout: checkout, phone: phone})
	}
	return out
}

// call claims the number, places the call, and records what happened.
//
// Returns whether a call actually reached Sarvam.
func (w *WinbackCallWorker) call(
	ctx context.Context, slug string, c candidate, client *sarvam.Client,
) bool {
	// CLAIMED BEFORE DIALLING. A row written afterwards leaves a window in
	// which this process can die with the customer's phone ringing and nothing
	// recorded - and the next sweep rings them again. Claiming first means a
	// crash costs a missed call, which is recoverable, rather than a repeated
	// one, which is not.
	claim, err := w.server.store.Q.ClaimWinbackContact(ctx, gen.ClaimWinbackContactParams{
		ID: uuid.New(), BrandSlug: slug,
		CheckoutID: c.checkout.ID, CheckoutName: c.checkout.Name,
		CustomerName: c.checkout.CustomerName, Phone: c.phone,
		Channel: channelVoice,
	})
	if err != nil {
		// No row: another replica claimed this number between our read and
		// our write. Exactly what the unique index is for - but LOGGED, not
		// silent, because until 0098 the index and the read asked different
		// questions and a legitimate candidate vanished here on every sweep
		// with nothing to show for it.
		w.logger.Info("win-back: already claimed on this channel",
			"brand", slug, "channel", channelVoice, "checkout", c.checkout.Name)
		return false
	}

	result, callErr := client.Call(ctx, sarvam.CallRequest{
		CustomerNumber: c.phone,
		Variables:      w.server.agentVariables(requestFor(c)),
	})
	if callErr != nil {
		// Nothing rang, so the number goes back. Otherwise one refusal - no
		// credit, Sarvam down - would mark a customer called forever over a
		// call that never happened.
		_ = w.server.store.Q.ReleaseWinbackContact(ctx, claim.ID)
		w.logger.Error("win-back call refused",
			"brand", slug, "checkout", c.checkout.Name, "error", callErr)
		return false
	}

	detail := ""
	attempt := result.AttemptID
	if _, err := w.server.store.Q.SettleWinbackContact(ctx, gen.SettleWinbackContactParams{
		ID: claim.ID, Status: "placed", AttemptID: &attempt, Detail: &detail,
	}); err != nil {
		// The call is already placed; failing to record its id costs the join
		// to the call log, not a repeat call - the claim row still holds the
		// number.
		w.logger.Error("win-back call placed but not recorded",
			"brand", slug, "checkout", c.checkout.Name, "attempt", attempt, "error", err)
	}
	// The customer's number is NOT logged. It is personal data and the
	// checkout name is enough to find them.
	w.logger.Info("win-back call placed",
		"brand", slug, "checkout", c.checkout.Name, "attempt", attempt)
	return true
}

// message claims the number for WhatsApp, sends the template, and records what
// happened. Returns whether Meta accepted a message.
//
// The same claim-send-settle shape as call(), and deliberately so: the two
// channels differ in what they send, not in how they avoid contacting the same
// person twice.
func (w *WinbackCallWorker) message(
	ctx context.Context, slug string, c candidate,
	client *whatsapp.Client, discount, linkHost string,
) bool {
	// BUILT BEFORE CLAIMING. A refusal here is deterministic - a missing
	// recovery URL, a host the template cannot address - so there is no point
	// taking the claim only to hand it straight back.
	link := buildWinbackLink(c.checkout.RecoveryURL, discount, linkHost)
	if link.Param == "" {
		// Still claimed and recorded, because this will refuse identically on
		// every future sweep. The row is what stops it being retried forever
		// and what tells somebody why this cart was never messaged.
		if claim, err := w.claimFor(ctx, slug, c, channelWhatsApp); err == nil {
			w.settle(ctx, claim.ID, "failed", "", link.Why)
		}
		w.logger.Warn("win-back message: no link could be built",
			"brand", slug, "checkout", c.checkout.Name, "reason", link.Why)
		return false
	}

	claim, err := w.claimFor(ctx, slug, c, channelWhatsApp)
	if err != nil {
		w.logger.Info("win-back: already claimed on this channel",
			"brand", slug, "channel", channelWhatsApp, "checkout", c.checkout.Name)
		return false
	}

	result, sendErr := client.SendTemplate(ctx, whatsapp.TemplateMessage{
		To:          c.phone,
		BodyParams:  winbackBodyParams(c.checkout, discount),
		ButtonParam: link.Param,
	})
	if sendErr != nil {
		// TRANSIENT GOES BACK, PERMANENT STAYS. A rate limit or an outage must
		// not mark somebody messaged over a message that never left; a wrong
		// template or an unapproved number will refuse identically forever, so
		// retrying it every five minutes is noise and the row is the only
		// record anybody will ever see of why.
		var metaErr *whatsapp.Error
		if errors.As(sendErr, &metaErr) && metaErr.Permanent() {
			w.settle(ctx, claim.ID, "failed", "", sendErr.Error())
			w.logger.Error("win-back message refused, and it will refuse again",
				"brand", slug, "checkout", c.checkout.Name,
				"code", metaErr.Code, "error", sendErr)
			return false
		}
		_ = w.server.store.Q.ReleaseWinbackContact(ctx, claim.ID)
		w.logger.Error("win-back message failed, will try again",
			"brand", slug, "checkout", c.checkout.Name, "error", sendErr)
		return false
	}

	// 'sent', NOT 'delivered'. Meta's 200 means it took the message, not that
	// a phone showed it - it accepts sends to numbers that are not on WhatsApp
	// and drops them. Without the delivery webhook this is the strongest claim
	// the data supports.
	w.settle(ctx, claim.ID, "sent", result.MessageID, result.Status)
	// The customer's number is NOT logged. It is personal data and the
	// checkout name is enough to find them.
	w.logger.Info("win-back message sent",
		"brand", slug, "checkout", c.checkout.Name, "message", result.MessageID)
	return true
}

// claimFor takes the right to contact this person on this channel.
func (w *WinbackCallWorker) claimFor(
	ctx context.Context, slug string, c candidate, channel string,
) (gen.AbandonedCheckoutCall, error) {
	return w.server.store.Q.ClaimWinbackContact(ctx, gen.ClaimWinbackContactParams{
		ID: uuid.New(), BrandSlug: slug,
		CheckoutID: c.checkout.ID, CheckoutName: c.checkout.Name,
		CustomerName: c.checkout.CustomerName, Phone: c.phone,
		Channel: channel,
	})
}

// settle records the outcome against a claim.
//
// Failing to record is logged and swallowed: the message is already sent, so
// losing the row costs the audit trail, not a repeat - the claim still holds
// the number.
func (w *WinbackCallWorker) settle(
	ctx context.Context, id uuid.UUID, status, attemptID, detail string,
) {
	params := gen.SettleWinbackContactParams{ID: id, Status: status, Detail: &detail}
	if attemptID != "" {
		params.AttemptID = &attemptID
	}
	if _, err := w.server.store.Q.SettleWinbackContact(ctx, params); err != nil {
		w.logger.Error("win-back: could not record the outcome",
			"id", id, "status", status, "error", err)
	}
}

// requestFor turns a checkout into the agent's variables.
//
// Reuses the same shape the Call button sends, so a scheduled call and a
// hand-pressed one say the same things in the same words.
func requestFor(c candidate) placeVoiceCallRequest {
	return placeVoiceCallRequest{
		CustomerName:   c.checkout.CustomerName,
		CustomerNumber: c.phone,
		ProductName:    productPhrase(c.checkout),
		CartValue:      spokenAmount(c.checkout.TotalAmount),
		ItemCount:      c.checkout.ItemCount,
	}
}

// productPhrase is everything in the basket, as one phrase the agent can say.
//
// All the titles, not just the first: a customer who left a Dual Name Plank
// AND a Soulmate Combo hears about both, and hearing only one is how a call
// sounds like it is about somebody else's basket.
func productPhrase(c shopify.AbandonedCheckout) string {
	seen := make(map[string]bool, len(c.LineItems))
	titles := make([]string, 0, len(c.LineItems))
	for _, item := range c.LineItems {
		title := strings.TrimSpace(item.Title)
		if title == "" || seen[strings.ToLower(title)] {
			continue
		}
		seen[strings.ToLower(title)] = true
		titles = append(titles, title)
	}
	switch len(titles) {
	case 0:
		return ""
	case 1:
		return titles[0]
	default:
		return strings.Join(titles[:len(titles)-1], ", ") + " and " + titles[len(titles)-1]
	}
}

// spokenAmount is the cart total as a number to be said aloud.
//
// Shopify sends "3499.00", which a speech engine reads as "three thousand four
// hundred and ninety nine point zero zero". Whole rupees lose their decimals;
// a real paise amount keeps two.
//
// PARSED, not trimmed. Trimming trailing zeroes turns "3499.0" into "3499." -
// which a test caught, and which would have been read aloud as a number with a
// full stop in it.
func spokenAmount(amount string) string {
	value, err := strconv.ParseFloat(strings.TrimSpace(amount), 64)
	if err != nil {
		return amount
	}
	if value == math.Trunc(value) {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'f', 2, 64)
}
