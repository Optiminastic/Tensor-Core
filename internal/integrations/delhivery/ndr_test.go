package delhivery

import (
	"encoding/json"
	"testing"
)

// shipment builds the one thing Classify reads.
func shipment(coarse, statusType, instruction string) Shipment {
	return Shipment{Status: Status{
		Status:       Text(coarse),
		StatusType:   Text(statusType),
		Instructions: Text(instruction),
	}}
}

// The sentences below are the NDR reasons Delhivery writes into
// Status.Instructions. The page's whole correctness rests on this mapping, so
// each is asserted rather than assumed - including the wordings that differ
// only slightly from one another, which is where a substring match goes wrong.
func TestClassifyReadsTheCarriersSentence(t *testing.T) {
	cases := []struct {
		instruction string
		want        Reason
	}{
		// The reason this page exists.
		{"Consignee not available", ReasonConsigneeUnavailable},
		{"Consignee unavailable", ReasonConsigneeUnavailable},
		{"CONSIGNEE NOT AVAILABLE", ReasonConsigneeUnavailable},
		{"Consignee unavailable/Door locked", ReasonConsigneeUnavailable},
		{"Residence closed", ReasonConsigneeUnavailable},
		{"Office/Factory closed", ReasonConsigneeUnavailable},
		{"Consignee out of station", ReasonConsigneeUnavailable},
		{"Consignee not reachable", ReasonConsigneeUnavailable},

		// Somebody WAS there and said no. A different conversation, so it must
		// not land in the bucket above even though it contains "consignee".
		{"Consignee refused to accept", ReasonConsigneeRefused},
		{"Shipment rejected by consignee", ReasonConsigneeRefused},

		{"COD amount not ready", ReasonPaymentNotReady},
		{"Payment mode dispute", ReasonPaymentNotReady},

		{"Address incorrect/Incomplete", ReasonAddressProblem},
		{"Consignee shifted", ReasonAddressProblem},
		{"Out of delivery area", ReasonAddressProblem},

		{"Future delivery requested by customer", ReasonRescheduled},

		{"Vehicle delayed - Controllable", ReasonCarrierDelay},
		{"Misroute", ReasonCarrierDelay},

		// Progress, not a problem.
		{"Vehicle Departed", ReasonMoving},
		{"Out for delivery", ReasonMoving},
		{"Bag Received at Facility", ReasonMoving},
		{"Shipment picked up", ReasonMoving},
		{"Manifest uploaded", ReasonMoving},
		{"Call placed to consignee", ReasonMoving},
	}

	for _, tc := range cases {
		got := Classify(shipment("Pending", "UD", tc.instruction))
		if got != tc.want {
			t.Errorf("%q classified as %q, want %q", tc.instruction, got, tc.want)
		}
	}
}

// MEASURED on the live account: three shipments carry the coarse status
// "Pending" with the instruction "Shipment Received at Facility" - a hand-off
// between hubs. Treating "Pending" as the NDR signal would have put them on the
// page as failed deliveries and sent somebody to ring three customers for no
// reason.
func TestPendingAtAFacilityIsNotAnException(t *testing.T) {
	s := shipment("Pending", "UD", "Shipment Received at Facility")
	if got := Classify(s); got != ReasonMoving {
		t.Errorf("classified as %q, want %q", got, ReasonMoving)
	}
	if NeedsAttention(s) {
		t.Error("a facility hand-off must not be listed as an exception")
	}
}

// A wording this file has not met must still reach the page, under "Other",
// with its text intact. The alternative - calling it progress - is how a real
// failure goes unnoticed.
func TestUnknownPendingReasonIsShownNotDropped(t *testing.T) {
	s := shipment("Pending", "UD", "Entry restricted in society till 6 PM")
	if got := Classify(s); got != ReasonOther {
		t.Errorf("classified as %q, want %q", got, ReasonOther)
	}
	if !NeedsAttention(s) {
		t.Error("an unexplained pending shipment must be listed")
	}
}

// The same unknown wording on a shipment that is NOT pending is progress. A new
// benign scan type must not flood the page.
func TestUnknownInstructionWhileMovingIsNotAnException(t *testing.T) {
	s := shipment("In Transit", "UD", "Linehaul connection scanned")
	if NeedsAttention(s) {
		t.Error("an unrecognised in-transit scan must not be listed")
	}
}

func TestDeliveredIsNeverAnException(t *testing.T) {
	s := shipment("Delivered", "DL", "Delivered to consignee")
	if got := Classify(s); got != ReasonDelivered {
		t.Errorf("classified as %q, want %q", got, ReasonDelivered)
	}
	if NeedsAttention(s) {
		t.Error("a delivered parcel is nobody's work")
	}
	// Even if the last instruction still reads like a failure, which happens
	// when a parcel is delivered on a re-attempt.
	late := shipment("Delivered", "DL", "Consignee not available")
	if got := Classify(late); got != ReasonDelivered {
		t.Errorf("a delivered parcel classified as %q on an old NDR reason", got)
	}
}

