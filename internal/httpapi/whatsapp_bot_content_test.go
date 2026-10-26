package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
)

// The overlay must leave a brand that has written nothing exactly as it was.
func TestNoOverridesMeansTheCodeIsUnchanged(t *testing.T) {
	tree := treeFrom(content.DefaultTree())

	root, ok := tree.root()
	if !ok {
		t.Fatal("the default tree has no root")
	}
	// TWO, and they are the new/returning question. The menu of things the
	// bot can do is the message BEHIND them now.
	if len(root.Options) != 2 {
		t.Fatalf("the greeting carries %d buttons, want the two customer answers",
			len(root.Options))
	}
	// Both answers have to land somewhere, or the first message is a dead end.
	for _, o := range root.Options {
		if o.Action != content.ActionOpen || o.Target != content.MenuNode {
			t.Errorf("%q does not open the menu (action %q, target %q)",
				o.Title, o.Action, o.Target)
		}
	}
	for _, key := range []string{content.MenuNode, content.QuestionsNode} {
		if _, found := tree.node(key); !found {
			t.Errorf("the %q message is missing", key)
		}
	}
	// Damage moved off the first message and into the questions list; it must
	// still be reachable, because losing it loses the whole damage flow.
	if d, found := tree.option(content.OptionDamage); !found || d.Action != content.ActionDamage {
		t.Error("the damage option is no longer in the tree")
	}

	cod, found := tree.option(content.FaqCOD)
	if !found || cod.Answer != content.FaqCODAnswer {
		t.Errorf("COD answer = %q", cod.Answer)
	}
}

// Every option must do something. One with no action, or an answer with no
// words, is a button that is tapped and says nothing.
func TestEveryDefaultOptionDoesSomething(t *testing.T) {
	for _, n := range content.DefaultTree() {
		for _, o := range n.Options {
			switch o.Action {
			case content.ActionAnswer:
				if o.Answer == "" {
					t.Errorf("%q has no answer", o.Title)
				}
			case content.ActionOpen, content.ActionURL:
				if o.Target == "" {
					t.Errorf("%q points nowhere", o.Title)
				}
			case content.ActionDamage, content.ActionTrack, content.ActionTeam,
				content.ActionFeedback:
			default:
				t.Errorf("%q has no action", o.Title)
			}
		}
	}
}

// A renamed button keeps its id, because the id is what comes back when it
// is tapped: renaming "Damage concern" must not unhook the damage flow. The
// page is what enforces this, so the rule is asserted against the validator.
func TestACodeActionKeepsItsReservedID(t *testing.T) {
	nodes := []botNodeJSON{{
		Key: content.RootNode, Kind: "buttons", Body: "Hi",
		Options: []botOptionJSON{
			{Key: content.OptionDamage, Title: "Broken item", Action: "damage", Enabled: true},
		},
	}}
	if msg := validateTree(nodes); msg != "" {
		t.Fatalf("renaming the damage button was refused: %s", msg)
	}

	nodes[0].Options[0].Key = "broken_item"
	if msg := validateTree(nodes); msg == "" {
		t.Fatal("a damage action under a made-up id was accepted; the bot would not recognise it")
	}
}

// The checks that stop the bot going silent, each stated as the thing that
// would otherwise break in a chat where nobody is watching.
func TestTheValidatorCatchesWhatMetaWouldRefuse(t *testing.T) {
	root := func(opts ...botOptionJSON) []botNodeJSON {
		return []botNodeJSON{{Key: content.RootNode, Kind: "buttons", Body: "Hi", Options: opts}}
	}
	answer := func(key, title string) botOptionJSON {
		return botOptionJSON{Key: key, Title: title, Action: "answer", Answer: "ok", Enabled: true}
	}

	cases := []struct {
		name  string
		nodes []botNodeJSON
	}{
		{"a fourth button", root(
			answer("a", "One"), answer("b", "Two"), answer("c", "Three"), answer("d", "Four"),
		)},
		{"a button label over 20 characters", root(
			answer("a", "An extremely long button label"),
		)},
		{"two options sharing an id", root(answer("a", "One"), answer("a", "Two"))},
		{"an answer with no words", root(
			botOptionJSON{Key: "a", Title: "One", Action: "answer", Enabled: true},
		)},
		{"a button that opens a message which does not exist", root(
			botOptionJSON{Key: "a", Title: "One", Action: "open", Target: "gone", Enabled: true},
		)},
		{"a link that is not a web address", root(
			botOptionJSON{Key: "a", Title: "One", Action: "url", Target: "the3dprintingstore.in", Enabled: true},
		)},
		{"a message with nothing to say", []botNodeJSON{
			{Key: content.RootNode, Kind: "buttons", Body: "  ", Options: []botOptionJSON{answer("a", "One")}},
		}},
		{"no root message at all", []botNodeJSON{
			{Key: "somewhere", Kind: "buttons", Body: "Hi", Options: []botOptionJSON{answer("a", "One")}},
		}},
	}
	for _, tc := range cases {
		if msg := validateTree(tc.nodes); msg == "" {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}

// Ten rows is a list's limit, and the eleventh has to be refused with a
// sentence that names the way out.
func TestAListTakesTenRows(t *testing.T) {
	rows := make([]botOptionJSON, 0, 11)
	for i := 0; i < 11; i++ {
		rows = append(rows, botOptionJSON{
			Key: string(rune('a' + i)), Title: "Row", Action: "answer", Answer: "ok", Enabled: true,
		})
	}
	nodes := []botNodeJSON{
		{Key: content.RootNode, Kind: "buttons", Body: "Hi", Options: []botOptionJSON{
			{Key: "other", Title: "Other", Action: "open", Target: "q", Enabled: true},
		}},
		{Key: "q", Kind: "list", Body: "Pick one", ListButton: "See questions", Options: rows},
	}
	if msg := validateTree(nodes); msg == "" {
		t.Fatal("an eleventh list row was accepted")
	}
	nodes[1].Options = rows[:10]
	if msg := validateTree(nodes); msg != "" {
		t.Fatalf("ten rows were refused: %s", msg)
	}
}
