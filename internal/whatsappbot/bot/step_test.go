package bot

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/order"
)

const waID = "919876543210"

var now = time.Date(2026, 10, 13, 6, 0, 0, 0, time.UTC)

func idle() Conversation { return Conversation{WaID: waID, State: StateIdle} }

func in(state State) Conversation {
	c := idle()
	c.State = state
	return c
}

func texts(actions []Action) []string {
	var out []string
	for _, a := range actions {
		switch v := a.(type) {
		case SendText:
			out = append(out, v.Text)
		case SendMenu:
			out = append(out, v.Text)
		}
	}
	return out
}

func assertMenu(t *testing.T, c Conversation, actions []Action) {
	t.Helper()
	if c.State != StateIdle {
		t.Fatalf("state = %s, want idle", c.State)
	}
	if len(actions) != 1 {
		t.Fatalf("want 1 action, got %#v", actions)
	}
	m, ok := actions[0].(SendMenu)
	if !ok || !reflect.DeepEqual(m.Menu, content.MainMenu) {
		t.Fatalf("want the main menu, got %#v", actions[0])
	}
}

func TestMainMenuButtons(t *testing.T) {
	want := []string{"Damage concern", "Explore products", "Other"}
	if got := titles(content.MainMenu); !reflect.DeepEqual(got, want) {
		t.Fatalf("main menu = %v", got)
	}
}

func titles(m content.Menu) []string {
	var out []string
	for _, o := range m.Options {
		out = append(out, o.Title)
	}
	return out
}

func TestMenusFitWhatsAppButtons(t *testing.T) {
	for _, m := range []content.Menu{content.MainMenu} {
		if len(m.Options) == 0 || len(m.Options) > 3 {
			t.Errorf("a reply-button message carries 1 to 3 buttons, got %d", len(m.Options))
		}
		for _, o := range m.Options {
			if len([]rune(o.Title)) > 20 {
				t.Errorf("button %q is longer than WhatsApp's 20 characters", o.Title)
			}
		}
	}
}

// Meta refuses the whole send if a row is too long, and the refusal names
// neither the row nor the rule - so the shape is checked here, where the copy
// is written, rather than discovered against the live number.
func TestFaqListFitsWhatsAppLimits(t *testing.T) {
	if n := len(content.FaqList.Rows); n == 0 || n > 10 {
		t.Errorf("a list carries 1 to 10 rows, got %d", n)
	}
	if len([]rune(content.FaqList.Button)) > 20 {
		t.Errorf("the list button %q is over 20 characters", content.FaqList.Button)
	}
	seen := map[string]bool{}
	for _, r := range content.FaqList.Rows {
		if len([]rune(r.Title)) > 24 {
			t.Errorf("row %q is over WhatsApp's 24 characters", r.Title)
		}
		if len([]rune(r.Description)) > 72 {
			t.Errorf("the description on %q is over 72 characters", r.Title)
		}
		// A duplicate id would answer the wrong question, silently.
		if seen[r.ID] {
			t.Errorf("two rows share the id %q", r.ID)
		}
		seen[r.ID] = true
	}
}

// Every row has to land somewhere. A row that produced nothing, or that fell
// back to the main menu, reads to the customer as the bot ignoring them.
func TestEveryFaqRowIsAnswered(t *testing.T) {
	for _, r := range content.FaqList.Rows {
		_, actions := Step(idle(), ButtonIn{ID: r.ID}, now)
		if len(actions) == 0 {
			t.Errorf("row %q produced no reply", r.Title)
			continue
		}
		if _, isMenu := actions[0].(SendMenu); isMenu {
			t.Errorf("row %q fell through to the main menu", r.Title)
		}
	}
}

func TestOtherOpensTheFaqList(t *testing.T) {
	for _, ev := range []Event{ButtonIn{ID: content.OptionOther}, TextIn{Text: "3"}} {
		c, actions := Step(idle(), ev, now)
		want := []Action{SendList{Text: content.FaqIntro, List: content.FaqList}}
		if c.State != StateIdle || !reflect.DeepEqual(actions, want) {
			t.Fatalf("%#v: got %s %#v", ev, c.State, actions)
		}
	}
}

