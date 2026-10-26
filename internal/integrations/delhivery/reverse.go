package delhivery

// Creating a reverse shipment: a parcel coming BACK from a customer to the
// warehouse. The reverse half of the damage flow books one once support has
// confirmed the customer's Rs 100 arrived.
//
// THE SAME ENDPOINT AND THE SAME PAYLOAD AS A FORWARD SHIPMENT, with the legs
// swapped and ONE field changed. Delhivery manifests both through
// /api/cmu/create.json; what makes it a return is payment_mode "Pickup",
// after which the address in the shipment is read as the place to COLLECT
// FROM rather than deliver to, and the configured pickup location becomes the
// destination.
//
// THERE IS NO shipment_type ON THIS ENDPOINT. An earlier version of this file
// sent one - "Reverse", then "REVERSE" - alongside payment_mode "Prepaid",
// and every booking was refused with:
//
//	Waybill does not match master waybill pattern or wrong shipment type
//	for waybill
//
// which reads like a waybill problem and sent a whole afternoon after the
// account's service mappings and waybill series. It was this one field. If
// that error appears again, suspect payment_mode before anything else.
//
// IT IS A WRITE, and the most consequential one in this package: it sends a
// van to a customer's door and bills the shop. Everything
// CreateForwardShipment does to read the response carefully applies here
// unchanged, which is why both share createResponse and Booking rather than
// this one growing its own.
//
// VERIFY BEFORE TRUSTING. Their API answers 200 with success:false, so a
// wrong shape looks like a booking until the van does not come. The first
// real booking on an account should be checked against their dashboard.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ReverseShipment is one parcel to collect from a customer.
//
// Deliberately NOT ForwardShipment with a flag. The fields differ in meaning
// even where they share a name: From is where a van goes, To is where a van
// goes, and one struct serving both is one call site away from booking a
// collection from the warehouse to the warehouse.
type ReverseShipment struct {
	// OrderNumber is the shop's own reference, which comes back on every scan.
	// The damage flow suffixes the original order's number so a reverse leg
	// stays traceable to the parcel it is undoing.
	OrderNumber string
	// From is the CUSTOMER: whose door the van goes to.
	From Recipient
	// Description is what is in the box, for the courier's paperwork.
	Description string
	// DeclaredValue in rupees, for insurance. NOT an amount to collect: the
	// Rs 100 is settled by UPI before this is ever called, and asking a
	// courier to collect it again would charge the customer twice.
	DeclaredValue string
	// Pieces is how many boxes. One unless the product shipped split.
	Pieces int
	// Waybill is a number allocated in advance, or empty to let Delhivery
	// pick one.
	//
	// A RETURN MAY NEED ITS OWN SERIES. Left empty, Delhivery allocates from
	// whichever series the client is provisioned for - and a client with only
	// a forward service gets a forward number, which it then refuses to carry
	// a return on: "Waybill does not match master waybill pattern or wrong
	// shipment type for waybill". Supplying one is how a caller uses a
	// reverse series once the account has one.
	Waybill string
}

// CreateReverseShipment books one collection and returns its waybill.
//
// returnLocation is the warehouse's FACILITY NAME exactly as the Delhivery
// dashboard spells it - the same string a forward booking uses as its pickup
// location, because it is the same warehouse seen from the other end.
func (c *Client) CreateReverseShipment(
	ctx context.Context, returnLocation string, s ReverseShipment,
) (Booking, error) {
	if !c.Configured() {
		return Booking{}, fmt.Errorf("delhivery is not configured")
	}
	if strings.TrimSpace(returnLocation) == "" {
		return Booking{}, fmt.Errorf(
			"no Delhivery pickup location is configured; set DELHIVERY_PICKUP_LOCATION " +
				"to the warehouse's facility name exactly as the Delhivery dashboard spells it")
	}
	if err := s.validate(); err != nil {
		return Booking{}, err
	}

	payload := map[string]any{
		// Still "pickup_location", and still the warehouse. On a return this
		// names where the parcel ENDS UP; the address in the shipment below
		// is where the van collects from. The field keeping its forward name
		// is Delhivery's choice, not a mistake here.
		"pickup_location": map[string]any{"name": returnLocation},
		"shipments": []map[string]any{{
			// The customer, as the place to collect from.
			"name":    s.From.Name,
			"add":     s.From.Address,
			"city":    s.From.City,
			"state":   s.From.State,
			"country": orDefault(s.From.Country, "India"),
			"pin":     s.From.Pincode,
			"phone":   s.From.Phone,
			"order":   s.OrderNumber,
			// THE FIELD THAT MAKES IT A RETURN, and it is payment_mode - not a
			// shipment_type, which this endpoint has no such field for.
			//
			// Delhivery manifests a reverse through the SAME endpoint and the
			// SAME payload as a forward one - their reference says the URL and
			// payload are the same as forward - and this value is the change.
			// Forward uses Prepaid or COD; Pickup is what sends the van to
			// collect instead of deliver.
			//
			// NOT A CHARGE, despite living in payment_mode: it asks the customer
			// for nothing at the door. The Rs 100 was settled by UPI and
			// confirmed by a person long before this call.
			//
			// SENDING "Prepaid" HERE BOOKS NOTHING AT ALL. The account's forward
			// waybill series will not carry a collection, and the API answers
			// "Waybill does not match master waybill pattern or wrong shipment
			// type for waybill" - which reads like a waybill problem and is
			// really this one field. Verified against the live account on
			// 2026-10-26.
			"payment_mode":    "Pickup",
			"products_desc":   s.Description,
			"total_amount":    orDefault(s.DeclaredValue, "0"),
			"quantity":        max(s.Pieces, 1),
			"shipment_width":  "",
			"shipment_height": "",
			"weight":          "",
		}},
	}
	if wb := strings.TrimSpace(s.Waybill); wb != "" {
		payload["shipments"].([]map[string]any)[0]["waybill"] = wb
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Booking{}, fmt.Errorf("build the reverse shipment: %w", err)
	}

	// Form-encoded with the JSON in a `data` field, exactly as the forward
	// call - posting JSON here is accepted with a 200 and creates nothing.
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
		return Booking{}, fmt.Errorf("delhivery refused the reverse pickup (%d): %s",
			res.StatusCode, parsed.message())
	}
	return parsed.booking()
}

// validate refuses what the caller can see is missing, rather than paying a
// round trip to be told in Delhivery's vocabulary.
func (s ReverseShipment) validate() error {
	for _, f := range []struct{ name, value string }{
		{"order number", s.OrderNumber},
		{"customer name", s.From.Name},
		{"address", s.From.Address},
		{"pincode", s.From.Pincode},
		{"phone", s.From.Phone},
	} {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf(
				"this report has no %s, so no pickup can be booked for it", f.name)
		}
	}
	return nil
}
