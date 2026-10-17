package content

// The bot as a tree of messages, rather than a menu with code behind it.
//
// WHAT CHANGED AND WHY. The first version had three fixed buttons whose
// behaviour was a switch in the state machine, and one list of questions
// behind "Other". The shop could rename a button but not add one, and could
// not say what a new button should DO - every behaviour was a code change.
//
// Now a node is a message with options under it, and each option carries an
// action. "Explore products opens the shop's website" and "Other opens the
// questions list" are rows in a table, not cases in a switch.
//
// THREE ACTIONS STAY IN CODE, and that is deliberate rather than unfinished:
// tracking an order, reporting damage and asking for a person all change the
// CONVERSATION'S STATE, and state belongs to bot.Step, which is pure and
// cannot read a table. So those three keep reserved ids the machine knows by
// name. The shop can rename them, move them, put them on any node or leave
// them out - it just cannot invent a fourth kind of them.

// ActionKind is what happens when an option is tapped.
type ActionKind string

const (
	// ActionAnswer sends a canned message back. Wholly data.
	ActionAnswer ActionKind = "answer"
	// ActionOpen sends another node - this is what makes it a tree.
	ActionOpen ActionKind = "open"
	// ActionURL sends a message with a button that opens a web address.
	ActionURL ActionKind = "url"

	// The three the state machine owns. See the file comment.
	ActionDamage ActionKind = "damage"
	ActionTrack  ActionKind = "track"
	ActionTeam   ActionKind = "team"
)

// CodeAction reports whether an action is one the state machine performs,
// and so one whose option id is fixed.
func CodeAction(a ActionKind) bool {
	switch a {
	case ActionDamage, ActionTrack, ActionTeam:
		return true
	}
	return false
}

// CodeActionID is the id the state machine listens for. Empty for a data
// action, whose id the shop chooses.
func CodeActionID(a ActionKind) string {
	switch a {
	case ActionDamage:
		return OptionDamage
	case ActionTrack:
		return OptionTrack
	case ActionTeam:
		return OptionTeam
	}
	return ""
}

// NodeKind is how a node's options reach the customer.
//
// Meta's limits, and the whole reason both exist: a message carries at most
// THREE reply buttons, and a list carries ten rows behind one button. A
// fourth button is not a thing WhatsApp can send, so a node that needs more
// options has to become a list.
type NodeKind string

const (
	NodeButtons NodeKind = "buttons"
	NodeList    NodeKind = "list"
)

// MaxButtons and MaxRows are Meta's, restated where the tree is defined.
const (
	MaxButtons = 3
	MaxRows    = 10
)

// NodeOption is one button or one list row, and what it does.
type NodeOption struct {
	// ID comes back from WhatsApp verbatim when tapped. Fixed for a code
	// action; the shop's own for anything else.
	ID    string
	Title string
	// Description is the grey second line, on list rows only.
	Description string
	Action      ActionKind
	// Answer is the message sent for ActionAnswer.
	Answer string
	// Target is the node key for ActionOpen, or the address for ActionURL.
	Target string
	// ButtonLabel is what the link button says for ActionURL. WhatsApp caps
	// it at 20 characters.
	ButtonLabel string
}

// Node is one message the bot can send, with its options under it.
type Node struct {
	Key  string
	Kind NodeKind
	Body string
	// ListButton is the word on the button that opens a list, e.g. "See
	// questions". Unused by a buttons node.
	ListButton string
	Options    []NodeOption
}

// RootNode is the message any unrecognised message gets.
const (
	RootNode      = "main"
	QuestionsNode = "questions"
)

// DefaultTree is what a brand that has never opened the page behaves as.
//
// The same overlay rule as before: these are the defaults, a stored tree
// replaces them, and clearing the stored one brings these back without a
// deploy.
func DefaultTree() []Node {
	return []Node{
		{
			Key:  RootNode,
			Kind: NodeButtons,
			Body: Greeting,
			Options: []NodeOption{
				{ID: OptionDamage, Title: "Damage concern", Action: ActionDamage},
				{
					ID: OptionExplore, Title: "Explore products", Action: ActionURL,
					Target: "https://the3dprintingstore.in/", ButtonLabel: "Visit store",
					Answer: Explore,
				},
				{ID: OptionOther, Title: "Other", Action: ActionOpen, Target: QuestionsNode},
			},
		},
		{
			Key:        QuestionsNode,
			Kind:       NodeList,
			Body:       FaqIntro,
			ListButton: "See questions",
			Options: []NodeOption{
				{ID: FaqDelivery, Title: "Delivery time", Description: "When will my order reach me?",
					Action: ActionAnswer, Answer: FaqDeliveryAnswer},
				{ID: FaqCOD, Title: "Cash on delivery", Description: "Do you accept COD?",
					Action: ActionAnswer, Answer: FaqCODAnswer},
				{ID: FaqMakingTime, Title: "How long to make", Description: "Everything is made after you order",
					Action: ActionAnswer, Answer: FaqMakingTimeAnswer},
				{ID: OptionTrack, Title: "Where is my order", Description: "Track an order that has shipped",
					Action: ActionTrack},
				{ID: FaqChangeName, Title: "Change my spelling", Description: "Correct a name after ordering",
					Action: ActionAnswer, Answer: FaqChangeNameAnswer},
				{ID: FaqShipping, Title: "Where you deliver", Description: "Shipping across India",
					Action: ActionAnswer, Answer: FaqShippingAnswer},
				{ID: FaqReturns, Title: "Returns & refunds", Description: "How it works for personalised items",
					Action: ActionAnswer, Answer: FaqReturnsAnswer},
				{ID: FaqCare, Title: "Caring for it", Description: "Keeping your plank looking new",
					Action: ActionAnswer, Answer: FaqCareAnswer},
				{ID: OptionTeam, Title: "Talk to our team", Description: "A person will reply here",
					Action: ActionTeam},
			},
		},
	}
}
