package delhivery

// What makes a booking a RETURN rather than a second parcel.
//
// The forward and reverse calls go to the same endpoint and share the same
// response reading, so what is tested here is only the difference - and the
// difference is the whole risk. A reverse that loses its shipment_type books
// an ordinary outbound parcel to the customer's address: a second product
// arrives instead of the broken one leaving, the shop pays for it, and
// Delhivery reports a perfectly successful booking throughout.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// captureBooking returns a client pointed at a server that records the
// shipment payload it was sent.
func captureBooking(t *testing.T, got *map[string]any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Errorf("parse form: %v", err)
		}
		var payload struct {
			PickupLocation map[string]any   `json:"pickup_location"`
			Shipments      []map[string]any `json:"shipments"`
		}
		if err := json.Unmarshal([]byte(form.Get("data")), &payload); err != nil {
			t.Errorf("parse data: %v", err)
		}
		if len(payload.Shipments) == 1 {
			ship := payload.Shipments[0]
			ship["_pickup_location"] = payload.PickupLocation["name"]
			*got = ship
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"success":true,"packages":[{"waybill":"RVP123","status":"Success","remarks":[]}]}`))
	}))
	t.Cleanup(srv.Close)
	return New(Config{APIKey: "k", BaseURL: srv.URL})
}

func aReverse() ReverseShipment {
	return ReverseShipment{
		OrderNumber: "T3DPS-1001-RV",
		From: Recipient{
			Name: "Asha", Phone: "9999999999", Address: "12 Hill Road",
			City: "Mumbai", State: "MH", Pincode: "400050",
		},
		Description: "Damaged diya holder",
		Pieces:      1,
	}
}

func TestAReverseBookingSaysItIsAReturn(t *testing.T) {
	var sent map[string]any
	c := captureBooking(t, &sent)

	booking, err := c.CreateReverseShipment(context.Background(), "Alt Optiminastic", aReverse())
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if !booking.Accepted() {
		t.Fatalf("booking not accepted: %+v", booking)
	}

	// THE FIELD THE WHOLE THING TURNS ON. Delhivery has no shipment_type on
	// this endpoint; payment_mode "Pickup" is what makes a van collect rather
	// than deliver, and "Prepaid" here books nothing at all.
	if sent["payment_mode"] != "Pickup" {
		t.Errorf("payment_mode = %v, want \"Pickup\" - this does not book a collection",
			sent["payment_mode"])
	}
	if _, ok := sent["shipment_type"]; ok {
		t.Errorf("a shipment_type was sent (%v); this API has no such field",
			sent["shipment_type"])
	}
}

func TestAReverseCollectsFromTheCustomerAndReturnsToTheWarehouse(t *testing.T) {
	var sent map[string]any
	c := captureBooking(t, &sent)

	if _, err := c.CreateReverseShipment(
		context.Background(), "Alt Optiminastic", aReverse(),
	); err != nil {
		t.Fatalf("book: %v", err)
	}

	// The address on the shipment is the CUSTOMER's - the door the van goes
	// to. Swapping these two books a van to the warehouse to collect from
	// itself.
	if sent["name"] != "Asha" || sent["pin"] != "400050" {
		t.Errorf("the shipment names %v at %v, want the customer", sent["name"], sent["pin"])
	}
	if sent["_pickup_location"] != "Alt Optiminastic" {
		t.Errorf("returns to %v, want the warehouse", sent["_pickup_location"])
	}
}

func TestAReverseNeverAsksTheCustomerForMoneyAtTheDoor(t *testing.T) {
	var sent map[string]any
	c := captureBooking(t, &sent)

	if _, err := c.CreateReverseShipment(
		context.Background(), "Alt Optiminastic", aReverse(),
	); err != nil {
		t.Fatalf("book: %v", err)
	}

	// The Rs 100 was settled by UPI and confirmed by a person before this
	// call could happen. A COD flag here would ask for it a second time, at
	// the door, for a parcel the customer is handing over - and "Pickup",
	// despite the field's name, collects nothing from them.
	if sent["payment_mode"] == "COD" {
		t.Error("the customer would be asked for money at their own door")
	}
}

func TestAReverseWithNothingToCollectFromIsRefusedHere(t *testing.T) {
	var sent map[string]any
	c := captureBooking(t, &sent)

	// Each of these is something the caller can see is missing, and the
	// refusal names it in the shop's words rather than Delhivery's - after a
	// round trip that books nothing.
	for _, broken := range []struct {
		field  string
		mutate func(*ReverseShipment)
	}{
		{"address", func(s *ReverseShipment) { s.From.Address = "" }},
		{"pincode", func(s *ReverseShipment) { s.From.Pincode = "" }},
		{"phone", func(s *ReverseShipment) { s.From.Phone = "" }},
		{"customer name", func(s *ReverseShipment) { s.From.Name = "" }},
	} {
		s := aReverse()
		broken.mutate(&s)
		_, err := c.CreateReverseShipment(context.Background(), "Alt Optiminastic", s)
		if err == nil {
			t.Errorf("a shipment with no %s was sent to Delhivery", broken.field)
			continue
		}
		if !strings.Contains(err.Error(), broken.field) {
			t.Errorf("refusal for a missing %s reads %q", broken.field, err)
		}
	}
}

func TestAReverseCarriesAWaybillOnlyWhenItIsGivenOne(t *testing.T) {
	var sent map[string]any
	c := captureBooking(t, &sent)

	// Blank means "Delhivery, pick one" - and the field must be ABSENT, not
	// empty. An empty waybill is read as a waybill, and refused.
	if _, err := c.CreateReverseShipment(
		context.Background(), "Alt Optiminastic", aReverse(),
	); err != nil {
		t.Fatalf("book: %v", err)
	}
	if _, ok := sent["waybill"]; ok {
		t.Errorf("an unasked-for waybill field was sent: %v", sent["waybill"])
	}

	// Given one, it travels. This is how a caller uses a reverse series once
	// the account has one - see reverse.go on why this account does not.
	s := aReverse()
	s.Waybill = "  RVP999  "
	if _, err := c.CreateReverseShipment(
		context.Background(), "Alt Optiminastic", s,
	); err != nil {
		t.Fatalf("book with a waybill: %v", err)
	}
	if sent["waybill"] != "RVP999" {
		t.Errorf("waybill = %v, want the trimmed RVP999", sent["waybill"])
	}
}

func TestAReverseWithNoWarehouseNamesTheSetting(t *testing.T) {
	var sent map[string]any
	c := captureBooking(t, &sent)

	_, err := c.CreateReverseShipment(context.Background(), "  ", aReverse())
	if err == nil {
		t.Fatal("a pickup was booked with no warehouse to return to")
	}
	// Delhivery's own error for this is generic; this one says which setting.
	if !strings.Contains(err.Error(), "DELHIVERY_PICKUP_LOCATION") {
		t.Errorf("refusal reads %q, which does not name the setting to fix", err)
	}
}
