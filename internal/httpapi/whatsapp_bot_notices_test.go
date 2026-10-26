package httpapi

// The seam between the pure state machine and the editable wording.
//
// bot.Key* are plain strings matched against content.Notices() by value,
// because bot is pure and the registry lives beside the HTTP layer. Nothing
// but a test holds those two lists together, and the failure if they drift is
// quiet: the shop rewrites a message, saves it, and the bot keeps sending the
// old words for ever with no error anywhere.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/bot"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
)

// botKeys is every key the state machine tags a message with.
func botKeys() []string {
	return []string{
		bot.KeyStopAck, bot.KeyHandoff, bot.KeyMenuHint, bot.KeyAskOrderNumber,
		bot.KeyConfirmThanks, bot.KeyAskChangeText, bot.KeyAskChangeTextAgain,
		bot.KeyChangeReceived, bot.KeyFeedbackThanks,
	}
}

func TestEveryBotKeyIsAMessageTheShopCanEdit(t *testing.T) {
	for _, key := range botKeys() {
		if _, ok := content.NoticeByKey(key); !ok {
			t.Errorf("the bot tags a message %q that the Bot Messages page does not list, "+
				"so rewriting it would silently do nothing", key)
		}
	}
}

func TestEveryNoticeTheCodeSendsIsListed(t *testing.T) {
	// The httpapi-side keys, which are constants here rather than in bot.
	for _, key := range []string{
		noticeDamageLinkBody, noticeDamageLinkButton, noticeDamageLinkAlready,
		noticeDamageLinkFailed, noticeReprintScheduled, noticeReversePaymentAsk,
		noticeReversePaymentNoQR, noticeReversePickupBooked,
	} {
		if _, ok := content.NoticeByKey(key); !ok {
			t.Errorf("%q is sent but not listed, so nobody can edit it", key)
		}
	}
}

func TestANoticeNobodyWroteSendsTheShippedWording(t *testing.T) {
	var none notices
	for _, n := range content.Notices() {
		if got := none.text(n.Key); got != n.Default {
			t.Errorf("%s with nothing stored sent %q, want the shipped wording", n.Key, got)
		}
	}
}

func TestARewrittenNoticeIsWhatGetsSent(t *testing.T) {
	said := notices{noticeReversePickupBooked: "Van is coming. Keep it boxed."}
	if got := said.text(noticeReversePickupBooked); got != "Van is coming. Keep it boxed." {
		t.Errorf("sent %q, want the shop's wording", got)
	}
	// And a message they have NOT rewritten is unaffected by one they have.
	if got := said.text(noticeDamageLinkBody); got != content.DamageLinkBody {
		t.Error("rewriting one message changed another")
	}
}

func TestAnEmptyRewriteFallsBackRatherThanSendingNothing(t *testing.T) {
	// A blank row should never reach the database - the API refuses it - but
	// one that did must not send an empty WhatsApp message.
	said := notices{noticeDamageLinkBody: ""}
	if got := said.text(noticeDamageLinkBody); got != content.DamageLinkBody {
		t.Errorf("an empty stored message sent %q", got)
	}
}

func TestAPlaceholderCannotBeLostInARewrite(t *testing.T) {
	notice, ok := content.NoticeByKey("confirm_thanks")
	if !ok || notice.Placeholders != 1 {
		t.Fatal("confirm_thanks should take exactly one placeholder")
	}

	// THE CASE THAT REACHES A CUSTOMER. A rewrite that dropped the %s would
	// render as "%!s(MISSING)" - so format falls back to the default rather
	// than send it.
	said := notices{"confirm_thanks": "Thanks! We are making it now."}
	got := said.format("confirm_thanks", "T3DPS-7")
	if strings.Contains(got, "MISSING") || strings.Contains(got, "%!") {
		t.Errorf("a customer would receive %q", got)
	}
	if !strings.Contains(got, "T3DPS-7") {
		t.Errorf("the order number was lost: %q", got)
	}

	// A rewrite that KEEPS the placeholder is used, wherever they put it.
	said = notices{"confirm_thanks": "Order %s confirmed — making it now."}
	if got := said.format("confirm_thanks", "T3DPS-7"); got != "Order T3DPS-7 confirmed — making it now." {
		t.Errorf("got %q", got)
	}
}

func TestTheApiRefusesARewriteTheBotCouldNotSend(t *testing.T) {
	ask, _ := content.NoticeByKey("confirm_thanks")
	plain, _ := content.NoticeByKey("handoff")
	button, _ := content.NoticeByKey("damage_link_button")

	cases := []struct {
		name   string
		notice content.Notice
		body   string
		want   string // a word the refusal must contain
	}{
		{"empty", plain, "   ", "empty"},
		{"lost placeholder", ask, "Thanks, making it now.", "%s"},
		{"extra placeholder", ask, "Order %s for %s confirmed.", "once"},
		{"placeholder where none belongs", plain, "Hi %s", "cannot contain"},
		{"too long for a button", button, strings.Repeat("x", 25), "too long"},
	}
	for _, tc := range cases {
		got := content.ValidateNotice(tc.notice, tc.body)
		if got == "" {
			t.Errorf("%s was accepted", tc.name)
			continue
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s was refused with %q, which does not mention %q", tc.name, got, tc.want)
		}
	}

	// And the shipped wording of every message must pass its own rules -
	// otherwise the page refuses to save a message back to its default.
	for _, n := range content.Notices() {
		if refusal := content.ValidateNotice(n, n.Default); refusal != "" {
			t.Errorf("the shipped wording for %s is refused by its own rules: %s", n.Key, refusal)
		}
	}
}

