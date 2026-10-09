package delhivery

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// Text is a field Delhivery types inconsistently.
//
// Measured on the live account: Consignee.Address3 comes back as "" while
// Address1 and Address2 on the SAME shipment come back as [] - an empty JSON
// array where a string belongs - and PinCode comes back as a number. A plain
// string field makes json.Decode fail on the whole batch because one address
// line was blank, which is how a working integration reports "no shipments".
type Text string

func (t *Text) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	switch {
	case trimmed == "" || trimmed == "null":
		*t = ""
		return nil
	case trimmed[0] == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		*t = Text(strings.TrimSpace(s))
		return nil
	case trimmed[0] == '[':
		// Delhivery's empty-address shape. A populated array is joined rather
		// than having its first element taken, so a two-part address line is
		// not silently halved.
		var parts []Text
		if err := json.Unmarshal(raw, &parts); err != nil {
			*t = ""
			return nil
		}
		kept := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(string(p)); s != "" {
				kept = append(kept, s)
			}
		}
		*t = Text(strings.Join(kept, ", "))
		return nil
	default:
		// A number, or anything else scalar. Rendered as text because that is
		// all this product does with it.
		*t = Text(strings.Trim(trimmed, `"`))
		return nil
	}
}

func (t Text) String() string { return string(t) }

// Stamp is one of Delhivery's timestamps.
//
// They are local Indian time with no zone offset and inconsistent fractional
// seconds ("2026-10-09T05:57:56.24", "2026-10-06T16:43:55"), and null when the
// event has not happened. time.Time with the standard decoder rejects both.
type Stamp struct{ time.Time }

// delhiveryLayouts are the shapes seen in live responses, longest first.
var delhiveryLayouts = []string{
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func (s *Stamp) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		// null, or a non-string. Left zero; IsZero() is what callers read.
		return nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, text); err == nil {
		s.Time = t
		return nil
	}
	for _, layout := range delhiveryLayouts {
		if t, err := time.ParseInLocation(layout, text, indiaTime()); err == nil {
			s.Time = t
			return nil
		}
	}
	return nil
}

// indiaTime is where Delhivery's zoneless stamps actually happened. Reading
// them as UTC puts every scan five and a half hours in the past, which on this
// page is the difference between "attempted this morning" and "attempted
// yesterday".
func indiaTime() *time.Location {
	if loc, err := time.LoadLocation("Asia/Kolkata"); err == nil {
		return loc
	}
	// Windows has no tzdata unless the binary imports time/tzdata. A fixed
	// +05:30 is exactly right for India, which has never observed DST.
	return time.FixedZone("IST", 5*60*60+30*60)
}

// Consignee is who the parcel is for. This is customer data, and it is here
// because the page exists to let somebody act on a failed delivery.
type Consignee struct {
	Name     Text `json:"Name"`
	Address1 Text `json:"Address1"`
	Address2 Text `json:"Address2"`
	Address3 Text `json:"Address3"`
	City     Text `json:"City"`
	State    Text `json:"State"`
	Country  Text `json:"Country"`
	PinCode  Text `json:"PinCode"`
	// Telephone1 and Telephone2 are Delhivery's own field names - there is no
	// "Phone" or "Mobile" on a consignee. Both come back EMPTY on this
	// account's shipments, so the order's phone number from Shopify is what
	// the page actually has to reach somebody with; these are read in case
	// that changes rather than assumed to work.
	Telephone1 Text `json:"Telephone1"`
	Telephone2 Text `json:"Telephone2"`
}

