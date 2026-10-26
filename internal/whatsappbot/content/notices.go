package content

// The messages the bot sends OUTSIDE the menu tree, and which of them the shop
// may rewrite from the Bot Messages page.
//
// THE TREE IS NOT THIS. A node's body and its options are a conversation with
// a shape - tap this, open that - and they already have an editor of their
// own. These are the one-off lines: the damage form's covering message, the
// Rs 100 ask, "your pickup is booked", "type MENU to see the options". They
// have no options under them and no shape, so a list of named boxes is the
// honest way to show them.
//
// THE CONSTANTS STAY THE DEFAULT. A brand with no stored row sends exactly
// what it sent before this existed, and deleting a row restores that wording
// without a deploy - the same overlay rule the tree already uses, and what
// makes this safe to ship against a live bot.
//
// PLACEHOLDERS ARE A CONTRACT. Three of these are format strings: the order
// number is substituted into them. A rewrite that drops the %s would send a
// customer "We'll start crafting order now", and one that adds a second would
// send "%!s(MISSING)". Placeholders is how many the text must contain, and
// the API refuses a save that does not match.

import "strings"

// Notice is one editable message.
type Notice struct {
	// Key is what the database stores and the API speaks. Stable: renaming
	// one orphans whatever the shop has written.
	Key string
	// Label is the heading on the page, in the shop's words rather than the
	// code's - "Damage form sent", not "DamageLinkBody".
	Label string
	// Group gathers related messages under one heading on the page.
	Group string
	// Help says WHEN it is sent, which is the thing somebody editing needs
	// and cannot see from the text itself.
	Help string
	// Default is the wording shipped in the binary.
	Default string
	// Placeholders is how many %s the text must contain - 0 for most.
	Placeholders int
	// Limit is the most characters WhatsApp will carry for this message.
	// A button's caption is far shorter than a body, and finding that out
	// from Meta's refusal is finding out too late.
	Limit int
}

// Message groups, in the order they appear on the page.
const (
	GroupDamage       = "Damage and returns"
	GroupConversation = "Conversation"
	GroupOrders       = "Order confirmations"
)

// Meta's own limits for the shapes these messages take.
const (
	bodyLimit   = 1024
	buttonLimit = 20
)

