package httpapi

// The bot's tree, with the shop's own version laid over the code's.
//
// AN OVERLAY, AND THAT IS THE WHOLE DESIGN. content.DefaultTree() stays the
// default; a brand's rows replace it wholesale. A brand that has never
// opened the page behaves exactly as it did before this existed, and
// emptying the table undoes a bad edit without a deploy - which is what
// makes shipping it safe.
//
// WHY THE STATE MACHINE DOES NOT READ THIS. bot.Step is pure: it decides, it
// does not look things up. So it emits SendAnswer{Key} for any tap that is
// not one of the three state-changing flows, and the resolution of what that
// id means - a canned reply, another message, a link - happens here. That is
// what lets the shop add a button the code has never heard of.

import (
	"context"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
)

// botTree is one brand's resolved tree.
type botTree struct {
	nodes map[string]content.Node
	// options is every option in the tree by id, so a tap is resolved in one
	// lookup rather than by walking the nodes.
	options map[string]content.NodeOption
	order   []string // node keys, in display order
}

// root is the message any unrecognised customer message gets.
func (t botTree) root() (content.Node, bool) {
	n, ok := t.nodes[content.RootNode]
	if ok {
		return n, true
	}
	// A tree whose root was renamed or removed still has to answer. The
	// first node in order is the honest fallback - silence is not.
	for _, key := range t.order {
		return t.nodes[key], true
	}
	return content.Node{}, false
}

func (t botTree) node(key string) (content.Node, bool) {
	n, ok := t.nodes[key]
	return n, ok
}

func (t botTree) option(id string) (content.NodeOption, bool) {
	o, ok := t.options[id]
	return o, ok
}

// enabledOptions is what the customer actually sees, capped at what Meta
// will accept. A node over the cap is truncated rather than refused: the API
// prevents it on the way in, and a message that sends nine of ten rows is
// better than one Meta rejects whole.
func visibleOptions(n content.Node) []content.NodeOption {
	limit := content.MaxButtons
	if n.Kind == content.NodeList {
		limit = content.MaxRows
	}
	if len(n.Options) <= limit {
		return n.Options
	}
	return n.Options[:limit]
}

// botTreeFor reads one brand's tree, falling back to the code's.
//
// A read failure is not an error: it falls back to the defaults, because a
// bot that goes silent over an unreachable settings table is worse than one
// speaking last week's words.
func (s *Server) botTreeFor(ctx context.Context, brand string) botTree {
	nodes, err := s.store.Q.ListWhatsAppBotNodes(ctx, brand)
	if err != nil || len(nodes) == 0 {
		return treeFrom(content.DefaultTree())
	}
	options, err := s.store.Q.ListWhatsAppBotOptions(ctx, brand)
	if err != nil {
		return treeFrom(content.DefaultTree())
	}

	byNode := map[string][]content.NodeOption{}
	for _, o := range options {
		if !o.Enabled {
			// Off, not deleted - it simply does not reach the customer.
			continue
		}
		byNode[o.NodeKey] = append(byNode[o.NodeKey], content.NodeOption{
			ID: o.OptionKey, Title: o.Title, Description: o.Description,
			Action: content.ActionKind(o.Action), Answer: o.Answer,
			Target: o.Target, ButtonLabel: o.ButtonLabel,
		})
	}

	built := make([]content.Node, 0, len(nodes))
	for _, n := range nodes {
		built = append(built, content.Node{
			Key: n.NodeKey, Kind: content.NodeKind(n.Kind), Body: n.Body,
			ListButton: n.ListButton, Options: byNode[n.NodeKey],
		})
	}
	return treeFrom(built)
}

func treeFrom(nodes []content.Node) botTree {
	out := botTree{
		nodes:   make(map[string]content.Node, len(nodes)),
		options: map[string]content.NodeOption{},
		order:   make([]string, 0, len(nodes)),
	}
	for _, n := range nodes {
		out.nodes[n.Key] = n
		out.order = append(out.order, n.Key)
		for _, o := range n.Options {
			// First wins. Two options sharing an id is refused on save, so
			// this only decides what a tree written before that check does.
			if _, taken := out.options[o.ID]; !taken {
				out.options[o.ID] = o
			}
		}
	}
	return out
}

// numberedChoice maps a typed "2" to the second option of the root.
//
// Kept working now that the menu is editable: the old version mapped fixed
// positions to fixed ids, which stops being true the moment the shop
// reorders a button. Empty when the text is not a bare number in range.
func (t botTree) numberedChoice(text string) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) != 1 || trimmed[0] < '1' || trimmed[0] > '9' {
		return ""
	}
	root, ok := t.root()
	if !ok {
		return ""
	}
	options := visibleOptions(root)
	n := int(trimmed[0] - '1')
	if n >= len(options) {
		return ""
	}
	return options[n].ID
}

// --- JSON, as the dashboard edits it ----------------------------------------

type botOptionJSON struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Answer      string `json:"answer"`
	Target      string `json:"target"`
	ButtonLabel string `json:"button_label"`
	Enabled     bool   `json:"enabled"`
}

type botNodeJSON struct {
	Key        string          `json:"key"`
	Kind       string          `json:"kind"`
	Body       string          `json:"body"`
	ListButton string          `json:"list_button"`
	Options    []botOptionJSON `json:"options"`
}

func nodeJSON(n content.Node) botNodeJSON {
	out := botNodeJSON{
		Key: n.Key, Kind: string(n.Kind), Body: n.Body,
		ListButton: n.ListButton, Options: make([]botOptionJSON, 0, len(n.Options)),
	}
	if out.Kind == "" {
		out.Kind = string(content.NodeButtons)
	}
	for _, o := range n.Options {
		out.Options = append(out.Options, botOptionJSON{
			Key: o.ID, Title: o.Title, Description: o.Description,
			Action: string(o.Action), Answer: o.Answer, Target: o.Target,
			ButtonLabel: o.ButtonLabel, Enabled: true,
		})
	}
	return out
}

func (s *Server) treeJSON(ctx context.Context, brand string) ([]botNodeJSON, bool) {
	nodes, err := s.store.Q.ListWhatsAppBotNodes(ctx, brand)
	if err != nil || len(nodes) == 0 {
		out := make([]botNodeJSON, 0, 2)
		for _, n := range content.DefaultTree() {
			out = append(out, nodeJSON(n))
		}
		return out, true // from the code, never saved
	}
	options, _ := s.store.Q.ListWhatsAppBotOptions(ctx, brand)
	byNode := map[string][]botOptionJSON{}
	for _, o := range options {
		byNode[o.NodeKey] = append(byNode[o.NodeKey], botOptionJSON{
			Key: o.OptionKey, Title: o.Title, Description: o.Description,
			Action: o.Action, Answer: o.Answer, Target: o.Target,
			ButtonLabel: o.ButtonLabel, Enabled: o.Enabled,
		})
	}
	out := make([]botNodeJSON, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, botNodeJSON{
			Key: n.NodeKey, Kind: n.Kind, Body: n.Body,
			ListButton: n.ListButton, Options: orEmptyOptions(byNode[n.NodeKey]),
		})
	}
	return out, false
}

func orEmptyOptions(in []botOptionJSON) []botOptionJSON {
	if in == nil {
		return []botOptionJSON{}
	}
	return in
}

// deref is defined elsewhere in this package.
var _ = gen.WhatsappBotNode{}