// A question resolves to a KEY, not to words. The shop rewrites answers and
// adds questions from the dashboard, so the machine says which answer and the
// caller looks up what it currently says.
func TestAQuestionAsksForItsAnswerByKey(t *testing.T) {
	c, actions := Step(idle(), ButtonIn{ID: content.FaqCOD}, now)
	want := []Action{SendAnswer{Key: content.FaqCOD}}
	if c.State != StateIdle || !reflect.DeepEqual(actions, want) {
		t.Fatalf("COD: %s %#v", c.State, actions)
	}

	// An id this build has never heard of is ordinary, not an error: it is a
	// question somebody added in the dashboard. It must not fall through to
	// the menu here, or a new question would silently do nothing.
	c, actions = Step(idle(), ButtonIn{ID: "faq_added_last_tuesday"}, now)
	if !reflect.DeepEqual(actions, []Action{SendAnswer{Key: "faq_added_last_tuesday"}}) {
		t.Fatalf("unknown id: %#v", actions)
	}
	_ = c
}

func TestTalkToUs(t *testing.T) {
	c, actions := Step(idle(), ButtonIn{ID: content.OptionTeam}, now)
	assertHandoff(t, c, actions)
}

func TestGreetingShowsMenu(t *testing.T) {
	for _, msg := range []string{"hi", "Hi!", "HELLO", "hey", "menu", "what's up", "anything"} {
		c, actions := Step(idle(), TextIn{Text: msg}, now)
		assertMenu(t, c, actions)
		if c.LastCustomerAt == nil || !c.LastCustomerAt.Equal(now) {
			t.Fatalf("LastCustomerAt not set for %q", msg)
		}
	}
}

func TestMediaWhileIdleShowsMenu(t *testing.T) {
	c, actions := Step(idle(), MediaIn{Kind: "image"}, now)
	assertMenu(t, c, actions)
}

func TestMenuChoices(t *testing.T) {
	cases := []struct {
		event Event
		state State
		text  string
	}{
		// Track is the only choice that still starts a conversation. Damage
		// is a link now and has its own test.
		{ButtonIn{ID: content.OptionTrack}, StateAwaitTrackOrder, content.AskOrderNumber},
	}
	for _, tc := range cases {
		c, actions := Step(idle(), tc.event, now)
		if c.State != tc.state {
			t.Errorf("%#v: state %s want %s", tc.event, c.State, tc.state)
		}
		if got := texts(actions); len(got) != 1 || got[0] != tc.text {
			t.Errorf("%#v: texts %v", tc.event, got)
		}
	}
}

func TestExplore(t *testing.T) {
	for _, ev := range []Event{ButtonIn{ID: content.OptionExplore}} {
		c, actions := Step(idle(), ev, now)
		if c.State != StateIdle {
			t.Fatalf("state = %s", c.State)
		}
		got := texts(actions)
		if len(got) != 1 || !strings.Contains(got[0], "the3dprintingstore.in") || !strings.Contains(got[0], "MENU") {
			t.Fatalf("explore texts %v", got)
		}
	}
}

func TestMenuButtonWorksFromAnyFlow(t *testing.T) {
	c, _ := Step(in(StateAwaitChangeText), ButtonIn{ID: content.OptionTrack}, now)
	if c.State != StateAwaitTrackOrder {
		t.Fatalf("state = %s", c.State)
	}
}

func TestDigitsOnlyMeanMenuChoiceWhenIdle(t *testing.T) {
	c, actions := Step(in(StateAwaitTrackOrder), TextIn{Text: "1"}, now)
	if c.State != StateAwaitTrackOrder || len(texts(actions)) != 1 || texts(actions)[0] != content.InvalidOrderNumber {
		t.Fatalf("got %s %v", c.State, texts(actions))
	}
}

func TestOrderNumberParsing(t *testing.T) {
	for _, msg := range []string{"115766", "T3DPS-115766", "#115766", "my order is 115766 pls check", "t3dps 115766"} {
		_, actions := Step(in(StateAwaitTrackOrder), TextIn{Text: msg}, now)
		want := []Action{LookupOrder{Purpose: PurposeTrack, Number: "115766"}}
		if !reflect.DeepEqual(actions, want) {
			t.Errorf("%q: got %#v", msg, actions)
		}
	}
}

func TestInvalidOrderNumberRetriesOnceThenHandsOff(t *testing.T) {
	c, actions := Step(in(StateAwaitTrackOrder), TextIn{Text: "where is it"}, now)
	if c.State != StateAwaitTrackOrder || texts(actions)[0] != content.InvalidOrderNumber {
		t.Fatalf("first miss: %s %v", c.State, texts(actions))
	}
	c, actions = Step(c, TextIn{Text: "dunno"}, now)
	assertHandoff(t, c, actions)
}

