// Package content holds every piece of text a customer can see. Edit wording
// here; no other package builds customer-facing copy.
package content

// Button IDs. They come back to us verbatim when tapped.
const (
	OptionTrack           = "track"
	OptionDamage          = "damage"
	OptionMore            = "more"
	OptionExplore         = "explore"
	OptionDeliveryPayment = "delivery_payment"
	OptionTeam            = "team"
)

// Option is one reply button. WhatsApp limits its title to 20 characters.
type Option struct {
	ID    string
	Title string
}

// Menu is a message with reply buttons shown right under it. WhatsApp allows
// at most 3, which is why the fourth choice lives behind "More".
type Menu struct {
	Options []Option
}

// MainMenu is what any message gets.
var MainMenu = Menu{Options: []Option{
	{ID: OptionTrack, Title: "Where is my order"},
	{ID: OptionDamage, Title: "Damage concern"},
	{ID: OptionMore, Title: "More"},
}}

// MoreMenu is the second set of buttons, behind "More".
var MoreMenu = Menu{Options: []Option{
	{ID: OptionExplore, Title: "Explore more"},
	{ID: OptionDeliveryPayment, Title: "Delivery & payment"},
	{ID: OptionTeam, Title: "Talk to us"},
}}

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
	Greeting = "👋 Hi! Welcome to *The 3D Printing Store*.\n\nHow can we help you today?"

	MoreIntro = "What else can we help with?"

	DeliveryPaymentInfo = "🚚 *Delivery*\n" +
		"Every product is crafted after you order:\n" +
		"• Free dispatch: within 10 working days\n" +
		"• Express dispatch (₹100): within 3 working days\n" +
		"After dispatch, delivery takes 2-5 working days across India. You'll get a tracking link as soon as it ships.\n\n" +
		"💳 *Payment*\n" +
		"Everything is 100% personalised and crafted only after a prepaid order, so we don't offer Cash on Delivery. " +
		"Pay securely online at checkout.\n\n" +
		"🎁 20% OFF on orders above ₹2000!"

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