// Notices is every message the shop may rewrite, in page order.
//
// DELIBERATELY NOT EVERY CONSTANT IN THIS FILE. The FAQ answers and the menu
// greeting belong to the tree and are edited there; the WhatsApp template
// names are registered at Meta and a box here would be lying about what it
// changes. What is listed is what the bot composes itself.
func Notices() []Notice {
	return []Notice{
		// --- Damage and returns: the flow the shop runs most often. ---
		{
			Key: "damage_link_body", Label: "Damage form sent", Group: GroupDamage,
			Help:    "Sent with the button that opens the damage form, when somebody taps Damage concern.",
			Default: DamageLinkBody, Limit: bodyLimit,
		},
		{
			Key: "damage_link_button", Label: "Damage form button", Group: GroupDamage,
			Help:    "The caption on that button. WhatsApp allows 20 characters.",
			Default: DamageLinkButton, Limit: buttonLimit,
		},
		{
			Key: "damage_link_already", Label: "Damage form already sent", Group: GroupDamage,
			Help:    "Sent when somebody taps Damage concern again while they still have an open form.",
			Default: DamageLinkAlready, Limit: bodyLimit,
		},
		{
			Key: "damage_link_failed", Label: "Damage form could not open", Group: GroupDamage,
			Help:    "Sent when the form could not be created, asking them to send the photos here instead.",
			Default: DamageLinkFailed, Limit: bodyLimit,
		},
		{
			Key: "reprint_scheduled", Label: "Re-print chosen", Group: GroupDamage,
			Help:    "Sent when support decides to make the product again at our cost.",
			Default: ReprintScheduled, Limit: bodyLimit,
		},
		{
			Key: "reverse_payment_request", Label: "Reverse pickup: the ₹100 ask", Group: GroupDamage,
			Help: "Sent when support chooses a reverse pickup. It travels as the caption on the " +
				"payment QR, so the price and the code arrive together.",
			Default: ReversePaymentRequest, Limit: bodyLimit,
		},
		{
			Key: "reverse_payment_request_no_qr", Label: "Reverse pickup: ask without the QR",
			Group: GroupDamage,
			Help: "The same ask, sent as plain text when the QR image cannot be served. " +
				"Rarely seen, but it must still make sense on its own.",
			Default: ReversePaymentRequestNoQR, Limit: bodyLimit,
		},
		{
			Key: "reverse_pickup_booked", Label: "Reverse pickup booked", Group: GroupDamage,
			Help:    "Sent once the courier is booked. Its one instruction is to keep the product packed.",
			Default: ReversePickupBooked, Limit: bodyLimit,
		},

		// --- Conversation: the lines the bot falls back on. ---
		{
			Key: "menu_hint", Label: "How to reopen the menu", Group: GroupConversation,
			Help:    "Added under a canned answer, so somebody who has wandered off the menu can get back.",
			Default: MenuHint, Limit: bodyLimit,
		},
		{
			Key: "handoff", Label: "Handed to a person", Group: GroupConversation,
			Help:    "Sent when somebody asks to talk to the team, and the bot stops replying.",
			Default: Handoff, Limit: bodyLimit,
		},
		{
			Key: "feedback_thanks", Label: "Feedback invited", Group: GroupConversation,
			Help: "Sent when somebody taps Feedback. The bot then stops replying, so a " +
				"person reads whatever they say next.",
			Default: FeedbackThanks, Limit: bodyLimit,
		},
		{
			Key: "handed_over_tap", Label: "Tapped while a person has the chat",
			Group: GroupConversation,
			Help: "Sent when somebody taps a button after the chat was handed to a person. " +
				"Typing still gets silence, so the bot does not talk over your team.",
			Default: HandedOverTap, Limit: bodyLimit,
		},
		{
			Key: "stop_ack", Label: "Opted out", Group: GroupConversation,
			Help:    "Sent when somebody replies STOP.",
			Default: StopAck, Limit: bodyLimit,
		},
		{
			Key: "ask_order_number", Label: "Asking for an order number", Group: GroupConversation,
			Help:    "Sent when the bot needs an order number to look one up.",
			Default: AskOrderNumber, Limit: bodyLimit,
		},
		{
			Key: "invalid_order_number", Label: "That is not an order number", Group: GroupConversation,
			Help:    "Sent when what they typed could not be an order number at all.",
			Default: InvalidOrderNumber, Limit: bodyLimit,
		},
		{
			Key: "order_not_found", Label: "No such order", Group: GroupConversation,
			Help:    "Sent when the number looks right but matches no order.",
			Default: OrderNotFound, Limit: bodyLimit,
		},
		{
			Key: "phone_mismatch", Label: "Order belongs to another number", Group: GroupConversation,
			Help: "Sent when the order exists but was placed from a different phone number. " +
				"Deliberately does not confirm anything about that order.",
			Default: PhoneMismatch, Limit: bodyLimit,
		},

		// --- Order confirmations: these carry the order number. ---
		{
			Key: "confirm_thanks", Label: "Order confirmed", Group: GroupOrders,
			Help:         "Sent when a customer confirms their personalisation is correct.",
			Default:      ConfirmThanks,
			Placeholders: 1, Limit: bodyLimit,
		},
		{
			Key: "ask_change_text", Label: "Asking for the correct spelling", Group: GroupOrders,
			Help:         "Sent when a customer says the personalisation is wrong.",
			Default:      AskChangeText,
			Placeholders: 1, Limit: bodyLimit,
		},
		{
			Key: "ask_change_text_again", Label: "Asking again for the spelling", Group: GroupOrders,
			Help:    "Sent when what they sent back could not be used.",
			Default: AskChangeTextAgain, Limit: bodyLimit,
		},
		{
			Key: "change_received", Label: "Correction noted", Group: GroupOrders,
			Help:         "Sent once the new spelling is stored.",
			Default:      ChangeReceived,
			Placeholders: 1, Limit: bodyLimit,
		},
	}
}

// NoticeByKey finds one, so the API can refuse a key nobody defined rather
// than storing a row the bot will never read.
func NoticeByKey(key string) (Notice, bool) {
	for _, n := range Notices() {
		if n.Key == key {
			return n, true
		}
	}
	return Notice{}, false
}

// CountPlaceholders is how many %s the text would substitute.
//
// %% IS NOT ONE. A customer message with a literal percent in it - "20% off"
// written as "20%%" - must not count as a placeholder, or a perfectly good
// rewrite is refused for a reason nobody can see.
func CountPlaceholders(text string) int {
	n := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '%' || i+1 >= len(text) {
			continue
		}
		switch text[i+1] {
		case '%':
			i++ // an escaped percent, and not a placeholder
		case 's', 'v', 'd', 'q':
			n++
			i++
		}
	}
	return n
}

// ValidateNotice refuses a rewrite the bot could not send.
//
// EVERY REFUSAL IS IN THE SHOP'S WORDS, because it is read by whoever is
// typing - not by a developer. "That message needs the order number in it"
// can be acted on; "placeholder count mismatch" cannot.
func ValidateNotice(n Notice, text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "This message cannot be empty. Clear it back to the original instead."
	}
	if len([]rune(trimmed)) > n.Limit {
		return "That is too long for WhatsApp, which allows " +
			itoa(n.Limit) + " characters here."
	}
	if got := CountPlaceholders(trimmed); got != n.Placeholders {
		switch {
		case n.Placeholders == 0:
			return "This message does not take an order number, so it cannot contain %s."
		case got == 0:
			return "This message must contain %s once - that is where the order number goes."
		default:
			return "This message must contain %s exactly once - that is where the order " +
				"number goes, and it appears " + itoa(got) + " times."
		}
	}
	return ""
}

// itoa keeps strconv out of a file that otherwise needs nothing.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