func assertHandoff(t *testing.T, c Conversation, actions []Action) {
	t.Helper()
	if c.State != StateHuman || c.HumanSince == nil {
		t.Fatalf("expected human state, got %+v", c)
	}
	if got := texts(actions); len(got) == 0 || got[len(got)-1] != content.Handoff {
		t.Fatalf("expected handoff text, got %v", got)
	}
}

func shipped() order.Order {
	return order.Order{
		ID: "gid://shopify/Order/1", Name: "T3DPS-115766", CreatedAt: now, FulfillmentStatus: "FULFILLED",
		Fulfillments: []order.Fulfillment{{DisplayStatus: "IN_TRANSIT", Tracking: []order.Tracking{{Company: "Delhivery", Number: "599"}}}},
	}
}

func TestTrackFound(t *testing.T) {
	c, actions := Step(in(StateAwaitTrackOrder), LookupResult{Purpose: PurposeTrack, Outcome: OrderFound, Order: shipped()}, now)
	if c.State != StateIdle {
		t.Fatalf("state = %s", c.State)
	}
	got := texts(actions)
	if len(got) != 1 || !strings.Contains(got[0], "Delhivery") || !strings.Contains(got[0], "MENU") {
		t.Fatalf("texts %v", got)
	}
}

func TestTrackNotFoundAndMismatch(t *testing.T) {
	c, actions := Step(in(StateAwaitTrackOrder), LookupResult{Purpose: PurposeTrack, Outcome: OrderNotFound}, now)
	if c.State != StateAwaitTrackOrder || texts(actions)[0] != content.OrderNotFound {
		t.Fatalf("not found: %s %v", c.State, texts(actions))
	}
	c, actions = Step(c, LookupResult{Purpose: PurposeTrack, Outcome: OrderPhoneMismatch}, now)
	assertHandoff(t, c, actions)
}

func TestStaleLookupResultIgnored(t *testing.T) {
	c, actions := Step(idle(), LookupResult{Purpose: PurposeTrack, Outcome: OrderFound, Order: shipped()}, now)
	if c.State != StateIdle || len(actions) != 0 {
		t.Fatalf("stale result acted on: %s %#v", c.State, actions)
	}
}

// Tapping Damage asks for a form and NOTHING ELSE.
//
// Two things are pinned here that the old conversational flow got wrong. The
// conversation stays IDLE: that flow ended in a handoff, and StateHuman
// silences the bot for twelve hours, so the menu stopped working right after
// somebody reported damage. And the scratchpad stays empty - the link is
// minted against the phone number, not against an order, because the order
// number is the thing the form is for.
func TestDamageSendsALink(t *testing.T) {
	for _, ev := range []Event{ButtonIn{ID: content.OptionDamage}, TextIn{Text: "1"}} {
		c, actions := Step(idle(), ev, now)
		if !reflect.DeepEqual(actions, []Action{SendDamageLink{}}) {
			t.Fatalf("%#v: actions %#v", ev, actions)
		}
		if c.State != StateIdle {
			t.Fatalf("%#v: state = %s, want idle so the menu still works", ev, c.State)
		}
		if !reflect.DeepEqual(c.Ctx, Context{}) {
			t.Fatalf("%#v: context = %+v, want empty", ev, c.Ctx)
		}
	}
}

// Damage from inside another flow still sends the link and abandons the flow
// it interrupted, rather than reading the tap as that flow's answer.
func TestDamageInterruptsAnotherFlow(t *testing.T) {
	c, actions := Step(in(StateAwaitTrackOrder), ButtonIn{ID: content.OptionDamage}, now)
	if !reflect.DeepEqual(actions, []Action{SendDamageLink{}}) {
		t.Fatalf("actions %#v", actions)
	}
	if c.State != StateIdle {
		t.Fatalf("state = %s", c.State)
	}
}

func human(since time.Time) Conversation {
	c := in(StateHuman)
	c.HumanSince = &since
	return c
}

func TestHumanModeIsSilent(t *testing.T) {
	c, actions := Step(human(now.Add(-time.Hour)), TextIn{Text: "hi"}, now)
	if c.State != StateHuman || len(actions) != 0 {
		t.Fatalf("bot spoke in human mode: %v", texts(actions))
	}
}

func TestHumanModeResumesAfterIdleTimeout(t *testing.T) {
	c, actions := Step(human(now.Add(-13*time.Hour)), TextIn{Text: "hi"}, now)
	assertMenu(t, c, actions)
}

