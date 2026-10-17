// Package content holds every piece of text a customer can see. Edit wording
// here; no other package builds customer-facing copy.
package content

// Button and list-row IDs. They come back to us verbatim when tapped, and a
// list row is indistinguishable from a button by the time the bot sees it.
const (
	OptionTrack  = "track"
	OptionDamage = "damage"
	// OptionOther opens the FAQ list. Named for what the customer sees rather
	// than for the list behind it.
	OptionOther   = "other"
	OptionExplore = "explore"
	OptionTeam    = "team"

	// FAQ rows. One canned answer each: no state, no flow.
	FaqCOD        = "faq_cod"
	FaqDelivery   = "faq_delivery"
	FaqMakingTime = "faq_making_time"
	FaqChangeName = "faq_change_name"
	FaqShipping   = "faq_shipping"
	FaqReturns    = "faq_returns"
	FaqCare       = "faq_care"
)

// Option is one reply button. WhatsApp limits its title to 20 characters.
type Option struct {
	ID    string
	Title string
}

// Menu is a message with reply buttons shown right under it. WhatsApp allows
// at most 3, which is why everything else lives behind "Other" as a list.
type Menu struct {
	Options []Option
}

// Row is one line of the FAQ list. WhatsApp caps Title at 24 characters and
// Description at 72.
type Row struct {
	ID          string
	Title       string
	Description string
}

// List is a message with a tappable list behind one button: ten rows against
// a button message's three.
type List struct {
	Button string
	Rows   []Row
}

// MainMenu is what any message gets.
var MainMenu = Menu{Options: []Option{
	{ID: OptionDamage, Title: "Damage concern"},
	{ID: OptionExplore, Title: "Explore products"},
	{ID: OptionOther, Title: "Other"},
}}

// FaqList is everything behind "Other".
//
// A LIST, NOT BUTTONS. Nine answers do not fit in three buttons, and burying
// the rest behind a second "More" is a menu nobody reaches the bottom of.
// "Where is my order" sits here rather than on the main menu because the two
// things a customer opens a chat about are a problem and a purchase.
var FaqList = List{
	Button: "See questions",
	Rows: []Row{
		{ID: FaqDelivery, Title: "Delivery time", Description: "When will my order reach me?"},
		{ID: FaqCOD, Title: "Cash on delivery", Description: "Do you accept COD?"},
		{ID: FaqMakingTime, Title: "How long to make", Description: "Everything is made after you order"},
		{ID: OptionTrack, Title: "Where is my order", Description: "Track an order that has shipped"},
		{ID: FaqChangeName, Title: "Change my spelling", Description: "Correct a name after ordering"},
		{ID: FaqShipping, Title: "Where you deliver", Description: "Shipping across India"},
		{ID: FaqReturns, Title: "Returns & refunds", Description: "How it works for personalised items"},
		{ID: FaqCare, Title: "Caring for it", Description: "Keeping your plank looking new"},
		{ID: OptionTeam, Title: "Talk to our team", Description: "A person will reply here"},
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
	Greeting = "👋 Hi! Welcome to *The 3D Printing Store*.\n\nHow can we help you today?"

	FaqIntro = "Sure — here is what we are asked most. 👇\n\nTap *See questions* and pick one."

	// --- FAQ answers. One tap, one answer, no flow. ---

	FaqCODAnswer = "💳 *Cash on Delivery*\n\n" +
		"We don't offer COD. Every product is personalised and made only after the order is placed, " +
		"so it cannot be resold if a parcel is refused.\n\n" +
		"Payment online at checkout is quick and secure — UPI, cards, net banking and wallets all work."

	FaqDeliveryAnswer = "🚚 *Delivery time*\n\n" +
		"You will have it in *5 to 10 working days*.\n\n" +
		"That covers making your piece and the courier bringing it to you. " +
		"We will send you a tracking link the moment it ships."

	FaqMakingTimeAnswer = "🛠️ *How long it takes to make*\n\n" +
		"Nothing is sitting on a shelf — your piece is printed after you order, which is why the names come out exactly as you typed them.\n\n" +
		"Printing and finishing take 2 to 4 working days, then the courier does the rest."

	FaqChangeNameAnswer = "✏️ *Changing a name or spelling*\n\n" +
		"Yes, if we have not started printing. Send us your order ID here and tell us the correct spelling, " +
		"and we will fix it before it goes to the printer.\n\n" +
		"Once a piece is printed the names cannot be changed — the name is the product."

	FaqShippingAnswer = "📦 *Where we deliver*\n\n" +
		"All over India. Shipping is free on every order.\n\n" +
		"We do not ship outside India at the moment."

	FaqReturnsAnswer = "↩️ *Returns and refunds*\n\n" +
		"Personalised pieces carry somebody's name, so they cannot be returned or resold — " +
		"which is why we ask you to check the spelling before we print.\n\n" +
		"If your parcel arrives *damaged*, that is on us: type *MENU* and tap *Damage concern*, " +
		"and we will send you a link to upload the photos."

	FaqCareAnswer = "🧼 *Looking after it*\n\n" +
		"• Wipe with a soft dry cloth — no water, no cleaning sprays\n" +
		"• Keep it out of direct sun and away from heat; it is plastic and will warp\n" +
		"• Lift it by the base rather than by the letters\n\n" +
		"Treated kindly it will look new for years."

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

	// DamageLinkBody introduces the form. The address itself is not in it -
	// it travels as the button beside it, which is what a thumb goes for.
	DamageLinkBody = "We're really sorry about that. 🙏\n\n" +
		"Please open the link below and send us:\n" +
		"1️⃣ Your *order ID*\n" +
		"2️⃣ *Photos or a video* of the damaged product and its packaging\n\n" +
		"You can upload several files at once. Our team will check them and reply here."

	// DamageLinkButton is what the button says. WhatsApp caps it at 20
	// characters, and the shop asked for this word.
	DamageLinkButton = "LINK"

	// DamageLinkAlready answers a second tap from somebody who already has a
	// form open.
	//
	// IT CANNOT RESEND THE LINK. Only the token's hash is stored, so the
	// address exists nowhere but in the message already sitting in their
	// chat - which is the whole reason a database dump opens nobody's form.
	DamageLinkAlready = "You already have a damage form open — scroll up in this chat and tap *LINK*.\n\n" +
		"It stays open for 24 hours, and you can add more photos to it any time before then."

	// DamageLinkFailed is said when the form could not be minted. It names
	// the way through rather than apologising twice: the customer still has
	// a damaged parcel and still needs somebody to look at it.
	DamageLinkFailed = "Sorry — we couldn't open the damage form just now. " +
		"Please send us your order ID and photos of the damage here and our team will take it from there."

	// Explore leads with the shop itself: somebody tapping "Explore products"
	// wants the store, not a reading list.
	Explore = "✨ Have a look around *The 3D Printing Store*:\n" +
		"👉 https://the3dprintingstore.in\n\n" +
		"Or go straight to a collection:\n\n" +
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