func TestALiteralPercentIsNotAPlaceholder(t *testing.T) {
	// "20%% off" is a perfectly ordinary thing to write in a promotion, and
	// counting it as a placeholder would refuse the rewrite for a reason
	// nobody could see.
	if got := content.CountPlaceholders("Get 20%% off"); got != 0 {
		t.Errorf("counted %d placeholders in an escaped percent", got)
	}
	if got := content.CountPlaceholders("Order %s: 20%% off"); got != 1 {
		t.Errorf("counted %d, want 1", got)
	}
}

func TestAnUnknownKeyIsLoudRatherThanEmpty(t *testing.T) {
	var none notices
	// A mistyped key must not send a blank WhatsApp message - that is a bug
	// a customer sees and nobody else does.
	if got := none.text("not_a_real_key"); got == "" {
		t.Error("an unknown key would send an empty message")
	}
}

// --- Editing the wording, and the bot actually using it. ---
//
// THE WHOLE POINT OF THE FEATURE, and the part that cannot be proved by a
// unit test: a rewrite saved on the Bot Messages page has to reach the very
// next WhatsApp message, with nothing restarted. The send path reads the
// table every time rather than caching it, and this is what holds that
// decision in place - a cache added later for performance would pass every
// other test in this file and fail only here.

func TestIntegrationARewrittenMessageIsWhatTheBotSends(t *testing.T) {
	h := newWAHarness(t, nil)
	editor := h.minter.mint(t, []string{
		auth.WhatsAppRead.Key(), auth.WhatsAppReply.Key(),
	})

	// What it says before anybody touches it.
	h.tap(t, content.OptionDamage)
	if before := h.meta.last(t).Text; before != content.DamageLinkBody {
		t.Fatalf("the shipped wording was not sent: %q", before)
	}

	// Rewritten through the API the page uses, not by writing the row.
	rewritten := "Sorry about that! Tap below and send us the photos. 📷"
	rr := doJSON(h.router, http.MethodPut,
		"/brands/"+waBrand+"/whatsapp/bot-messages/damage_link_body", editor,
		map[string]any{"body": rewritten})
	if rr.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rr.Code, rr.Body.String())
	}

	// NOTHING RESTARTED, NOTHING INVALIDATED. The next message uses it.
	h.resetDamageLink(t)
	h.tap(t, content.OptionDamage)
	if got := h.meta.last(t).Text; got != rewritten {
		t.Errorf("the bot sent %q, want the shop's own wording - an edit that saves and "+
			"does not take effect is the failure this feature exists to avoid", got)
	}

	// And putting it back restores the shipped wording rather than freezing
	// today's copy of it.
	rr = doJSON(h.router, http.MethodDelete,
		"/brands/"+waBrand+"/whatsapp/bot-messages/damage_link_body", editor, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("reset = %d %s", rr.Code, rr.Body.String())
	}
	h.resetDamageLink(t)
	h.tap(t, content.OptionDamage)
	if got := h.meta.last(t).Text; got != content.DamageLinkBody {
		t.Errorf("after reset the bot sent %q, want the shipped wording", got)
	}
}

func TestIntegrationTheApiRefusesWordingTheBotCouldNotSend(t *testing.T) {
	h := newWAHarness(t, nil)
	editor := h.minter.mint(t, []string{
		auth.WhatsAppRead.Key(), auth.WhatsAppReply.Key(),
	})

	for _, tc := range []struct{ name, key, body string }{
		// Each of these reaches a CUSTOMER if it gets through: a blank
		// message, "crafting order  now", or "%!s(MISSING)".
		{"empty", "handoff", "   "},
		{"a lost order number", "confirm_thanks", "Thanks! Making it now."},
		{"a second order number", "confirm_thanks", "Order %s for %s confirmed."},
		{"longer than WhatsApp allows", "damage_link_button", strings.Repeat("x", 30)},
	} {
		rr := doJSON(h.router, http.MethodPut,
			"/brands/"+waBrand+"/whatsapp/bot-messages/"+tc.key, editor,
			map[string]any{"body": tc.body})
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s was accepted (%d)", tc.name, rr.Code)
		}
	}

	// A key nobody defined is refused rather than stored and never read.
	rr := doJSON(h.router, http.MethodPut,
		"/brands/"+waBrand+"/whatsapp/bot-messages/not_a_message", editor,
		map[string]any{"body": "hello"})
	if rr.Code != http.StatusNotFound {
		t.Errorf("an unknown message key = %d, want 404", rr.Code)
	}
}

func TestIntegrationSavingTheOriginalBackIsNotAnEdit(t *testing.T) {
	h := newWAHarness(t, nil)
	editor := h.minter.mint(t, []string{
		auth.WhatsAppRead.Key(), auth.WhatsAppReply.Key(),
	})

	// STORING THE DEFAULT WOULD FREEZE IT. A row always wins over the
	// binary, so a brand that saved today's wording would never receive a
	// later improvement to it - and would have no way of knowing.
	rr := doJSON(h.router, http.MethodPut,
		"/brands/"+waBrand+"/whatsapp/bot-messages/handoff", editor,
		map[string]any{"body": content.Handoff})
	if rr.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rr.Code, rr.Body.String())
	}

	var body struct {
		Messages []struct {
			Key    string `json:"key"`
			Edited bool   `json:"edited"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, m := range body.Messages {
		if m.Key == "handoff" && m.Edited {
			t.Error("saving the original wording back was recorded as an edit")
		}
	}
}

// resetDamageLink releases the live damage form, so the next tap mints a new
// one and sends the covering message again rather than the "you already have
// one" reminder.
func (h *waHarness) resetDamageLink(t *testing.T) {
	t.Helper()
	if _, err := h.store.Pool.Exec(h.ctx,
		`DELETE FROM damage_links WHERE brand_slug = $1`, waBrand); err != nil {
		t.Fatalf("release the damage form: %v", err)
	}
}
