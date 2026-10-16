package bot

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
)

const (
	// HumanIdleTimeout is how long a handed-off chat stays with staff after the
	// last human activity before the bot answers again.
	HumanIdleTimeout = 12 * time.Hour
	// MaxMisses is how many unusable replies a flow tolerates before handoff.
	MaxMisses        = 2
	minOrderDigits   = 5
	maxOrderDigits   = 10
	keywordTrimSet   = " !.?,"
	kindImage        = "image"
	kindVideo        = "video"
	doneKeyword      = "done"
	menuKeyword      = "menu"
	startKeyword     = "start"
	stopKeyword      = "stop"
	damageNoteFmt    = "WhatsApp damage report: %d photos. See the WhatsApp chat."
	changeNotePrefix = "WhatsApp personalisation change requested: "
)

// numberedChoices lets customers type a main-menu position instead of tapping.
var numberedChoices = map[string]string{
	"1": content.OptionTrack,
	"2": content.OptionDamage,
	"3": content.OptionMore,
}

var (
	greetings     = set("hi", "hii", "hiii", "hello", "hey", "helo", menuKeyword)
	humanKeywords = set("human", "agent", "support")
	digitRuns     = regexp.MustCompile(`\d+`)
)

// Step applies one event to a conversation.
func Step(c Conversation, ev Event, now time.Time) (Conversation, []Action) {
	switch e := ev.(type) {
	case StaffEcho:
		return toHumanByStaff(c, now), nil
	case LookupResult:
		return onLookup(c, e, now)
	case ConfirmationReply:
		c.LastCustomerAt = &now
		return onConfirmation(c, e)
	default:
		return onCustomer(c, ev, now)
	}
}

func onCustomer(c Conversation, ev Event, now time.Time) (Conversation, []Action) {
	c.LastCustomerAt = &now
	word := keyword(ev)

	switch {
	case word == stopKeyword:
		c.OptedOut = true
		return reset(c), []Action{SendText{Text: content.StopAck}}
	case word == startKeyword:
		c.OptedOut = false
		return menu(c)
	case c.OptedOut:
		return c, nil
	}

	if c.State == StateHuman {
		if word != menuKeyword && !humanExpired(c, now) {
			return c, nil
		}
		c = reset(c)
	}

	if b, ok := ev.(ButtonIn); ok {
		return onMenuChoice(c, b.ID, now)
	}
	if word == menuKeyword {
		return menu(c)
	}
	// Corrected personalisation is free text: "Hi" may be a name, not a greeting.
	if c.State == StateAwaitChangeText {
		return onChangeText(c, ev, now)
	}
	if greetings[word] {
		return menu(c)
	}
	if humanKeywords[word] {
		return handoff(c, now, content.Handoff)
	}

	switch c.State {
	case StateAwaitTrackOrder:
		return onOrderNumber(c, ev, PurposeTrack, now)
	case StateAwaitDamageOrder:
		return onOrderNumber(c, ev, PurposeDamage, now)
	case StateAwaitDamagePhotos:
		return onDamagePhotos(c, ev, word, now)
	default:
		return onIdle(c, word, now)
	}
}

func onIdle(c Conversation, word string, now time.Time) (Conversation, []Action) {
	if id, ok := numberedChoices[word]; ok {
		return onMenuChoice(c, id, now)
	}
	return menu(c)
}

func onMenuChoice(c Conversation, id string, now time.Time) (Conversation, []Action) {
	c = reset(c)
	switch id {
	case content.OptionTrack:
		c.State = StateAwaitTrackOrder
		return c, []Action{SendText{Text: content.AskOrderNumber}}
	case content.OptionDamage:
		c.State = StateAwaitDamageOrder
		return c, []Action{SendText{Text: content.AskOrderNumber}}
	case content.OptionMore:
		return c, []Action{SendMenu{Text: content.MoreIntro, Menu: content.MoreMenu}}
	case content.OptionExplore:
		return c, []Action{withMenuHint(content.Explore)}
	case content.OptionDeliveryPayment:
		return c, []Action{withMenuHint(content.DeliveryPaymentInfo)}
	case content.OptionTeam:
		return handoff(c, now, content.Handoff)
	}
	return menu(c)
}

func withMenuHint(text string) SendText {
	return SendText{Text: text + "\n\n" + content.MenuHint}
}

func onOrderNumber(c Conversation, ev Event, purpose Purpose, now time.Time) (Conversation, []Action) {
	t, ok := ev.(TextIn)
	if !ok {
		return c, []Action{SendText{Text: content.AskOrderNumber}}
	}
	number := parseOrderNumber(t.Text)
	if number == "" {
		return miss(c, now, content.InvalidOrderNumber)
	}
	return c, []Action{LookupOrder{Purpose: purpose, Number: number}}
}

func onLookup(c Conversation, r LookupResult, now time.Time) (Conversation, []Action) {
	if c.State != stateFor(r.Purpose) {
		return c, nil // the customer moved on before the lookup finished
	}
	switch r.Outcome {
	case OrderNotFound:
		return miss(c, now, content.OrderNotFound)
	case OrderPhoneMismatch:
		return miss(c, now, content.PhoneMismatch)
	}

	if r.Purpose == PurposeTrack {
		c = reset(c)
		return c, []Action{withMenuHint(content.OrderStatus(r.Order, now))}
	}
	c.State = StateAwaitDamagePhotos
	c.Ctx = Context{OrderID: r.Order.ID, OrderName: r.Order.Name}
	return c, []Action{SendText{Text: content.AskDamagePhotos}}
}

