// Package content holds every piece of text a customer can see. Edit wording
// here; no other package builds customer-facing copy.
package content

// List option IDs. They come back to us verbatim when tapped.
const (
	OptionTrack   = "track"
	OptionDamage  = "damage"
	OptionExplore = "explore"
	OptionMore    = "more"

	OptionDelivery   = "faq_delivery"
	OptionPayment    = "faq_payment"
	OptionBulk       = "faq_bulk"
	OptionChange     = "faq_change"
	OptionTeam       = "faq_team"
	OptionBackToMenu = "back_to_menu"
)

// Option is one row of a WhatsApp list message. WhatsApp limits the title to
// 24 characters and the description to 72.
type Option struct {
	ID          string
	Title       string
	Description string
}

// Menu is a WhatsApp list message: Button is the label that opens the list
// (max 20 characters), Options its rows (max 10).
type Menu struct {
	Button  string
	Options []Option
}

// MainMenu is what any message gets. It is a list, not reply buttons, because
// WhatsApp allows only 3 reply buttons and the menu has 4 options.
var MainMenu = Menu{
	Button: "Choose an option",
	Options: []Option{
		{ID: OptionTrack, Title: "Where is my order", Description: "Order status and tracking"},
		{ID: OptionDamage, Title: "Damage concern", Description: "Received a damaged product?"},
		{ID: OptionExplore, Title: "Explore more", Description: "Browse our personalised gifts"},
		{ID: OptionMore, Title: "More", Description: "Delivery, payment, bulk orders and more"},
	},
}

// MoreMenu is the FAQ list behind "More".
var MoreMenu = Menu{
	Button: "See topics",
	Options: []Option{
		{ID: OptionDelivery, Title: "Delivery time", Description: "When will my order arrive?"},
		{ID: OptionPayment, Title: "Payment & COD", Description: "How to pay for your order"},
		{ID: OptionBulk, Title: "Bulk / corporate order", Description: "Gifts for teams, events and clients"},
		{ID: OptionChange, Title: "Change my order", Description: "Edit names or details"},
		{ID: OptionTeam, Title: "Talk to our team", Description: "Chat with a person"},
		{ID: OptionBackToMenu, Title: "Back to main menu", Description: "See the main options again"},
	},
}

// Confirmation template button payload prefixes: "confirm:<order gid>".
const (
	PayloadConfirm = "confirm:"
	PayloadChange  = "change:"
)

// Order tags the bot writes to Shopify. Production reads these.
const (
	TagConfirmed       = "wa-confirmed"
	TagChangeRequested = "wa-change-requested"
	TagAutoConfirmed   = "wa-auto-confirmed"
	TagUnreachable     = "wa-unreachable"
	TagDamageReported  = "wa-damage-reported"
)

// Approved WhatsApp templates (business-initiated, UTILITY category).
const (
	TemplateConfirm  = "order_personalisation_confirm"
	TemplateReminder = "order_personalisation_reminder"
	TemplateLanguage = "en"
)

const (
	Greeting = "Hi! 👋 Welcome to *The 3D Printing Store*.\n\nHow can we help you today? Tap *Choose an option* below."

	MoreIntro = "What would you like to know? Tap *See topics* below."

	DeliveryInfo = "🚚 *Delivery time*\n\n" +
		"Every product is crafted after you order:\n" +
		"• Free dispatch: within 10 working days\n" +
		"• Express dispatch (₹100): within 3 working days\n\n" +
		"After dispatch, delivery takes 2-5 working days across India. You'll get a tracking link as soon as it ships."

	PaymentInfo = "💳 *Payment*\n\n" +
		"All our products are 100% personalised and crafted only after a prepaid order, so we don't offer Cash on Delivery. " +
		"You can pay securely online at checkout.\n\n" +
		"🎁 20% OFF on orders above ₹2000!"

	BulkHandoff = "🎁 Lovely! Please share the product, quantity and the date you need it by. Our team will reply here shortly with a quote."

	ChangeHandoff = "✏️ Please type your order number and what you'd like to change. Our team will reply here shortly.\n\nNote: changes are only possible before we start crafting."

	MenuHint = "Type *MENU* any time to see the options again."

	AskOrderNumber = "Please send your order number. You can find it in your order confirmation email, e.g. *115766* or *T3DPS-115766*."

	InvalidOrderNumber = "That doesn't look like an order number. Please send just the number, e.g. *115766*."

	OrderNotFound = "We couldn't find that order. Please check the number and send it again."

	PhoneMismatch = "For your privacy we can only share details of orders placed with this WhatsApp number. Please send the order number again, or type *HUMAN* to talk to our team."

	AskDamagePhotos = "We're really sorry about that. 🙏\n\nPlease send clear photos of the damaged product *and* the packaging. When you have sent all photos, type *DONE*."

	PhotoReceived = "Got it 📸. Send more photos, or type *DONE* when finished."

	DamageNeedPhotos = "Please send at least one photo of the damaged product, then type *DONE*."

	DamageReported = "Thank you. We've recorded your damage report for order %s and our team will check the photos and get back to you shortly."

	Explore = "✨ Explore our personalised 3D printed gifts:\n\n" +
		"⭐ Best sellers: https://the3dprintingstore.in/collections/best-seller\n" +
		"💑 Dual name planks: https://the3dprintingstore.in/collections/dual-name-planks\n" +
		"💞 Soulmate combos: https://the3dprintingstore.in/collections/soulmate-combo-collection\n" +
		"💼 Desk name planks: https://the3dprintingstore.in/collections/desk-name-planks\n" +
		"🏠 Home name plates: https://the3dprintingstore.in/collections/home-name-plate\n" +
		"🐕 Pet tags: https://the3dprintingstore.in/collections/pet-tags\n" +
		"🛍️ Everything: https://the3dprintingstore.in/collections/all\n\n" +
		"🎁 20% OFF on orders above ₹2000!"

	Handoff = "Thanks! Our team will reply to you here shortly. 🙂"

	StopAck = "You won't receive any more automated messages from us. Type *START* to turn them back on."

	ConfirmThanks = "Thank you for confirming! ✅ We'll start crafting order %s now."

	AskChangeText = "No problem. Please type the correct details for order %s exactly as you want them printed (e.g. *First name: RAHUL, Second name: PRIYA*)."

	ChangeReceived = "Thanks, we've noted your changes for order %s. Our team will confirm them with you here before we start crafting."

	AskChangeTextAgain = "Please type the corrected details as a text message."
)
