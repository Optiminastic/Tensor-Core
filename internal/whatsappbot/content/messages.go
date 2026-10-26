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

	// The first question: which kind of customer this is.
	//
	// IT ASKS BEFORE IT OFFERS, at the shop's instruction. Both answers open
	// the same menu today, so the choice is a record rather than a branch -
	// it is in the conversation log, and either answer can be pointed at its
	// own message later by changing where it opens on the Bot Messages page.
	OptionReturning = "returning_customer"
	OptionNew       = "new_customer"

	// OptionFeedback hands the chat to a person, like OptionTeam, but says
	// something different when it does: somebody offering praise or a
	// complaint should not be answered with "our team will reply shortly"
	// as though they had asked a question.
	OptionFeedback = "feedback"

	// FAQ rows. One canned answer each: no state, no flow.
	FaqCOD        = "faq_cod"
	FaqDelivery   = "faq_delivery"
	FaqMakingTime = "faq_making_time"
	FaqChangeName = "faq_change_name"
	FaqShipping   = "faq_shipping"
	FaqReturns    = "faq_returns"
	FaqCare       = "faq_care"
)

// The menu used to live here as a MainMenu and a FaqList: two fixed shapes,
// three buttons and ten rows, with the behaviour of each in a switch. It is
// a tree now - see tree.go - because the shop needed to add an option and
// say what it should do. The answers below remain the DEFAULT wording for
// the options the tree ships with.

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
	Greeting = "👋 Hi! Welcome to *The 3D Printing Store*.\n\n" +
		"Have you ordered from us before?"

	// MenuIntro is the second message, shown once they have answered.
	//
	// IT DOES NOT THANK THEM FOR ANSWERING. Two messages in a row that both
	// sound like a greeting read as a bot talking to itself; this one gets
	// straight to what it can do.
	MenuIntro = "How can we help you today?"

	// FeedbackThanks is said when somebody picks Feedback, and the bot then
	// stops replying so a person can read what comes next.
	FeedbackThanks = "We would love to hear it 🙂 Please type your feedback here " +
		"and a member of our team will read it personally."

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
	DamageLinkButton = "Open"

	// DamageLinkAlready answers a second tap from somebody who already has a
	// form open.
	//
	// IT CANNOT RESEND THE LINK. Only the token's hash is stored, so the
	// address exists nowhere but in the message already sitting in their
	// chat - which is the whole reason a database dump opens nobody's form.
	DamageLinkAlready = "You already have a damage form open — scroll up in this chat and tap *Open*.\n\n" +
		"It stays open for 24 hours, and you can add more photos to it any time before then."

	// --- The reprint. ---
	//
	// Sent the moment an admin or support decides the outcome, so the customer
	// stops wondering. It carries no question and no link: the shop has taken
	// the whole thing on, and the one useful thing to say is that there is
	// nothing for them to do.
	//
	// THE THREE TICKS ARE THE POINT. "We will reprint it" leaves a customer
	// with a damaged parcel in their hall asking three questions - do I pay,
	// do I send this one back, do I need to arrange anything - and support
	// then answers them one at a time. Answering them before they are asked is
	// the difference between a message and a reply.
	//
	// It says "as soon as it is ready" rather than a date, because nothing has
	// been printed yet and a date invented here is a promise the floor did not
	// make. The tracking message when it ships is the one that carries a
	// number.
	ReprintScheduled = "We're really sorry your order arrived damaged. 🙏\n\n" +
		"We've scheduled a *reprint* of your product — our team is making it again now.\n\n" +
		"✅ Nothing to pay\n" +
		"✅ Nothing you need to do\n" +
		"✅ No need to return the damaged piece\n\n" +
		"We'll message you here with tracking as soon as your new one ships. " +
		"Thank you for your patience."

	// --- The reverse pickup. ---
	//
	// Rs 100 is the customer's money, so the message says the whole deal
	// before it asks for anything: what a reverse pickup IS in one line, what
	// they pay, what we pay, and what happens in between. A customer who only
	// learns about the second leg after paying for the first has been sold
	// something they did not agree to.

	ReversePaymentRequest = "We can arrange a *reverse pickup* — our courier collects the " +
		"damaged product from your address and brings it to our warehouse.\n\n" +
		"Here is how it works:\n" +
		"1️⃣ *₹100 from you* for the pickup from your home to our warehouse\n" +
		"2️⃣ We repair the product at our warehouse\n" +
		"3️⃣ *We pay* to send it back to your home\n\n" +
		"Please pay *₹100* by scanning this QR with any UPI app, then " +
		"*send us a screenshot of the payment here* and our team will arrange " +
		"the pickup."

	// ReversePaymentRequestNoQR is the same ask with the QR promised rather
	// than attached, for a brand whose public base URL is not set.
	//
	// IT STILL ASKS. Dropping the message because the image is missing would
	// leave a report that says the customer was asked and a customer who was
	// not - and somebody can always send the QR by hand.
	ReversePaymentRequestNoQR = "We can arrange a *reverse pickup* — our courier collects the " +
		"damaged product from your address and brings it to our warehouse.\n\n" +
		"Here is how it works:\n" +
		"1️⃣ *₹100 from you* for the pickup from your home to our warehouse\n" +
		"2️⃣ We repair the product at our warehouse\n" +
		"3️⃣ *We pay* to send it back to your home\n\n" +
		"Our team will send you the payment QR here in a moment. Once you have " +
		"paid, *send us a screenshot of the payment* and we will arrange the pickup."

	// ReversePickupBooked is sent when an admin books the courier. It gives
	// the one instruction the customer has to act on - keep it packed - and
	// does not promise a date, because the courier has not given us one.
	ReversePickupBooked = "✅ Your reverse pickup is booked.\n\n" +
		"Please keep the product packed and ready. Our courier partner will " +
		"collect it from your address, and we will message you here as soon as " +
		"it reaches our warehouse."

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

	// HandedOverTap answers a button pressed while a person has the chat.
	//
	// IT DOES NOT APOLOGISE AND DOES NOT PROMISE A TIME. It says who has it
	// and gives the one way back, because the escape is a typed word and the
	// customer has just demonstrated they are tapping, not typing.
	HandedOverTap = "Our team has this one and will reply here. 🙂\n\n" +
		"Type *MENU* if you would like the options again."

	StopAck = "You won't receive any more automated messages from us. Type *START* to turn them back on."

	ConfirmThanks = "Thank you for confirming! ✅ We'll start crafting order %s now."

	AskChangeText = "No problem. Please type the correct details for order %s exactly as you want them printed (e.g. *First name: RAHUL, Second name: PRIYA*)."

	ChangeReceived = "Thanks, we've noted your changes for order %s. Our team will confirm them with you here before we start crafting."

	AskChangeTextAgain = "Please type the corrected details as a text message."
)
