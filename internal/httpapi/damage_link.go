package httpapi

// Minting the damage form and sending its address.
//
// The bot decides only that this customer should be given a link
// (bot.SendDamageLink); everything with a side effect is here - the token,
// the row, the message, and what to do when any of them fails.
//
// WHY A LINK AT ALL. The flow this replaced asked for the order number,
// looked it up live against Shopify, asked for photographs, then waited for
// the customer to type DONE. Four turns, two of which could be misread into a
// handoff, and the photographs then had to be fetched back out of Meta before
// anybody in the shop could look at them. A form asks for both things at once
// and the files arrive in object storage already.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/phone"
)

// damageLinkTTL is how long a damage form stays open.
//
// A day, against the personalisation link's much shorter window, because the
// two are racing different things. That one holds a printer: every extra hour
// is an order not being made. This one holds nothing - the parcel is already
// damaged - and the customer has to find the parcel, photograph it in decent
// light and come back. Asked for by the shop.
const damageLinkTTL = 24 * time.Hour

// sendDamageLink gives this customer a form to fill in.
//
// RESENDS RATHER THAN MINTS when they already hold a live one. Somebody who
// taps Damage twice - or taps it again after uploading two photographs - must
// land on the same form, or their uploads are orphaned behind a token nothing
// will ever submit. The partial unique index is what makes that race safe;
// this is the common path through it.
func (t *botTurn) sendDamageLink(ctx context.Context, q *gen.Queries) error {
	// ASKED BEFORE ANYTHING IS MINTED, and not left to the unique index to
	// catch. The index is PARTIAL - "one live link per person" holds over
	// 'sent' and 'opened' only - so a second claim inserts a 'pending' row
	// quite happily and the clash does not appear until the send has already
	// gone out and the row is settled to 'sent'. The customer would then have
	// two forms, one of which nothing will ever submit.
	//
	// Safe without a lock of its own because the whole turn runs inside
	// withWhatsAppConversation, which holds this customer's conversation row:
	// two taps from one person cannot be in here at once.
	if _, err := q.GetLiveDamageLinkForWaID(ctx, gen.GetLiveDamageLinkForWaIDParams{
		BrandSlug: t.brand, WaID: t.waID,
	}); err == nil {
		return t.remindDamageLink(ctx, q)
	}

	token, hash, err := newPersonalisationToken()
	if err != nil {
		return err
	}
	url := damageLinkURL(t.server.cfg.FrontendURL, token)
	if url == "" {
		// No FRONTEND_URL means no form, and a button with no address is
		// worse than a sentence asking them to send the photographs here.
		t.logger.Error("damage link not sent: FRONTEND_URL is not set", "brand", t.brand)
		return t.sendDamageFallback(ctx, q)
	}

	link, err := q.ClaimDamageLink(ctx, gen.ClaimDamageLinkParams{
		ID: uuid.New(), BrandSlug: t.brand, WaID: t.waID, TokenHash: hash,
		ExpiresAt: db.Timestamptz(ptr(time.Now().UTC().Add(damageLinkTTL))),
	})
	if err != nil {
		return err
	}
	if link.ID == uuid.Nil {
		// The index caught what the read above did not - a genuine race, or
		// a 'pending' row left by a crash. The raw token is NOT stored, so
		// an existing link cannot be re-sent as a URL; point them back at
		// the message they already have.
		return t.remindDamageLink(ctx, q)
	}

	res, sendErr := t.wa.SendCTAURL(ctx, t.waID,
		content.DamageLinkBody, content.DamageLinkButton, url)
	if sendErr != nil {
		// Released, not marked failed: nothing was sent, so the customer
		// should be able to ask again rather than be blocked by a row that
		// stands for a message they never got.
		if relErr := q.ReleaseDamageLink(ctx, link.ID); relErr != nil {
			t.logger.Warn("could not release an unsent damage link", "error", relErr)
		}
		t.logger.Warn("damage link not sent", "brand", t.brand,
			"wa", phone.Mask(t.waID), "error", sendErr)
		return river.JobCancel(sendErr)
	}

	status := res.Status
	if _, err := q.SettleDamageLink(ctx, gen.SettleDamageLinkParams{
		ID: link.ID, Status: "sent", MessageID: &res.MessageID,
	}); err != nil {
		return err
	}
	_, err = recordWhatsAppMessage(ctx, q, whatsappMessageRecord{
		Brand: t.brand, WaID: t.waID, MessageID: res.MessageID, Direction: directionOut,
		Kind: "cta_url", Body: content.DamageLinkBody, Status: &status, At: time.Now(),
	})
	return err
}

// remindDamageLink answers a second tap from somebody who already has a form.
func (t *botTurn) remindDamageLink(ctx context.Context, q *gen.Queries) error {
	res, err := t.wa.SendText(ctx, t.waID, content.DamageLinkAlready)
	return t.recordSend(ctx, q, res, err, "text", content.DamageLinkAlready)
}

// sendDamageFallback is the chat flow of last resort: ask for the order
// number and the photographs here, as the old bot did.
func (t *botTurn) sendDamageFallback(ctx context.Context, q *gen.Queries) error {
	res, err := t.wa.SendText(ctx, t.waID, content.DamageLinkFailed)
	return t.recordSend(ctx, q, res, err, "text", content.DamageLinkFailed)
}
