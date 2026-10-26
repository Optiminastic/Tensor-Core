package damage

// The steps a damaged order passes through, and which of them are still to
// come.
//
// A reprint is four acts against three systems - book the courier, tell the
// customer, re-queue the print, watch it dispatch - and any one can fail while
// the others succeed. The board needs to say which; a single "in progress"
// cannot.
//
// WHAT IS REMAINING is as much the point as what is done. A timeline showing
// only completed steps tells an operator a report is "fine" right up until
// they notice it has been fine for four days. Plan() gives the whole sequence
// so the drawer can draw the rest of it greyed out.

// Stage is one step. Named rather than numbered: a reprint and a reverse pass
// through different steps, and a number would imply one sequence.
type Stage string

const (
	// StageReported - the customer sent the form. Every report starts here.
	StageReported Stage = "reported"
	// StageDecided - somebody chose reprint or reverse.
	StageDecided Stage = "decided"

	// --- the reprint route ---

	// StageCourierBooked - Delhivery accepted the forward shipment.
	//
	// BEFORE the customer is told, deliberately: a promise to send a
	// replacement is made only once there is a courier to send it with.
	StageCourierBooked Stage = "courier_booked"
	// StageJobQueued - the order's production job went back in the queue.
	StageJobQueued Stage = "job_queued"
	// StagePrinting - a printer picked it up.
	StagePrinting Stage = "printing"

	// --- the reverse route ---

	// StagePaymentClaimed - they sent something that looks like proof.
	StagePaymentClaimed Stage = "payment_claimed"
	// StagePaymentConfirmed - a person checked it.
	StagePaymentConfirmed Stage = "payment_confirmed"
	// StagePickupBooked - Delhivery accepted the reverse pickup.
	StagePickupBooked Stage = "pickup_booked"

	// --- the closed route ---

	// StageClosed - support settled it in conversation and wrote down what
	// they did.
	StageClosed Stage = "closed"

	// --- both routes ---

	// StageCustomerTold - the customer was messaged about the decision.
	StageCustomerTold Stage = "customer_told"
	// StageDispatched - the parcel is on its way.
	StageDispatched Stage = "dispatched"
)

// State is how a step went.
type State string

const (
	// StateDone - it happened.
	StateDone State = "done"
	// StateFailed - it was attempted and did not work. The detail says why,
	// and the step stays in the timeline even after a retry succeeds.
	StateFailed State = "failed"
	// StateSkipped - it does not apply to this route. Recorded rather than
	// left absent: a reverse books no forward courier, and a timeline that
	// simply omitted the step would read as one still waiting to happen.
	StateSkipped State = "skipped"
)

// Step is one entry in a plan: what it is and what it is called on screen.
type Step struct {
	Stage Stage
	// Label is what an operator reads. Written for somebody glancing at a
	// drawer, not for a log: "Courier booked", not "COURIER_BOOKED".
	Label string
}

// reprintPlan is the whole sequence for a reprint, in the order the shop asked
// for it.
//
// THE COURIER COMES BEFORE THE MESSAGE. Telling a customer their replacement
// is on the way and then failing to book a courier leaves the shop having
// promised something it has not arranged. Booking first means the message is
// only ever sent about something real.
//
// The job is re-queued AFTER both, because it is the slowest step and the one
// that does not need the customer waiting on it: the plank has to be printed,
// batched and packed whatever the courier said.
var reprintPlan = []Step{
	{StageReported, "Damage reported"},
	{StageDecided, "Re-print chosen"},
	{StageCourierBooked, "Courier booked"},
	{StageCustomerTold, "Customer told"},
	{StageJobQueued, "Back in the print queue"},
	{StagePrinting, "Printing"},
	{StageDispatched, "Dispatched"},
}

// reversePlan is the Rs 100 reverse pickup, which is a negotiation rather than
// a sequence of our own acts: two of its five steps wait on the customer.
//
// There is no "customer agreed" step any more. The ask and the QR are one
// message, so there is nothing to agree TO before paying - a customer who
// does not want a reverse pickup simply does not pay, and the report sits at
// "payment requested" where an operator can see it waiting.
var reversePlan = []Step{
	{StageReported, "Damage reported"},
	{StageDecided, "Reverse pickup chosen"},
	{StageCustomerTold, "Payment requested"},
	{StagePaymentClaimed, "Payment sent"},
	{StagePaymentConfirmed, "Payment confirmed"},
	{StagePickupBooked, "Pickup booked"},
}

// closedPlan is support settling it in conversation. Three steps, and the
// third IS the note: there is no courier and nothing to make, so what was
// done is the only record the report leaves.
var closedPlan = []Step{
	{StageReported, "Damage reported"},
	{StageDecided, "Closed by support"},
	{StageClosed, "Sorted with the customer"},
}

// undecidedPlan is all a report has before anybody chooses.
var undecidedPlan = []Step{
	{StageReported, "Damage reported"},
	{StageDecided, "Waiting on a decision"},
}

// Plan is every step this report should pass through, done or not.
//
// Keyed on the DECISION rather than on how far it has got, so the drawer can
// draw the remaining steps greyed out from the moment one is chosen - which is
// the whole reason an operator can tell a stalled report from a healthy one.
func Plan(kind Kind) []Step {
	switch kind {
	case KindReprint:
		return reprintPlan
	case KindReverse:
		return reversePlan
	case KindClosed:
		return closedPlan
	default:
		return undecidedPlan
	}
}

// Furthest is the last stage in the plan that has actually been reached.
//
// Read off the plan rather than off the newest row, because rows arrive in the
// order things happened and a FAILED late step must not make the report look
// further along than it is. A failure is not progress.
func Furthest(kind Kind, reached map[Stage]State) Stage {
	last := StageReported
	for _, step := range Plan(kind) {
		if reached[step.Stage] == StateDone {
			last = step.Stage
		}
	}
	return last
}

// Blocked reports the first step that was attempted and failed and has not
// since succeeded - the thing stopping this report, if anything is.
//
// Returns the EARLIEST failure rather than the latest: a courier that would
// not take the shipment is why the customer was never told, and naming the
// second failure would send somebody to fix a symptom.
func Blocked(kind Kind, reached map[Stage]State) (Step, bool) {
	for _, step := range Plan(kind) {
		if reached[step.Stage] == StateFailed {
			return step, true
		}
	}
	return Step{}, false
}
