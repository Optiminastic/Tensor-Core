package delhivery

// Creating a forward shipment: a parcel going OUT from the warehouse to a
// customer. The reprint half of the damage flow books one before the customer
// is told anything, so that a promise to send a replacement is only ever made
// once there is a courier to send it with.
//
// THIS IS THE FIRST WRITE CALL IN THIS PACKAGE. Everything else here reads -
// tracking a waybill cannot change anything, and a wrong answer costs a wrong
// row on a page. This one makes a parcel exist in somebody else's system, and
// a wrong answer costs a van at a door. The difference shows in how carefully
// the response is read: Delhivery answers 200 with success:false, and answers
// success:true while refusing an individual package inside it, so neither the
// status code nor the top-level flag is enough on its own.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// createPath is Delhivery's manifestation endpoint.
const createPath = "/api/cmu/create.json"

// createTimeout is longer than the tracking one. Manifestation writes to their
// side and is routinely slower than a lookup; a timeout here is ambiguous in
// the worst way - the shipment may or may not exist - so it is set to outlast
// a slow accept rather than to fail fast.
const createTimeout = 60 * time.Second

// Recipient is where a parcel we are SENDING should go.
//
// Not Consignee, which is this package's name for who Delhivery says a parcel
// was for on a tracking response - that one is shaped by their reply and wraps
// every field in Text. These are two directions of the same idea and keeping
// them apart stops a response type being half-filled to make a request.
type Recipient struct {
	Name    string
	Phone   string
	Address string
	City    string
	State   string
	Pincode string
	Country string
}

// ForwardShipment is one parcel to create.
type ForwardShipment struct {
	// OrderNumber is the shop's own reference, which comes back on every scan
	// and is how a tracking row is matched to an order.
	OrderNumber string
	To          Recipient
	// Description is what is in the box, for the courier's paperwork.
	Description string
	// DeclaredValue in rupees. A REPLACEMENT IS NOT A SALE: the customer has
	// already paid, so this is the value for insurance and customs rather than
	// an amount to collect. Collecting on a reprint would charge somebody
	// twice for a parcel that arrived broken.
	DeclaredValue string
	// Pieces is how many boxes. One unless the product ships split.
	Pieces int
}

// Booking is what Delhivery gave back for one shipment.
type Booking struct {
	Waybill string
	// Refused carries their wording when a package was not accepted. Empty on
	// a booking that went through.
	Refused string
}

// Accepted reports whether this is a booking that exists at Delhivery's end.
func (b Booking) Accepted() bool {
	return strings.TrimSpace(b.Waybill) != "" && strings.TrimSpace(b.Refused) == ""
}

