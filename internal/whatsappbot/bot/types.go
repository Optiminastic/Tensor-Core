// Package bot is the conversation state machine. It is pure: Step takes the
// current conversation and one event and returns the next conversation and the
// side effects to perform. Anything that needs I/O (an order lookup) is returned
// as an action whose result is fed back in as a new event.
package bot

import (
	"time"

	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/order"
)

type State string

const (
	StateIdle            State = "idle"
	StateAwaitTrackOrder State = "await_track_order"
	StateAwaitChangeText State = "await_change_text"
	StateHuman           State = "human"
)

// Context is per-flow scratch data, persisted as JSON with the conversation.
type Context struct {
	OrderID   string `json:"order_id,omitempty"`
	OrderName string `json:"order_name,omitempty"`
	Misses    int    `json:"misses,omitempty"`
}

type Conversation struct {
	WaID           string
	State          State
	Ctx            Context
	LastCustomerAt *time.Time
	LastStaffAt    *time.Time
	HumanSince     *time.Time
	OptedOut       bool
}

// ConfirmationStatus is the lifecycle of a personalisation confirmation.
type ConfirmationStatus string

const (
	ConfirmationPending         ConfirmationStatus = "pending"
	ConfirmationSent            ConfirmationStatus = "sent"
	ConfirmationReminded        ConfirmationStatus = "reminded"
	ConfirmationConfirmed       ConfirmationStatus = "confirmed"
	ConfirmationChangeRequested ConfirmationStatus = "change_requested"
	ConfirmationAutoConfirmed   ConfirmationStatus = "auto_confirmed"
	ConfirmationUnreachable     ConfirmationStatus = "unreachable"
	// ConfirmationNotNeeded marks an order with nothing personalised to confirm.
	ConfirmationNotNeeded ConfirmationStatus = "not_needed"
	// ConfirmationSkipped marks an order whose customer opted out of messages.
	ConfirmationSkipped ConfirmationStatus = "skipped"
)

// Purpose says why an order is being looked up.
//
// One value, and it stays an enum rather than collapsing into the action:
// LookupResult has to say which flow it answers, because a customer who moved
// on mid-lookup must not be dragged back into the one they left.
type Purpose string

const PurposeTrack Purpose = "track"

type Outcome int

const (
	OrderFound Outcome = iota
	OrderNotFound
	OrderPhoneMismatch
)

// Event is something that happened in a conversation.
type Event interface{ isEvent() }

// TextIn is a typed customer message.
type TextIn struct{ Text string }

// ButtonIn is a tapped reply button; ID is the button's id.
type ButtonIn struct{ ID string }

// MediaIn is a customer photo, video, document, audio or sticker.
//
// No flow collects attachments any more - damage photographs are uploaded to
// a form - but an attachment arriving out of the blue still has to be
// answered, and the menu is that answer.
type MediaIn struct{ Kind string }

// StaffEcho is a message staff sent from the WhatsApp Business app.
type StaffEcho struct{}

// ConfirmationReply is a tap on the confirmation template's Correct/Change
// button, already resolved to its order by the caller.
type ConfirmationReply struct {
	OrderID   string
	OrderName string
	Confirmed bool
}

// LookupResult answers a LookupOrder action.
type LookupResult struct {
	Purpose Purpose
	Outcome Outcome
	Order   order.Order
}

func (TextIn) isEvent()            {}
func (ButtonIn) isEvent()          {}
func (MediaIn) isEvent()           {}
func (StaffEcho) isEvent()         {}
func (ConfirmationReply) isEvent() {}
func (LookupResult) isEvent()      {}

// Action is a side effect for the caller to perform, in order.
type Action interface{ isAction() }

type SendText struct{ Text string }

// SendMenu sends Text with up to three reply buttons under it.
type SendMenu struct {
	Text string
	Menu content.Menu
}

// SendList sends Text with a tappable list behind one button - ten rows
// where SendMenu has three. A row comes back as an ordinary button id, so
// nothing downstream has to tell them apart.
type SendList struct {
	Text string
	List content.List
}

// SendAnswer is a canned reply identified by key rather than by text.
//
// THE WORDS ARE NOT HERE ON PURPOSE. A shop can rewrite an answer, and add
// questions this package has never heard of, from the dashboard - so the
// machine says WHICH answer and the caller looks up what it currently says.
// An unknown key is not an error here either: the caller falls back to the
// menu, which is also what happens to a question somebody has deleted.
type SendAnswer struct{ Key string }

type LookupOrder struct {
	Purpose Purpose
	Number  string
}

type TagOrder struct {
	OrderID string
	Tag     string
}

type AppendNote struct {
	OrderID string
	Note    string
}

type RecordConfirmation struct {
	OrderID    string
	Status     ConfirmationStatus
	ChangeText string
}

// SendDamageLink asks the caller to mint a damage form and send its address.
//
// NO FIELDS, and that is the point: minting a token, writing the row and
// composing the message are all I/O, and this package is pure. The machine
// says only that this customer should be given a link.
type SendDamageLink struct{}

func (SendText) isAction()           {}
func (SendMenu) isAction()           {}
func (SendList) isAction()           {}
func (SendAnswer) isAction()         {}
func (LookupOrder) isAction()        {}
func (TagOrder) isAction()           {}
func (AppendNote) isAction()         {}
func (RecordConfirmation) isAction() {}
func (SendDamageLink) isAction()     {}
