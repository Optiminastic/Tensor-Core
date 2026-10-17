package httpapi

// Sending a node, and doing what a tapped option says.
//
// This is where the tree becomes WhatsApp messages. The state machine has
// already decided WHICH message - the root, or the option somebody tapped -
// and everything below is the shape of it: three buttons or ten rows, a
// canned reply, a link button, or another node.
//
// A TAP THAT RESOLVES TO NOTHING SHOWS THE MENU. An option can be deleted
// between the message being sent and the customer getting round to tapping
// it, and a chat where a button does nothing at all is worse than one that
// starts again.

import (
	"context"
	"time"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/whatsapp"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
)

// sendNode sends one message with its options under it.
func (t *botTurn) sendNode(ctx context.Context, q *gen.Queries, n content.Node) error {
	options := visibleOptions(n)
	if len(options) == 0 {
		// Every option switched off. A message with an empty list opens a
		// blank sheet in WhatsApp, so send the words alone.
		res, err := t.wa.SendText(ctx, t.waID, n.Body)
		return t.recordSend(ctx, q, res, err, "text", n.Body)
	}

	if n.Kind == content.NodeList {
		rows := make([]whatsapp.ListRow, 0, len(options))
		for _, o := range options {
			rows = append(rows, whatsapp.ListRow{ID: o.ID, Title: o.Title, Description: o.Description})
		}
		button := n.ListButton
		if button == "" {
			button = "See options"
		}
		res, err := t.wa.SendList(ctx, t.waID, n.Body, button, rows)
		return t.recordSend(ctx, q, res, err, "list", n.Body)
	}

	buttons := make([]whatsapp.Button, 0, len(options))
	for _, o := range options {
		buttons = append(buttons, whatsapp.Button{ID: o.ID, Title: o.Title})
	}
	res, err := t.wa.SendButtons(ctx, t.waID, n.Body, buttons)
	return t.recordSend(ctx, q, res, err, "menu", n.Body)
}

// answer performs whatever the tapped option says to do.
func (t *botTurn) answer(ctx context.Context, q *gen.Queries, key string) error {
	tree := t.menu(ctx)
	option, ok := tree.option(key)
	if !ok {
		return t.sendRoot(ctx, q)
	}

	switch option.Action {
	case content.ActionOpen:
		next, found := tree.node(option.Target)
		if !found {
			// The page refuses to save this, so it means a tree written
			// before that check, or a node deleted under a live message.
			return t.sendRoot(ctx, q)
		}
		return t.sendNode(ctx, q, next)

	case content.ActionURL:
		return t.sendLink(ctx, q, option)

	case content.ActionAnswer:
		if option.Answer == "" {
			return t.sendRoot(ctx, q)
		}
		body := option.Answer + "\n\n" + content.MenuHint
		res, err := t.wa.SendText(ctx, t.waID, body)
		return t.recordSend(ctx, q, res, err, "text", body)
	}

	// A code action that reached here rather than the state machine, which
	// means its id no longer matches. The menu is the honest answer.
	return t.sendRoot(ctx, q)
}

// sendLink sends a message with a button that opens a web address.
//
// A BUTTON, NOT A BARE URL. A link in the body is a line of grey text the
// customer has to notice and trust; a button is what their thumb is looking
// for. It falls back to text with the address in it when Meta refuses the
// interactive form - outside the 24-hour window, most likely - because an
// awkward link still beats no answer.
func (t *botTurn) sendLink(ctx context.Context, q *gen.Queries, o content.NodeOption) error {
	body := o.Answer
	if body == "" {
		body = o.Title
	}
	label := o.ButtonLabel
	if label == "" {
		label = "Open"
	}

	res, err := t.wa.SendCTAURL(ctx, t.waID, body, label, o.Target)
	if err == nil {
		return t.recordSend(ctx, q, res, nil, "cta_url", body)
	}
	t.logger.Warn("link button refused, falling back to text",
		"brand", t.brand, "option", o.ID, "error", err)
	fallback := body + "\n\n" + o.Target
	res, err = t.wa.SendText(ctx, t.waID, fallback)
	return t.recordSend(ctx, q, res, err, "text", fallback)
}

func (t *botTurn) sendRoot(ctx context.Context, q *gen.Queries) error {
	root, ok := t.menu(ctx).root()
	if !ok {
		return nil
	}
	return t.sendNode(ctx, q, root)
}

// recordSendAt exists only so this file's sends are timestamped the same way
// the rest of the turn's are.
var _ = time.Now