// Every shipment on the live account must come out as progress. If the
// classifier ever calls one of these an exception, the page will show
// forty-nine stuck parcels on a day when nothing is stuck.
func TestLiveShipmentsAreAllMoving(t *testing.T) {
	var body struct {
		ShipmentData []struct {
			Shipment Shipment `json:"Shipment"`
		} `json:"ShipmentData"`
	}
	if err := json.Unmarshal(liveFixture(t), &body); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	for _, entry := range body.ShipmentData {
		s := entry.Shipment
		if NeedsAttention(s) {
			t.Errorf("%s (%s / %s / %q) was called an exception",
				s.AWB, s.Status.Status, s.Status.StatusType, s.Status.Instructions)
		}
	}
}

func TestReturningIsDetectedThreeWays(t *testing.T) {
	byType := Shipment{Status: Status{StatusType: "RT"}}
	if !byType.Returning() {
		t.Error("StatusType RT must count as returning")
	}
	byFlag := Shipment{ReverseInTransit: true}
	if !byFlag.Returning() {
		t.Error("ReverseInTransit must count as returning")
	}
	var byDate Shipment
	if err := json.Unmarshal([]byte(`{"RTOStartedDate":"2026-10-01T10:00:00"}`), &byDate); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !byDate.Returning() {
		t.Error("an RTO start date must count as returning")
	}
}

func TestAttemptsPrefersDispatchCount(t *testing.T) {
	if got := (Shipment{DispatchCount: 3}).Attempts(); got != 3 {
		t.Errorf("attempts %d, want 3", got)
	}
	var once Shipment
	if err := json.Unmarshal([]byte(`{"FirstAttemptDate":"2026-10-01T10:00:00"}`), &once); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := once.Attempts(); got != 1 {
		t.Errorf("attempts %d, want 1 (a first attempt with no dispatch count)", got)
	}
	if got := (Shipment{}).Attempts(); got != 0 {
		t.Errorf("attempts %d, want 0 (never been out)", got)
	}
}

// A courier delay is classified and countable but is not somebody's work. Seven
// of forty-nine live shipments carried one; see Reason.Actionable.
func TestCarrierDelayIsCountedButNotListed(t *testing.T) {
	s := shipment("In Transit", "UD", "Vehicle delayed - Controllable")
	if got := Classify(s); got != ReasonCarrierDelay {
		t.Fatalf("classified as %q, want %q", got, ReasonCarrierDelay)
	}
	if NeedsAttention(s) {
		t.Error("a late courier vehicle must not be listed as the shop's work")
	}
	if ReasonCarrierDelay.Label() == "" {
		t.Error("the bucket still needs a label - the page offers it as a chip")
	}
}

func TestActionableBuckets(t *testing.T) {
	for _, r := range []Reason{
		ReasonConsigneeUnavailable, ReasonConsigneeRefused,
		ReasonAddressProblem, ReasonPaymentNotReady, ReasonRescheduled, ReasonOther,
	} {
		if !r.Actionable() {
			t.Errorf("%q should be actionable", r)
		}
	}
	for _, r := range []Reason{ReasonMoving, ReasonDelivered, ReasonCarrierDelay} {
		if r.Actionable() {
			t.Errorf("%q should not be actionable", r)
		}
	}
}

// Found in the live scan trail rather than in documentation. The sentence reads
// like the consignee refusing, but DLYLH-126 is the destination HUB being
// unable to take the parcel - the courier's problem, not the customer's.
func TestDestinationHoldIsACarrierDelay(t *testing.T) {
	s := shipment("In Transit", "UD", "On hold. Destination unable to receive")
	if got := Classify(s); got != ReasonCarrierDelay {
		t.Errorf("classified as %q, want %q", got, ReasonCarrierDelay)
	}
}

// "Unexpected scan" (X-UNEX) appears in the live trail against both "In
// Transit" and "Pending". As a CURRENT status with nothing else to go on it is
// genuinely worth a look, so it is listed rather than assumed benign.
func TestUnexpectedScanWhilePendingIsListed(t *testing.T) {
	if !NeedsAttention(shipment("Pending", "UD", "Unexpected scan")) {
		t.Error("an unexpected scan on a pending parcel should be listed")
	}
	if NeedsAttention(shipment("In Transit", "UD", "Unexpected scan")) {
		t.Error("an unexpected scan on a moving parcel should not be listed")
	}
}

// All four wordings below are from the LIVE account, not from documentation.
// Three of them were unrecognised until they turned up there, which is the
// argument for keeping "Other" visible rather than treating an unknown sentence
// as progress.
func TestLiveNDRWordings(t *testing.T) {
	for _, tc := range []struct {
		instruction string
		want        Reason
	}{
		// The exact string Delhivery uses - title case, two words.
		{"Consignee Unavailable", ReasonConsigneeUnavailable},
		// The same story one step later: the courier has given up.
		{"Maximum attempts reached", ReasonAttemptsExhausted},
		// A parcel lost inside the courier's own network, with zero delivery
		// attempts. Not a failed delivery; a claim.
		{"Package Missing in Audit", ReasonAudit},
		{"Package found in Audit", ReasonAudit},
	} {
		got := Classify(shipment("Pending", "UD", tc.instruction))
		if got != tc.want {
			t.Errorf("%q classified as %q, want %q", tc.instruction, got, tc.want)
		}
		if !got.Actionable() {
			t.Errorf("%q must be somebody's work", tc.instruction)
		}
		if got.Label() == "" {
			t.Errorf("%q has no label for the filter chip", tc.instruction)
		}
	}
}
