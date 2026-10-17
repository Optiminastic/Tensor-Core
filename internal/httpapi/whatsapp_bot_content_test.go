package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
)

// Every question the code ships with must have words to answer it, or a flow
// that handles it. A row with neither opens, is tapped, and says nothing.
func TestEveryShippedQuestionHasAnAnswer(t *testing.T) {
	answers := codeAnswers()
	// The two rows that start a conversation rather than printing a reply.
	flows := map[string]bool{content.OptionTrack: true, content.OptionTeam: true}

	for _, row := range content.FaqList.Rows {
		if flows[row.ID] {
			continue
		}
		if answers[row.ID] == "" {
			t.Errorf("the question %q (%s) has no answer", row.Title, row.ID)
		}
	}
}

// The overlay must leave a brand that has written nothing exactly as it was.
func TestNoOverridesMeansTheCodeIsUnchanged(t *testing.T) {
	empty := botContent{menu: map[string]string{}, answers: codeAnswers()}
	if got := empty.applyMenu(content.MainMenu); len(got.Options) != len(content.MainMenu.Options) {
		t.Fatalf("menu changed shape: %+v", got)
	}
	for i, o := range empty.applyMenu(content.MainMenu).Options {
		if o.Title != content.MainMenu.Options[i].Title {
			t.Errorf("button %d relabelled to %q with no override", i, o.Title)
		}
	}
	if text, ok := empty.answerFor(content.FaqCOD); !ok || text != content.FaqCODAnswer {
		t.Errorf("COD answer = %q", text)
	}
}

// A relabelled button keeps its id, because the id is what comes back when it
// is tapped: renaming "Damage concern" must not unhook the damage flow.
func TestRelabellingKeepsTheAction(t *testing.T) {
	b := botContent{menu: map[string]string{content.OptionDamage: "Broken item"}}
	got := b.applyMenu(content.MainMenu)
	for _, o := range got.Options {
		if o.ID == content.OptionDamage && o.Title != "Broken item" {
			t.Errorf("label not applied: %q", o.Title)
		}
	}
	if len(got.Options) != len(content.MainMenu.Options) {
		t.Errorf("relabelling changed the button count")
	}
}
