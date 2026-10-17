package httpapi

// What the bot says, with the shop's own wording laid over the code's.
//
// AN OVERLAY, AND THAT IS THE WHOLE DESIGN. The Go constants in
// internal/whatsappbot/content stay the default; a row in whatsapp_bot_content
// replaces one of them for one brand. A brand that has never opened the page
// behaves exactly as it did before this existed, and clearing a field undoes a
// bad edit without a deploy - which is what makes shipping it safe.
//
// WHY THE STATE MACHINE DOES NOT DO THIS. bot.Step is pure: it decides, it
// does not read. So it emits SendAnswer{Key} for anything that is not a flow,
// including an id no constant knows about, and this resolves the words. That
// is what lets the shop add a tenth question with no code at all.

import (
	"context"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
)

// Kinds of row. A menu row carries a label only: the action behind a button is
// a conversation with state, not a string, so it stays in code.
const (
	botContentMenu = "menu"
	botContentFAQ  = "faq"
)

// botContent is one brand's wording, resolved once per turn.
type botContent struct {
	menu    map[string]string // option id -> label
	faq     []content.Row     // the list, in order
	answers map[string]string // row id -> what to send
}

// codeAnswers is every canned answer the code ships with, keyed the way the
// list rows are. The fallback when a brand has written nothing.
func codeAnswers() map[string]string {
	return map[string]string{
		content.FaqCOD:        content.FaqCODAnswer,
		content.FaqDelivery:   content.FaqDeliveryAnswer,
		content.FaqMakingTime: content.FaqMakingTimeAnswer,
		content.FaqChangeName: content.FaqChangeNameAnswer,
		content.FaqShipping:   content.FaqShippingAnswer,
		content.FaqReturns:    content.FaqReturnsAnswer,
		content.FaqCare:       content.FaqCareAnswer,
	}
}

// botContentFor reads one brand's overrides and folds them over the defaults.
//
// A read failure is not an error: it falls back to the code, because a bot
// that goes silent over an unreachable settings table is worse than one
// speaking last week's words.
func (s *Server) botContentFor(ctx context.Context, brand string) botContent {
	out := botContent{
		menu:    map[string]string{},
		faq:     append([]content.Row(nil), content.FaqList.Rows...),
		answers: codeAnswers(),
	}
	rows, err := s.store.Q.ListWhatsAppBotContent(ctx, brand)
	if err != nil || len(rows) == 0 {
		return out
	}

	var faq []content.Row
	custom := false
	for _, r := range rows {
		if !r.Enabled {
			// Off, not deleted. A menu label that is off falls back to the
			// code's; an FAQ row that is off leaves the list entirely.
			if r.Kind == botContentFAQ {
				custom = true
			}
			continue
		}
		switch r.Kind {
		case botContentMenu:
			if r.Title != "" {
				out.menu[r.ContentKey] = r.Title
			}
		case botContentFAQ:
			custom = true
			faq = append(faq, content.Row{
				ID: r.ContentKey, Title: r.Title, Description: r.Description,
			})
			if r.Answer != "" {
				out.answers[r.ContentKey] = r.Answer
			}
		}
	}
	// REPLACED WHOLESALE, not merged. The order of the list and which rows
	// are in it are one decision; merging a stored list into the code's would
	// resurrect a question the shop had deliberately removed.
	if custom {
		out.faq = faq
	}
	return out
}

// applyMenu relabels the buttons the shop has renamed.
func (b botContent) applyMenu(m content.Menu) content.Menu {
	out := content.Menu{Options: make([]content.Option, 0, len(m.Options))}
	for _, o := range m.Options {
		if label, ok := b.menu[o.ID]; ok && label != "" {
			o.Title = label
		}
		out.Options = append(out.Options, o)
	}
	return out
}

// applyList swaps in the shop's own questions.
func (b botContent) applyList(l content.List) content.List {
	if len(b.faq) == 0 {
		// Every row switched off. The button would open an empty sheet, so
		// the caller is told there is nothing to show.
		return content.List{Button: l.Button}
	}
	return content.List{Button: l.Button, Rows: b.faq}
}

// answerFor is what to send when a row is tapped, and whether there is one.
func (b botContent) answerFor(key string) (string, bool) {
	text, ok := b.answers[key]
	return text, ok && text != ""
}

// botContentJSON is one editable row as the dashboard sees it.
type botContentJSON struct {
	Kind        string `json:"kind"`
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Answer      string `json:"answer"`
	Position    int32  `json:"position"`
	Enabled     bool   `json:"enabled"`
	// FromCode is true for a row the shop has not overridden: the page shows
	// it so there is something to edit, and saving it writes the first row.
	FromCode bool `json:"from_code"`
	// Locked is true for a menu button whose behaviour is code. The label is
	// editable; the button cannot be removed or repointed.
	Locked bool `json:"locked"`
}

func botContentRowJSON(r gen.WhatsappBotContent) botContentJSON {
	return botContentJSON{
		Kind: r.Kind, Key: r.ContentKey, Title: r.Title, Description: r.Description,
		Answer: r.Answer, Position: r.Position, Enabled: r.Enabled,
		Locked: r.Kind == botContentMenu,
	}
}
