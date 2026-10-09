package delhivery

// Why a delivery did not happen.
//
// Delhivery puts the reason in Status.Instructions as a SENTENCE, not a code
// anybody can switch on. Status.StatusCode is a code, but the codes are
// internal and undocumented - measured on this account they include X-OLL4F,
// DLYLH-105, FMPUR-101, EOD-38, ST-114 - and nothing published says which of
// them means "nobody was home". So the reason is read from the sentence.
//
// THE SENTENCE IS ALWAYS SHOWN. The bucket below is a filter, never a
// replacement for what Delhivery actually said: every row carries the raw
// instruction, so a phrase this file has not seen yet shows up as itself under
// "Other" rather than disappearing or being mislabelled.

import "strings"

// Reason is the bucket a shipment's current instruction falls into.
type Reason string

const (
	// ReasonConsigneeUnavailable is the one this page is for: Delhivery went,
	// nobody could take the parcel.
	ReasonConsigneeUnavailable Reason = "consignee_unavailable"
	// ReasonConsigneeRefused is a different problem with the same shape -
	// somebody was there and said no. Separated because the follow-up is a
	// refund conversation, not a redelivery.
	ReasonConsigneeRefused Reason = "consignee_refused"
	// ReasonAddressProblem means the parcel never got close enough to fail on
	// the doorstep.
	ReasonAddressProblem Reason = "address_problem"
	// ReasonPaymentNotReady is a COD parcel the customer could not pay for.
	ReasonPaymentNotReady Reason = "payment_not_ready"
	// ReasonRescheduled is the customer asking for another day. Not a failure.
	ReasonRescheduled Reason = "rescheduled"
	// ReasonAttemptsExhausted is the end of the road: Delhivery has tried as
	// often as it will and is about to send the parcel back.
	//
	// Found live ("Maximum attempts reached", three attempts), and separated
	// from ReasonConsigneeUnavailable deliberately - it is the SAME story one
	// step later, and it is the only row where ringing the customer no longer
	// helps on its own. Somebody has to tell the courier to go again.
	ReasonAttemptsExhausted Reason = "attempts_exhausted"
	// ReasonAudit is a parcel Delhivery has lost track of inside its own
	// network. Found live as "Package Missing in Audit" and "Package found in
	// Audit", both with zero delivery attempts - so this is not a failed
	// delivery at all, it is a parcel that never reached the door.
	//
	// Its own bucket because the follow-up is completely different: a claim
	// against the courier, not a call to the customer.
	ReasonAudit Reason = "audit"
	// ReasonCarrierDelay is Delhivery's own fault - a late vehicle, a missed
	// connection. Nothing to ring a customer about.
	ReasonCarrierDelay Reason = "carrier_delay"
	// ReasonMoving is a shipment doing what it should: in a bag, on a vehicle,
	// at a facility, out for delivery. The large majority of any account.
	ReasonMoving Reason = "moving"
	// ReasonDelivered is the happy ending, and its own bucket rather than part
	// of ReasonMoving: the page lists every delivery, and labelling an arrived
	// parcel "In transit" would be wrong on the row somebody is least likely
	// to look at twice.
	ReasonDelivered Reason = "delivered"
	// ReasonOther is an exception whose sentence this file does not recognise.
	// Shown, not hidden.
	ReasonOther Reason = "other"
)

// Label is the human name for a bucket, used as the page's filter chips.
func (r Reason) Label() string {
	switch r {
	case ReasonConsigneeUnavailable:
		return "Consignee unavailable"
	case ReasonConsigneeRefused:
		return "Refused"
	case ReasonAddressProblem:
		return "Address problem"
	case ReasonPaymentNotReady:
		return "Payment not ready"
	case ReasonRescheduled:
		return "Rescheduled"
	case ReasonAttemptsExhausted:
		return "Attempts exhausted"
	case ReasonAudit:
		return "In carrier audit"
	case ReasonCarrierDelay:
		return "Carrier delay"
	case ReasonMoving:
		return "In transit"
	case ReasonDelivered:
		return "Delivered"
	default:
		return "Other"
	}
}