// Address is the delivery address as one line, for a table cell.
func (c Consignee) Address() string {
	parts := make([]string, 0, 6)
	for _, p := range []Text{c.Address1, c.Address2, c.Address3, c.City, c.State, c.PinCode} {
		if s := strings.TrimSpace(p.String()); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

// Phone is whichever number Delhivery holds for the consignee, if any.
func (c Consignee) Phone() string {
	if s := strings.TrimSpace(c.Telephone1.String()); s != "" {
		return s
	}
	return strings.TrimSpace(c.Telephone2.String())
}

// Status is the shipment's current state.
//
// Status is Delhivery's coarse bucket (Manifested, In Transit, Dispatched,
// Pending, Delivered, RTO, ...); StatusType is the two-letter form ("UD"
// undelivered, "DL" delivered, "RT" return); and Instructions is the SENTENCE -
// "Out for delivery", "Vehicle Departed", and, when a delivery fails, the NDR
// reason. Instructions is the field this whole page filters on.
type Status struct {
	Status         Text  `json:"Status"`
	StatusType     Text  `json:"StatusType"`
	StatusCode     Text  `json:"StatusCode"`
	StatusDateTime Stamp `json:"StatusDateTime"`
	StatusLocation Text  `json:"StatusLocation"`
	Instructions   Text  `json:"Instructions"`
	ReceivedBy     Text  `json:"RecievedBy"` // Delhivery's spelling, not ours.
}

// ScanDetail is one event in the shipment's history.
type ScanDetail struct {
	Scan            Text  `json:"Scan"`
	ScanType        Text  `json:"ScanType"`
	ScanDateTime    Stamp `json:"ScanDateTime"`
	ScannedLocation Text  `json:"ScannedLocation"`
	Instructions    Text  `json:"Instructions"`
	StatusCode      Text  `json:"StatusCode"`
}

type scanEnvelope struct {
	ScanDetail ScanDetail `json:"ScanDetail"`
}

// Shipment is one tracked parcel.
type Shipment struct {
	AWB         Text      `json:"AWB"`
	ReferenceNo Text      `json:"ReferenceNo"`
	OrderType   Text      `json:"OrderType"`
	Status      Status    `json:"Status"`
	Consignee   Consignee `json:"Consignee"`

	Origin      Text `json:"Origin"`
	Destination Text `json:"Destination"`
	SenderName  Text `json:"SenderName"`
	// PickupLocation is the warehouse the parcel left.
	PickupLocation Text `json:"PickupLocation"`

	Quantity      Text    `json:"Quantity"`
	InvoiceAmount float64 `json:"InvoiceAmount"`
	CODAmount     float64 `json:"CODAmount"`

	PickupDate           Stamp `json:"PickUpDate"`
	ExpectedDeliveryDate Stamp `json:"ExpectedDeliveryDate"`
	PromisedDeliveryDate Stamp `json:"PromisedDeliveryDate"`
	// FirstAttemptDate and DispatchCount together say how hard Delhivery has
	// already tried. A parcel on its third attempt is a different conversation
	// from one on its first.
	FirstAttemptDate Stamp `json:"FirstAttemptDate"`
	DispatchCount    int   `json:"DispatchCount"`
	DeliveryDate     Stamp `json:"DeliveryDate"`
	RTOStartedDate   Stamp `json:"RTOStartedDate"`
	ReturnedDate     Stamp `json:"ReturnedDate"`
	ReverseInTransit bool  `json:"ReverseInTransit"`

	// The hub timeline, as Delhivery names it: when the parcel reached the
	// origin hub, left for the destination city, and arrived there. A parcel
	// with no DestRecieveDate has not got to the delivery city at all, which
	// is a different problem from one that got there and failed at the door.
	OriginReceiveDate  Stamp `json:"OriginRecieveDate"`
	OutDestinationDate Stamp `json:"OutDestinationDate"`
	DestReceiveDate    Stamp `json:"DestRecieveDate"`
	// ChargedWeight is null until Delhivery weighs the parcel, which is why it
	// is a pointer: 0 and "not weighed yet" are different facts, and a billing
	// dispute turns on which one it is.
	ChargedWeight *float64 `json:"ChargedWeight"`

	Scans []scanEnvelope `json:"Scans"`
}

// History is the scan trail, in the order Delhivery returns it.
func (s Shipment) History() []ScanDetail {
	out := make([]ScanDetail, 0, len(s.Scans))
	for _, entry := range s.Scans {
		out = append(out, entry.ScanDetail)
	}
	return out
}

// Delivered reports whether the parcel reached the customer.
func (s Shipment) Delivered() bool {
	return strings.EqualFold(s.Status.StatusType.String(), "DL")
}

// Returning reports whether the parcel is on its way back to the warehouse.
//
// Past the point where ringing the customer helps, so it is counted separately
// rather than shown beside live exceptions - a returned parcel in the
// "unavailable" list is work somebody can no longer do.
func (s Shipment) Returning() bool {
	return strings.EqualFold(s.Status.StatusType.String(), "RT") ||
		s.ReverseInTransit ||
		!s.RTOStartedDate.IsZero()
}

// Attempts is how many times Delhivery has been out with this parcel.
func (s Shipment) Attempts() int {
	if s.DispatchCount > 0 {
		return s.DispatchCount
	}
	if !s.FirstAttemptDate.IsZero() {
		return 1
	}
	return 0
}

// Units is the parcel's piece count, as a number. Delhivery sends it as a
// string ("1").
func (s Shipment) Units() int {
	n, err := strconv.Atoi(strings.TrimSpace(s.Quantity.String()))
	if err != nil {
		return 0
	}
	return n
}
