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
	//
	// TWENTY-FOUR HOURS, which also happens to be the window Meta allows a
	// free-form reply in: past it the bot could only reach that customer with
	// a template anyway, so handing the chat back sooner buys nothing.
	//
	// Worth knowing what it still costs: a customer who replies two days
	// later to a thread a person was handling gets the bot, not that person.
	// That shows up in production as a conversation sitting at 'idle' with a
	// last_staff_at from earlier in the week. isClosing below is what keeps
	// the commonest version of it - being thanked after a resolved query -
	// from reopening a finished conversation whatever this is set to.
	HumanIdleTimeout = 24 * time.Hour
	// MaxMisses is how many unusable replies a flow tolerates before handoff.
	MaxMisses        = 2
	minOrderDigits   = 5
	maxOrderDigits   = 10
	keywordTrimSet   = " !.?,"
	menuKeyword      = "menu"
	startKeyword     = "start"
	stopKeyword      = "stop"
	changeNotePrefix = "WhatsApp personalisation change requested: "
)

// closings end a conversation rather than starting one.
//
// WHAT THEY MUST NOT GET IS THE MENU. A customer whose problem a person has
// just solved says "thank you" - and the bot answering that with a greeting
// and three buttons reopens a conversation that was finished, in front of
// the customer, immediately after a colleague closed it well. Silence is the
// right reply to being thanked.
var closings = set(
	"thanks", "thank", "thankyou", "ty", "tq",
	"ok", "okay", "okey", "k", "kk", "fine", "good", "great", "nice",
	"done", "noted", "sure", "cool", "got", "perfect", "welcome", "bye",
)

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

	if c.State == StateAwaitTrackOrder {
		return onOrderNumber(c, ev, PurposeTrack, now)
	}
	return onIdle(c, word, now)
}

// onIdle answers anything the bot was not expecting.
//
// A typed menu position - "2" - is turned into the tap it stands for by the
// caller, which can see the brand's own menu; by the time it reaches here it
// is already a ButtonIn. So anything still arriving as text gets the menu.
func onIdle(c Conversation, word string, _ time.Time) (Conversation, []Action) {
	if isClosing(word) {
		return c, nil
	}
	return menu(c)
}

// isClosing reports whether a message is somebody signing off.
//
// THE FIRST WORD, AND ONLY IN A SHORT MESSAGE. "Thanks" is a closing;
// "thanks but my order is still late" is a complaint that has to be
// answered, and matching on the word alone would swallow it. Four words is
// long enough for "thank you, for guide" - the message that prompted this -
// and short enough that a real question cannot hide inside it.
func isClosing(word string) bool {
	if word == "" {
		return false
	}
	fields := strings.Fields(word)
	if len(fields) == 0 || len(fields) > 4 {
		return false
	}
	first := strings.Trim(fields[0], keywordTrimSet)
	return closings[first]
}

func onMenuChoice(c Conversation, id string, now time.Time) (Conversation, []Action) {
	c = reset(c)
	switch id {
	case content.OptionTrack:
		c.State = StateAwaitTrackOrder
		return c, []Action{SendText{Text: content.AskOrderNumber}}
	case content.OptionDamage:
		// A LINK, NOT A CONVERSATION, and the conversation stays idle behind
		// it. The flow this replaced asked for the order number, looked it
		// up, asked for photographs and waited for DONE - and ended by
		// handing the chat to a person, which silences the bot for twelve
		// hours. Sending a form leaves the menu working.
		return c, []Action{SendDamageLink{}}
	case content.OptionTeam:
		return handoff(c, now, content.Handoff)
	}
	// ANYTHING ELSE IS A QUESTION, not a mistake. The shop adds and removes
	// FAQ rows from the dashboard, so an id this package has never seen is
	// ordinary - the caller looks it up and falls back to the menu when there
	// is nothing to say.
	return c, []Action{SendAnswer{Key: id}}
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

	c = reset(c)
	return c, []Action{withMenuHint(content.OrderStatus(r.Order, now))}
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

// menu sends the first message in the brand's tree.
//
// NO TEXT AND NO OPTIONS HERE. This package is pure and the menu is now the
// shop's to edit, so the machine says only WHICH message; the caller reads
// what it currently says.
func menu(c Conversation) (Conversation, []Action) {
	return reset(c), []Action{SendMenu{}}
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

func stateFor(Purpose) State { return StateAwaitTrackOrder }

// keyword is the lower-cased message with surrounding punctuation removed, so
// "Hi!" and "Menu." match. Non-text events have no keyword.
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
