package delhivery

// Reading Delhivery's answer to a booking.
//
// This is the test that matters most in this package. The reprint flow books a
// courier and tells the customer their replacement is on the way ONLY if the
// booking was accepted - so every way Delhivery can say no has to be read as
// no. They have several, and two of them look like yes:
//
//   - HTTP 200 with success:false
//   - success:true with the individual package refused inside it
//
// A parser that trusts the status code promises a customer a parcel that was
// never booked.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func bookingServer(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The endpoint takes form-encoded data with the JSON in a `data`
		// field. Posting JSON is accepted with a 200 and creates nothing, so
		// the shape is asserted here rather than discovered in production.
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
			t.Errorf("Content-Type = %q, want form-encoded", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.PostForm.Get("data") == "" {
			t.Error("no data field; Delhivery would create nothing")
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Token ") {
			t.Errorf("Authorization = %q, want a Token header", auth)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(Config{APIKey: "k", BaseURL: srv.URL})
}

func aShipment() ForwardShipment {
	return ForwardShipment{
		OrderNumber: "T3DPS-115766",
		Description: "Dual Name Plank",
		To: Recipient{
			Name: "Manohar Rao", Phone: "9799931864",
			Address: "12 Kalher Village", City: "Bhiwandi", State: "MH", Pincode: "421302",
		},
	}
}

func TestABookingReturnsItsWaybill(t *testing.T) {
	c := bookingServer(t, 200,
		`{"success":true,"packages":[{"waybill":"59906610025130","status":"Success","remarks":[]}]}`)

	got, err := c.CreateForwardShipment(context.Background(), "Alt Optiminastic", aShipment())
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if got.Waybill != "59906610025130" {
		t.Errorf("waybill = %q", got.Waybill)
	}
	if !got.Accepted() {
		t.Error("a booking with a waybill and no remark was not treated as accepted")
	}
}

// 200 with success:false. The customer must NOT be told.
func TestA200WithSuccessFalseIsARefusal(t *testing.T) {
	c := bookingServer(t, 200,
		`{"success":false,"rmk":"ClientWarehouse matching query does not exist.","packages":[]}`)

	got, err := c.CreateForwardShipment(context.Background(), "Wrong Name", aShipment())
	if err == nil {
		t.Fatal("a 200 with success:false was read as a booking")
	}
	if got.Accepted() {
		t.Error("a refused request reported itself accepted")
	}
	// The operator has to be able to act on it: the reason names the setting.
	if !strings.Contains(err.Error(), "ClientWarehouse") {
		t.Errorf("error %q does not carry Delhivery's reason", err)
	}
}

// success:true with the package refused inside it. The nastiest shape, because
// both the status code and the top-level flag say yes.
func TestAPackageRefusedInsideASuccessIsARefusal(t *testing.T) {
	c := bookingServer(t, 200, `{"success":true,"packages":[
		{"waybill":"","status":"Fail","remarks":["pincode not serviceable"]}]}`)

	got, err := c.CreateForwardShipment(context.Background(), "Alt Optiminastic", aShipment())
	if err == nil {
		t.Fatal("a refused package inside success:true was read as a booking - " +
			"the customer would be promised a parcel that was never booked")
	}
	if got.Accepted() {
		t.Error("a refused package reported itself accepted")
	}
	if got.Refused != "pincode not serviceable" {
		t.Errorf("refusal = %q, want Delhivery's own wording", got.Refused)
	}
}

// They sometimes echo the requested waybill back while refusing it. A waybill
// alone is not an acceptance.
func TestAWaybillWithARemarkIsStillARefusal(t *testing.T) {
	c := bookingServer(t, 200, `{"success":true,"packages":[
		{"waybill":"59906610025130","status":"Fail","remarks":["duplicate order id"]}]}`)

	if _, err := c.CreateForwardShipment(context.Background(), "Alt Optiminastic", aShipment()); err == nil {
		t.Fatal("a refused package with a waybill was read as a booking")
	}
}

// Missing fields are refused HERE, before a round trip, and named in the
// shop's vocabulary rather than Delhivery's.
func TestAnIncompleteShipmentIsRefusedBeforeSending(t *testing.T) {
	c := bookingServer(t, 200, `{"success":true,"packages":[{"waybill":"1","status":"Success"}]}`)

	for name, mangle := range map[string]func(*ForwardShipment){
		"no phone":   func(s *ForwardShipment) { s.To.Phone = "" },
		"no pincode": func(s *ForwardShipment) { s.To.Pincode = "" },
		"no address": func(s *ForwardShipment) { s.To.Address = "" },
		"no name":    func(s *ForwardShipment) { s.To.Name = "" },
		"no order":   func(s *ForwardShipment) { s.OrderNumber = "" },
	} {
		t.Run(name, func(t *testing.T) {
			s := aShipment()
			mangle(&s)
			_, err := c.CreateForwardShipment(context.Background(), "Alt Optiminastic", s)
			if err == nil {
				t.Fatalf("%s was sent to Delhivery anyway", name)
			}
			if !strings.Contains(err.Error(), "no courier can be booked") {
				t.Errorf("error %q does not say what is missing in the shop's words", err)
			}
		})
	}
}

// A missing pickup location is named as OUR setting, not passed through as
// their generic refusal.
func TestAMissingPickupLocationNamesTheSetting(t *testing.T) {
	c := bookingServer(t, 200, `{"success":true,"packages":[{"waybill":"1","status":"Success"}]}`)
	_, err := c.CreateForwardShipment(context.Background(), "  ", aShipment())
	if err == nil {
		t.Fatal("a blank pickup location was sent to Delhivery")
	}
	if !strings.Contains(err.Error(), "DELHIVERY_PICKUP_LOCATION") {
		t.Errorf("error %q does not name the setting to fix", err)
	}
}

// A REPLACEMENT IS NOT A SALE. The customer paid once; a COD booking would
// charge them again for a parcel that arrived broken.
func TestAReplacementIsNeverBookedCashOnDelivery(t *testing.T) {
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sent = r.PostForm.Get("data")
		_, _ = w.Write([]byte(`{"success":true,"packages":[{"waybill":"1","status":"Success"}]}`))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", BaseURL: srv.URL})
	if _, err := c.CreateForwardShipment(context.Background(), "Alt Optiminastic", aShipment()); err != nil {
		t.Fatalf("book: %v", err)
	}
	if !strings.Contains(sent, `"payment_mode":"Prepaid"`) {
		t.Errorf("payment mode is not Prepaid:\n%s", sent)
	}
	if strings.Contains(strings.ToUpper(sent), `"COD"`) {
		t.Errorf("a replacement was booked cash-on-delivery:\n%s", sent)
	}
	// The pickup location travels by name, and it is what Delhivery matches.
	if !strings.Contains(sent, `"name":"Alt Optiminastic"`) {
		t.Errorf("the pickup location did not reach Delhivery:\n%s", sent)
	}
}