// CreateForwardShipment books one outbound parcel and returns its waybill.
//
// ONE SHIPMENT PER CALL, though the API takes an array. A batch that half
// succeeds would need the caller to unpick which parcel got which waybill
// against which of its own records, and the damage flow books one at a time
// anyway - the simpler shape cannot be got wrong at the call site.
//
// pickupLocation is the warehouse's FACILITY NAME exactly as Delhivery spells
// it. They match it as a string: a typo is a refusal at their end, at the
// moment somebody is waiting for a courier.
func (c *Client) CreateForwardShipment(
	ctx context.Context, pickupLocation string, s ForwardShipment,
) (Booking, error) {
	if !c.Configured() {
		return Booking{}, fmt.Errorf("delhivery is not configured")
	}
	if strings.TrimSpace(pickupLocation) == "" {
		// Refused here rather than sent. Delhivery's own error for a missing
		// pickup location is generic, and this one names the setting.
		return Booking{}, fmt.Errorf(
			"no Delhivery pickup location is configured; set DELHIVERY_PICKUP_LOCATION " +
				"to the warehouse's facility name exactly as the Delhivery dashboard spells it")
	}
	if err := s.validate(); err != nil {
		return Booking{}, err
	}

	payload := map[string]any{
		"pickup_location": map[string]any{"name": pickupLocation},
		"shipments": []map[string]any{{
			"name":    s.To.Name,
			"add":     s.To.Address,
			"city":    s.To.City,
			"state":   s.To.State,
			"country": orDefault(s.To.Country, "India"),
			"pin":     s.To.Pincode,
			"phone":   s.To.Phone,
			"order":   s.OrderNumber,
			// PREPAID, never COD. See DeclaredValue: the customer has already
			// paid for this product once.
			"payment_mode":    "Prepaid",
			"products_desc":   s.Description,
			"total_amount":    orDefault(s.DeclaredValue, "0"),
			"quantity":        max(s.Pieces, 1),
			"shipment_width":  "",
			"shipment_height": "",
			"weight":          "",
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Booking{}, fmt.Errorf("build the shipment: %w", err)
	}

	// Form-encoded with the JSON in a `data` field, which is what this
	// endpoint takes - NOT a JSON body. Posting JSON here is accepted with a
	// 200 and creates nothing.
	form := url.Values{}
	form.Set("format", "json")
	form.Set("data", string(body))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.BaseURL+createPath, strings.NewReader(form.Encode()))
	if err != nil {
		return Booking{}, err
	}
	req.Header.Set("Authorization", "Token "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: createTimeout}
	res, err := client.Do(req)
	if err != nil {
		return Booking{}, fmt.Errorf("reach delhivery: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	var parsed createResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return Booking{}, fmt.Errorf("delhivery answered %d with something unreadable: %w",
			res.StatusCode, err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return Booking{}, fmt.Errorf("delhivery refused the shipment (%d): %s",
			res.StatusCode, parsed.message())
	}
	return parsed.booking()
}

func (s ForwardShipment) validate() error {
	// Checked here rather than left to Delhivery, because their refusal for a
	// missing field names the field in their vocabulary and arrives after a
	// round trip. Each of these is something the caller can see is absent.
	for _, f := range []struct{ name, value string }{
		{"order number", s.OrderNumber},
		{"customer name", s.To.Name},
		{"address", s.To.Address},
		{"pincode", s.To.Pincode},
		{"phone", s.To.Phone},
	} {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("this order has no %s, so no courier can be booked for it", f.name)
		}
	}
	return nil
}

// createResponse is Delhivery's answer, which is shaped to report per-package
// outcomes inside an overall one.
type createResponse struct {
	Success  bool   `json:"success"`
	Error    any    `json:"error"`
	RMK      string `json:"rmk"`
	Packages []struct {
		Waybill string `json:"waybill"`
		Status  string `json:"status"`
		// Remarks is an array of strings on a refusal and empty on an accept.
		Remarks []string `json:"remarks"`
	} `json:"packages"`
}

// booking reads one package out of the response.
//
// EVERY LAYER IS CHECKED. Delhivery answers 200 with success:false, and
// answers success:true while refusing an individual package inside it - so a
// caller that trusts the status code books nothing and tells the customer it
// did, which is the exact failure the reprint flow is ordered to avoid.
func (r createResponse) booking() (Booking, error) {
	if len(r.Packages) == 0 {
		if r.Success {
			return Booking{}, fmt.Errorf(
				"delhivery accepted the request but returned no waybill: %s", r.message())
		}
		return Booking{}, fmt.Errorf("delhivery refused the shipment: %s", r.message())
	}
	p := r.Packages[0]
	remark := strings.TrimSpace(strings.Join(p.Remarks, "; "))

	// A waybill with a remark is still a refusal: they echo the requested
	// number back while explaining why they would not take it.
	if strings.TrimSpace(p.Waybill) == "" || !strings.EqualFold(p.Status, "Success") {
		detail := remark
		if detail == "" {
			detail = strings.TrimSpace(p.Status)
		}
		if detail == "" {
			detail = r.message()
		}
		return Booking{Refused: detail}, fmt.Errorf("delhivery refused the shipment: %s", detail)
	}
	return Booking{Waybill: strings.TrimSpace(p.Waybill)}, nil
}

// message is the most useful wording in a response, for an operator.
func (r createResponse) message() string {
	if s := strings.TrimSpace(r.RMK); s != "" {
		return s
	}
	switch e := r.Error.(type) {
	case string:
		if s := strings.TrimSpace(e); s != "" {
			return s
		}
	case []any:
		parts := make([]string, 0, len(e))
		for _, item := range e {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				parts = append(parts, strings.TrimSpace(s))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	}
	return "no reason given"
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
