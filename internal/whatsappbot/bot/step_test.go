package bot

import (
	"fmt"
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
		// Track still asks for the order number alone; damage asks for the
		// number and the photographs in one message.
		{ButtonIn{ID: content.OptionTrack}, StateAwaitTrackOrder, content.AskOrderNumber},
		{ButtonIn{ID: content.OptionDamage}, StateAwaitDamageOrder, content.AskDamageDetails},
		{TextIn{Text: "1"}, StateAwaitDamageOrder, content.AskDamageDetails},
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
	c, _ := Step(in(StateAwaitDamagePhotos), ButtonIn{ID: content.OptionTrack}, now)
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

func TestDamageFlow(t *testing.T) {
	c, _ := Step(idle(), ButtonIn{ID: content.OptionDamage}, now)
	c, actions := Step(c, TextIn{Text: "115766"}, now)
	if !reflect.DeepEqual(actions, []Action{LookupOrder{Purpose: PurposeDamage, Number: "115766"}}) {
		t.Fatalf("lookup actions %#v", actions)
	}
	c, actions = Step(c, LookupResult{Purpose: PurposeDamage, Outcome: OrderFound, Order: shipped()}, now)
	if c.State != StateAwaitDamagePhotos || c.Ctx.OrderID != "gid://shopify/Order/1" || texts(actions)[0] != content.AskDamagePhotos {
		t.Fatalf("after lookup: %+v %v", c, texts(actions))
	}

	c, actions = Step(c, TextIn{Text: "done"}, now)
	if texts(actions)[0] != content.DamageNeedPhotos || c.State != StateAwaitDamagePhotos {
		t.Fatalf("done without photos: %v", texts(actions))
	}

	c, actions = Step(c, MediaIn{Kind: "image", MessageID: "wamid.A"}, now)
	if texts(actions)[0] != content.PhotoReceived {
		t.Fatalf("first photo: %v", texts(actions))
	}
	c, actions = Step(c, MediaIn{Kind: "image", MessageID: "wamid.B"}, now)
	if len(actions) != 0 {
		t.Fatalf("second photo should be silent, got %v", texts(actions))
	}
	// An audio note is not a damage photograph, and must not be counted as
	// one - the customer would then be told their photos were received.
	c, _ = Step(c, MediaIn{Kind: "audio", MessageID: "wamid.C"}, now)
	if c.Ctx.Photos != 2 || len(c.Ctx.PhotoMessages) != 2 {
		t.Fatalf("audio counted as a photo: %d, %v", c.Ctx.Photos, c.Ctx.PhotoMessages)
	}

	c, actions = Step(c, TextIn{Text: "Done"}, now)
	if c.State != StateHuman {
		t.Fatalf("state after DONE = %s", c.State)
	}
	// RecordDamage FIRST. The two Shopify writes after it go through
	// requireShop, which cancels the job when a brand has no connected store;
	// Tensor's own record must be made before anything that can do that.
	want := []Action{
		RecordDamage{
			OrderID: "gid://shopify/Order/1", OrderName: "T3DPS-115766",
			Photos: 2, PhotoMessages: []string{"wamid.A", "wamid.B"},
		},
		TagOrder{OrderID: "gid://shopify/Order/1", Tag: content.TagDamageReported},
		AppendNote{OrderID: "gid://shopify/Order/1", Note: "WhatsApp damage report: 2 photos. See the WhatsApp chat."},
	}
	if !reflect.DeepEqual(actions[:3], want) {
		t.Fatalf("damage actions %#v", actions)
	}
	if !strings.Contains(texts(actions)[0], "T3DPS-115766") {
		t.Fatalf("damage reply %v", texts(actions))
	}
	// The scratchpad is wiped, and that is correct: the conversation has left
	// the flow, and the durable record is the damage_reports row. Pinned so
	// nobody later "fixes" the wipe to keep the order id around.
	if !reflect.DeepEqual(c.Ctx, Context{}) {
		t.Fatalf("context survived the handoff: %+v", c.Ctx)
	}
}

// The wamid list is bounded; the count is not. A report of thirty photographs
// still says thirty, and links the first twenty.
func TestThePhotoListIsBounded(t *testing.T) {
	c, _ := Step(idle(), ButtonIn{ID: content.OptionDamage}, now)
	c, _ = Step(c, TextIn{Text: "115766"}, now)
	c, _ = Step(c, LookupResult{Purpose: PurposeDamage, Outcome: OrderFound, Order: shipped()}, now)

	for i := 0; i < maxDamagePhotos+10; i++ {
		c, _ = Step(c, MediaIn{Kind: "image", MessageID: fmt.Sprintf("wamid.%d", i)}, now)
	}
	if c.Ctx.Photos != maxDamagePhotos+10 {
		t.Errorf("counted %d photos, want every one of them", c.Ctx.Photos)
	}
	if len(c.Ctx.PhotoMessages) != maxDamagePhotos {
		t.Errorf("linked %d messages, want the cap of %d", len(c.Ctx.PhotoMessages), maxDamagePhotos)
	}
}

// A conversation that was already mid-flow when PhotoMessages shipped has a
// count and no ids. It must still be able to finish.
func TestAFlowFromBeforeThePhotoListStillFinishes(t *testing.T) {
	c := in(StateAwaitDamagePhotos)
	c.Ctx = Context{OrderID: "gid://shopify/Order/1", OrderName: "T3DPS-115766", Photos: 2}

	c, actions := Step(c, TextIn{Text: "DONE"}, now)
	rec, ok := actions[0].(RecordDamage)
	if !ok {
		t.Fatalf("first action %#v, want RecordDamage", actions[0])
	}
	if rec.Photos != 2 || len(rec.PhotoMessages) != 0 {
		t.Errorf("record = %+v", rec)
	}
	if c.State != StateHuman {
		t.Errorf("state = %s", c.State)
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