// phrases maps a bucket to the instruction text that means it.
//
// Substring matches on a lower-cased instruction, not equality: Delhivery
// varies the wording around the same reason ("Consignee not available",
// "Consignee unavailable/Door locked"), and a fixed list of exact strings would
// pass the one case it was written from and silently drop the rest.
//
// Order matters - the first bucket whose phrase appears wins - so the
// deliberately narrow buckets come before the broad ones. "Consignee refused to
// accept" contains "consignee", which is why refusal is checked before
// unavailability.
var phrases = []struct {
	reason Reason
	any    []string
}{
	// Checked first. "Maximum attempts reached" is the consequence of repeated
	// consignee-unavailable attempts and would otherwise be unrecognised; the
	// audit wordings contain no other bucket's phrase but are listed early so
	// a later addition cannot capture them by accident.
	{ReasonAttemptsExhausted, []string{
		"maximum attempts reached", "max attempts", "maximum attempt",
		"all attempts exhausted", "attempts exhausted",
	}},
	{ReasonAudit, []string{
		"in audit", "audit", "shipment lost", "lost in transit", "package damaged",
		"shipment damaged", "misplaced",
	}},
	{ReasonConsigneeRefused, []string{
		"refused", "refuse to accept", "rejected by consignee", "did not accept",
	}},
	{ReasonPaymentNotReady, []string{
		"cod not ready", "cod amount not ready", "amount not ready",
		"payment not ready", "payment mode dispute", "cash not available",
	}},
	{ReasonRescheduled, []string{
		"future delivery", "delivery rescheduled", "requested by consignee for",
		"customer wants delivery on", "reattempt as per",
	}},
	{ReasonAddressProblem, []string{
		"address incorrect", "incorrect address", "incomplete address",
		"address incomplete", "wrong address", "consignee shifted",
		"out of delivery area", "pincode not serviceable", "untraceable",
		"premises closed permanently",
	}},
	{ReasonConsigneeUnavailable, []string{
		"consignee not available", "consignee unavailable", "consignee not avl",
		"customer not available", "customer unavailable", "no one available",
		"nobody available", "door locked", "house locked", "premises locked",
		"residence closed", "office closed", "factory closed", "shop closed",
		"not available at", "consignee is not available", "consignee out of station",
		"consignee travelling", "consignee not reachable", "phone not reachable",
	}},
	{ReasonCarrierDelay, []string{
		"vehicle delayed", "vehicle breakdown", "misroute", "mis-route",
		"connection missed", "weather", "strike", "bandh", "natural disaster",
		"operational delay", "delivery not attempted",
		// Found in the live scan trail, not in any documentation:
		// "On hold. Destination unable to receive" (DLYLH-126). The
		// destination hub, not the customer - so it is the courier's delay
		// even though the sentence reads like a refusal.
		"on hold", "unable to receive",
	}},
}

// movingPhrases are the instructions that mean nothing is wrong.
//
// This list exists because Delhivery's coarse Status alone does not separate a
// problem from progress. MEASURED: three live shipments on this account carry
// Status "Pending" with the instruction "Shipment Received at Facility" - a
// hand-off between hubs, not a failed delivery. Treating every "Pending" as an
// exception would have put them on this page as stuck parcels, and the person
// reading it would have rung three customers for no reason.
var movingPhrases = []string{
	"vehicle departed", "added to bag", "bag added to trip", "bag received",
	"bag removed", "trip arrived", "trip departed", "shipment received",
	"received at facility", "manifest uploaded", "pickup scheduled",
	"shipment picked up", "weight captured", "out for delivery",
	"pincode updated", "call placed to consignee", "in transit",
}

// Classify reads a shipment's current instruction and says what it means.
func Classify(s Shipment) Reason {
	instruction := strings.ToLower(strings.TrimSpace(s.Status.Instructions.String()))

	if s.Delivered() {
		return ReasonDelivered
	}
	if instruction == "" {
		// No sentence at all. Progress unless the coarse status says otherwise,
		// because an empty instruction is far more often a shipment that has
		// only just been manifested than a silent failure.
		if strings.EqualFold(s.Status.Status.String(), "Pending") {
			return ReasonOther
		}
		return ReasonMoving
	}

	for _, bucket := range phrases {
		for _, phrase := range bucket.any {
			if strings.Contains(instruction, phrase) {
				return bucket.reason
			}
		}
	}

	for _, phrase := range movingPhrases {
		if strings.Contains(instruction, phrase) {
			return ReasonMoving
		}
	}

	// An unrecognised sentence. Called an exception only when the coarse
	// status agrees something is pending, so a new benign scan type does not
	// flood the page.
	if strings.EqualFold(s.Status.Status.String(), "Pending") {
		return ReasonOther
	}
	return ReasonMoving
}

// Actionable reports whether a bucket is somebody's work.
//
// MEASURED, and this is why the method exists: of forty-nine live shipments on
// this account, SEVEN carried "Vehicle delayed - Controllable". That is
// Delhivery's own vehicle running late - nothing the shop can do, nobody to
// ring - and listing it would put seven rows of courier noise on a page whose
// job is to surface the one parcel that failed at somebody's door. It is still
// classified and still counted, so the page can offer it as a chip; it is just
// not what the page opens on.
//
// Rescheduled IS actionable, by contrast: the customer named a day, and
// somebody has to make sure the parcel is still in the country when it comes.
func (r Reason) Actionable() bool {
	switch r {
	case ReasonMoving, ReasonDelivered, ReasonCarrierDelay:
		return false
	default:
		return true
	}
}

// NeedsAttention reports whether a shipment is somebody's work right now:
// something went wrong that a person can do something about.
//
// It is what the LIST IS SORTED BY, not what the list contains - the page shows
// every delivery, and these float to the top of it.
func NeedsAttention(s Shipment) bool {
	if s.Delivered() {
		return false
	}
	return Classify(s).Actionable()
}