func onDamagePhotos(c Conversation, ev Event, word string, now time.Time) (Conversation, []Action) {
	if m, ok := ev.(MediaIn); ok && (m.Kind == kindImage || m.Kind == kindVideo) {
		c.Ctx.Photos++
		if c.Ctx.Photos == 1 {
			return c, []Action{SendText{Text: content.PhotoReceived}}
		}
		return c, nil
	}
	if c.Ctx.Photos == 0 {
		return c, []Action{SendText{Text: content.DamageNeedPhotos}}
	}
	if word != doneKeyword {
		return c, []Action{SendText{Text: content.PhotoReceived}}
	}

	orderID, orderName, photos := c.Ctx.OrderID, c.Ctx.OrderName, c.Ctx.Photos
	c, reply := handoff(c, now, fmt.Sprintf(content.DamageReported, orderName))
	return c, append([]Action{
		TagOrder{OrderID: orderID, Tag: content.TagDamageReported},
		AppendNote{OrderID: orderID, Note: fmt.Sprintf(damageNoteFmt, photos)},
	}, reply...)
}

func onConfirmation(c Conversation, r ConfirmationReply) (Conversation, []Action) {
	if r.Confirmed {
		return c, []Action{
			RecordConfirmation{OrderID: r.OrderID, Status: ConfirmationConfirmed},
			TagOrder{OrderID: r.OrderID, Tag: content.TagConfirmed},
			SendText{Text: fmt.Sprintf(content.ConfirmThanks, r.OrderName)},
		}
	}
	c = reset(c)
	c.State = StateAwaitChangeText
	c.Ctx = Context{OrderID: r.OrderID, OrderName: r.OrderName}
	return c, []Action{SendText{Text: fmt.Sprintf(content.AskChangeText, r.OrderName)}}
}

func onChangeText(c Conversation, ev Event, now time.Time) (Conversation, []Action) {
	t, ok := ev.(TextIn)
	text := ""
	if ok {
		text = strings.TrimSpace(t.Text)
	}
	if text == "" {
		return c, []Action{SendText{Text: content.AskChangeTextAgain}}
	}

	orderID, orderName := c.Ctx.OrderID, c.Ctx.OrderName
	c, reply := handoff(c, now, fmt.Sprintf(content.ChangeReceived, orderName))
	return c, append([]Action{
		AppendNote{OrderID: orderID, Note: changeNotePrefix + text},
		TagOrder{OrderID: orderID, Tag: content.TagChangeRequested},
		RecordConfirmation{OrderID: orderID, Status: ConfirmationChangeRequested, ChangeText: text},
	}, reply...)
}

// miss counts an unusable reply: retry once, then hand off to a human.
func miss(c Conversation, now time.Time, retryText string) (Conversation, []Action) {
	c.Ctx.Misses++
	if c.Ctx.Misses >= MaxMisses {
		return handoff(c, now, content.Handoff)
	}
	return c, []Action{SendText{Text: retryText}}
}

func menu(c Conversation) (Conversation, []Action) {
	return reset(c), []Action{SendMenu{Text: content.Greeting, Menu: content.MainMenu}}
}

func handoff(c Conversation, now time.Time, text string) (Conversation, []Action) {
	c = reset(c)
	c.State = StateHuman
	c.HumanSince = &now
	return c, []Action{SendText{Text: text}}
}

func toHumanByStaff(c Conversation, now time.Time) Conversation {
	if c.State != StateHuman {
		c = reset(c)
		c.State = StateHuman
		c.HumanSince = &now
	}
	c.LastStaffAt = &now
	return c
}

func reset(c Conversation) Conversation {
	c.State = StateIdle
	c.Ctx = Context{}
	return c
}

func humanExpired(c Conversation, now time.Time) bool {
	last := c.HumanSince
	if c.LastStaffAt != nil && (last == nil || c.LastStaffAt.After(*last)) {
		last = c.LastStaffAt
	}
	return last == nil || now.Sub(*last) > HumanIdleTimeout
}

func stateFor(p Purpose) State {
	if p == PurposeDamage {
		return StateAwaitDamageOrder
	}
	return StateAwaitTrackOrder
}

// keyword is the lower-cased message with surrounding punctuation removed, so
// "Hi!" and "DONE." match. Non-text events have no keyword.
func keyword(ev Event) string {
	t, ok := ev.(TextIn)
	if !ok {
		return ""
	}
	return strings.Trim(strings.ToLower(strings.TrimSpace(t.Text)), keywordTrimSet)
}

// parseOrderNumber picks the longest digit run, so the "3" in "T3DPS-115766"
// is ignored.
func parseOrderNumber(text string) string {
	best := ""
	for _, run := range digitRuns.FindAllString(text, -1) {
		if len(run) > len(best) {
			best = run
		}
	}
	if len(best) < minOrderDigits || len(best) > maxOrderDigits {
		return ""
	}
	return best
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
