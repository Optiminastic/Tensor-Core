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
	if _, ok := actions[0].(SendMenu); !ok {
		t.Fatalf("want the menu, got %#v", actions[0])
	}
}

// The default tree is what a brand that has never opened the page behaves
// as, so its shape has to be one WhatsApp will actually send.
func TestTheDefaultTreeFitsWhatsApp(t *testing.T) {
	nodes := content.DefaultTree()
	keys := map[string]bool{}
	for _, n := range nodes {
		keys[n.Key] = true
	}
	if !keys[content.RootNode] {
		t.Fatalf("the default tree has no root node")
	}

	for _, n := range nodes {
		limit := content.MaxButtons
		if n.Kind == content.NodeList {
			limit = content.MaxRows
		}
		if len(n.Options) == 0 || len(n.Options) > limit {
			t.Errorf("%s carries %d options, want 1 to %d", n.Key, len(n.Options), limit)
		}
		if n.Body == "" {
			t.Errorf("%s has nothing to say", n.Key)
		}
		for _, o := range n.Options {
			switch o.Action {
			case content.ActionAnswer:
				if o.Answer == "" {
					t.Errorf("%q answers nothing", o.Title)
				}
			case content.ActionOpen:
				// A button that opens a message which is not in the tree is
				// the quietest way for this to break.
				if !keys[o.Target] {
					t.Errorf("%q opens %q, which is not in the tree", o.Title, o.Target)
				}
			case content.ActionURL:
				if o.Target == "" {
					t.Errorf("%q is a link to nowhere", o.Title)
				}
			default:
				// A code action's id is what the state machine listens for,
				// so it is not free to be anything.
				if content.CodeActionID(o.Action) != o.ID {
					t.Errorf("%q has action %q but id %q", o.Title, o.Action, o.ID)
				}
			}
		}
	}
}

// Every option id must be unique across the WHOLE tree, not just its own
// message: WhatsApp sends back the id alone, so two options sharing one
// answer each other's taps.
func TestDefaultTreeIdsAreUnique(t *testing.T) {
	seen := map[string]string{}
	for _, n := range content.DefaultTree() {
		for _, o := range n.Options {
			if where, taken := seen[o.ID]; taken {
				t.Errorf("id %q is on both %s and %s", o.ID, where, n.Key)
			}
			seen[o.ID] = n.Key
		}
	}
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

// Anything that is not one of the three state-changing flows leaves this
// package as a key for the caller to resolve - including options the shop
// invented, which is the whole point of the tree. "Explore products" used to
// be a case in the switch here; it is a row with a URL on it now.
func TestAnOrdinaryOptionIsResolvedByTheCaller(t *testing.T) {
	for _, id := range []string{content.OptionExplore, content.OptionOther, "faq_gift_wrap"} {
		c, actions := Step(idle(), ButtonIn{ID: id}, now)
		if !reflect.DeepEqual(actions, []Action{SendAnswer{Key: id}}) {
			t.Errorf("%s: actions %#v", id, actions)
		}
		if c.State != StateIdle {
			t.Errorf("%s: state = %s", id, c.State)
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
	// Only the tap. A typed "1" is turned into the tap it stands for by the
	// caller, which can see the brand's own menu - this package cannot.
	for _, ev := range []Event{ButtonIn{ID: content.OptionDamage}} {
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
	c, actions := Step(human(now.Add(-HumanIdleTimeout-time.Hour)), TextIn{Text: "hi"}, now)
	assertMenu(t, c, actions)
}

// A chat a person is still working stays theirs for the whole window: a
// customer chasing an update must reach the colleague handling it, not a menu.
func TestHumanModeHoldsInsideTheWindow(t *testing.T) {
	c, actions := Step(human(now.Add(-HumanIdleTimeout/2)), TextIn{Text: "any update?"}, now)
	if c.State != StateHuman || len(actions) != 0 {
		t.Fatalf("the bot spoke inside the handover window: %s %v", c.State, texts(actions))
	}
}

// Being thanked is not a reason to reopen a conversation somebody has just
// closed well. This is the message that prompted the change.
func TestClosingsGetNoReply(t *testing.T) {
	for _, text := range []string{
		"Thank you, for guide", "thanks", "Thanks!", "ok", "Okay 👍", "got it", "Noted.", "bye",
	} {
		c, actions := Step(idle(), TextIn{Text: text}, now)
		if len(actions) != 0 {
			t.Errorf("%q was answered with %v", text, texts(actions))
		}
		if c.State != StateIdle {
			t.Errorf("%q changed the state to %s", text, c.State)
		}
	}
}

// A complaint that opens with a pleasantry is still a complaint. Matching on
// the first word alone would swallow it.
func TestAClosingWordInALongerMessageStillGetsTheMenu(t *testing.T) {
	for _, text := range []string{
		"thanks but my order is still not delivered",
		"ok so when will it ship, it has been a week",
	} {
		_, actions := Step(idle(), TextIn{Text: text}, now)
		if len(actions) == 0 {
			t.Errorf("%q was ignored", text)
		}
	}
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