func TestRecentStaffActivityKeepsHumanMode(t *testing.T) {
	c := human(now.Add(-20 * time.Hour))
	staff := now.Add(-time.Hour)
	c.LastStaffAt = &staff
	c, actions := Step(c, TextIn{Text: "hello"}, now)
	if c.State != StateHuman || len(actions) != 0 {
		t.Fatalf("resumed despite recent staff reply")
	}
}

func TestMenuExitsHumanMode(t *testing.T) {
	c, actions := Step(human(now.Add(-time.Minute)), TextIn{Text: "MENU"}, now)
	assertMenu(t, c, actions)
}

func TestHumanKeyword(t *testing.T) {
	c, actions := Step(idle(), TextIn{Text: "human"}, now)
	assertHandoff(t, c, actions)
}

func TestStaffEchoSwitchesToHuman(t *testing.T) {
	c, actions := Step(in(StateAwaitTrackOrder), StaffEcho{}, now)
	if c.State != StateHuman || c.LastStaffAt == nil || c.HumanSince == nil || len(actions) != 0 {
		t.Fatalf("echo handling: %+v %#v", c, actions)
	}
}

func TestStopAndStart(t *testing.T) {
	c, actions := Step(idle(), TextIn{Text: "STOP"}, now)
	if !c.OptedOut || texts(actions)[0] != content.StopAck {
		t.Fatalf("stop: %+v %v", c, texts(actions))
	}
	c, actions = Step(c, TextIn{Text: "hi"}, now)
	if len(actions) != 0 {
		t.Fatalf("opted-out customer got %v", texts(actions))
	}
	c, actions = Step(c, TextIn{Text: "start"}, now)
	if c.OptedOut {
		t.Fatal("start did not opt back in")
	}
	assertMenu(t, c, actions)
}

func TestConfirmationCorrect(t *testing.T) {
	c, actions := Step(idle(), ConfirmationReply{OrderID: "gid://shopify/Order/7", OrderName: "T3DPS-7", Confirmed: true}, now)
	want := []Action{
		RecordConfirmation{OrderID: "gid://shopify/Order/7", Status: ConfirmationConfirmed},
		TagOrder{OrderID: "gid://shopify/Order/7", Tag: content.TagConfirmed},
		SendText{Text: "Thank you for confirming! ✅ We'll start crafting order T3DPS-7 now."},
	}
	if !reflect.DeepEqual(actions, want) || c.State != StateIdle {
		t.Fatalf("got %s %#v", c.State, actions)
	}
}

func TestConfirmationChange(t *testing.T) {
	c, actions := Step(idle(), ConfirmationReply{OrderID: "gid://shopify/Order/7", OrderName: "T3DPS-7"}, now)
	if c.State != StateAwaitChangeText || c.Ctx.OrderID != "gid://shopify/Order/7" || !strings.Contains(texts(actions)[0], "T3DPS-7") {
		t.Fatalf("change: %+v %v", c, texts(actions))
	}

	c, actions = Step(c, MediaIn{Kind: "image"}, now)
	if texts(actions)[0] != content.AskChangeTextAgain {
		t.Fatalf("media during change: %v", texts(actions))
	}

	c, actions = Step(c, TextIn{Text: "First name: RAHUL\nSecond: PRIYA"}, now)
	want := []Action{
		AppendNote{OrderID: "gid://shopify/Order/7", Note: "WhatsApp personalisation change requested: First name: RAHUL\nSecond: PRIYA"},
		TagOrder{OrderID: "gid://shopify/Order/7", Tag: content.TagChangeRequested},
		RecordConfirmation{OrderID: "gid://shopify/Order/7", Status: ConfirmationChangeRequested, ChangeText: "First name: RAHUL\nSecond: PRIYA"},
		SendText{Text: "Thanks, we've noted your changes for order T3DPS-7. Our team will confirm them with you here before we start crafting."},
	}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("change actions %#v", actions)
	}
	if c.State != StateHuman {
		t.Fatalf("state = %s", c.State)
	}
}

func TestChangeTextKeywordsAreTreatedAsDetails(t *testing.T) {
	// A customer whose corrected name is literally "Hi" must not get the menu.
	c := in(StateAwaitChangeText)
	c.Ctx = Context{OrderID: "gid://shopify/Order/7", OrderName: "T3DPS-7"}
	c, _ = Step(c, TextIn{Text: "hi"}, now)
	if c.State != StateHuman {
		t.Fatalf("state = %s", c.State)
	}
}
