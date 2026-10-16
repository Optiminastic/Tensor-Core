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
	StateIdle              State = "idle"
	StateAwaitTrackOrder   State = "await_track_order"
	StateAwaitDamageOrder  State = "await_damage_order"
	StateAwaitDamagePhotos State = "await_damage_photos"
	StateAwaitChangeText   State = "await_change_text"
	StateHuman             State = "human"
)

// Context is per-flow scratch data, persisted as JSON with the conversation.
type Context struct {
	OrderID   string `json:"order_id,omitempty"`
	OrderName string `json:"order_name,omitempty"`
	Photos    int    `json:"photos,omitempty"`
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
type Purpose string

const (
	PurposeTrack  Purpose = "track"
	PurposeDamage Purpose = "damage"
)

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

// SendMenu sends Text with a list menu the customer picks from.
type SendMenu struct {
	Text string
	Menu content.Menu
}

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

func (SendText) isAction()           {}
func (SendMenu) isAction()           {}
func (LookupOrder) isAction()        {}
func (TagOrder) isAction()           {}
func (AppendNote) isAction()         {}
func (RecordConfirmation) isAction() {}
