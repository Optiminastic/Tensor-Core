package delhivery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// testdata/track.json is a real response from Delhivery's production tracking
// endpoint with every identifier replaced: names, addresses, waybills, order
// references, towns and hub names are all invented. Real customers' parcel
// numbers do not belong in a repository, and the test does not need them.
//
// WHAT IS NOT EDITED is the shape, which is the whole point of testing
// against a captured response rather than a handwritten one: the field
// names, the JSON types (Address1 as [], PinCode as a number, Quantity as a
// string), the nulls, and the timestamp formats are exactly what the carrier
// sent.
func liveFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/track.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

// The decoder has to survive Delhivery's own inconsistency, which is the thing
// most likely to turn a working integration into a blank page.
func TestDecodesLiveResponse(t *testing.T) {
	var body struct {
		ShipmentData []struct {
			Shipment Shipment `json:"Shipment"`
		} `json:"ShipmentData"`
	}
	if err := json.Unmarshal(liveFixture(t), &body); err != nil {
		t.Fatalf("the carrier's own response did not decode: %v", err)
	}
	if len(body.ShipmentData) == 0 {
		t.Fatal("fixture carries no shipments")
	}

	for _, entry := range body.ShipmentData {
		s := entry.Shipment
		if s.AWB == "" {
			t.Error("waybill decoded empty")
		}
		if s.Status.Status == "" || s.Status.StatusType == "" {
			t.Errorf("%s: status decoded empty", s.AWB)
		}
		// Address1 and Address2 arrive as [] on every shipment on this
		// account. A plain string field fails the WHOLE batch on that, so this
		// is the assertion that matters most in the file.
		if s.Consignee.Name == "" {
			t.Errorf("%s: consignee name decoded empty", s.AWB)
		}
		if s.Consignee.City == "" || s.Consignee.PinCode == "" {
			// PinCode is a JSON number, not a string.
			t.Errorf("%s: city/pincode decoded empty", s.AWB)
		}
		if len(s.History()) == 0 {
			t.Errorf("%s: no scans decoded", s.AWB)
		}
		// Zoneless local timestamps, with and without fractional seconds.
		if s.PickupDate.IsZero() {
			t.Errorf("%s: pickup date decoded zero", s.AWB)
		}
		if s.Status.StatusDateTime.IsZero() {
			t.Errorf("%s: status time decoded zero", s.AWB)
		}
	}
}

// Delhivery's zoneless stamps are Indian time. Read as UTC they land five and a
// half hours early, which is the difference between "attempted this morning"
// and "attempted yesterday".
func TestStampIsIndianTime(t *testing.T) {
	var s Stamp
	if err := json.Unmarshal([]byte(`"2026-10-09T05:57:56.24"`), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := s.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-10-09T00:27:56Z" {
		t.Errorf("stamp read as %s, want 2026-10-09T00:27:56Z (IST 05:57:56)", got)
	}

	var absent Stamp
	if err := json.Unmarshal([]byte(`null`), &absent); err != nil {
		t.Fatalf("null stamp: %v", err)
	}
	if !absent.IsZero() {
		t.Error("a null stamp must stay zero, not become a real date")
	}
}

func TestTextAcceptsEveryShapeDelhiverySends(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`"Bhiwandi"`, "Bhiwandi"},
		{`"  padded  "`, "padded"},
		{`[]`, ""},
		{`["Flat 4","MG Road"]`, "Flat 4, MG Road"},
		{`421003`, "421003"},
		{`null`, ""},
	} {
		var got Text
		if err := json.Unmarshal([]byte(tc.raw), &got); err != nil {
			t.Errorf("%s: %v", tc.raw, err)
			continue
		}
		if got.String() != tc.want {
			t.Errorf("%s decoded as %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// Delhivery answers a bad request with HTTP 200 and an Error field, so the
// status code alone is not enough to know whether the call worked.
func TestTrackSurfacesTwoHundredErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"Error": "parameter ref_ids/ref_nos or waybill is required"}`))
	}))
	defer server.Close()

	client := New(Config{APIKey: "k", BaseURL: server.URL})
	if _, err := client.Track(context.Background(), []string{"1"}); err == nil {
		t.Fatal("a 200 carrying an Error must not be reported as success")
	} else if !strings.Contains(err.Error(), "waybill is required") {
		t.Errorf("error lost the carrier's own sentence: %v", err)
	}
}

// Fifty per request is Delhivery's cap, and exceeding it comes back SHORT
// rather than refused - so the chunking is the only thing stopping a long list
// from silently losing its tail.
//
// The requests run concurrently, so the ORDER they arrive in is not asserted -
// only their sizes, and the fact that the RESULT comes back in the order the
// waybills were asked for. A page that reshuffles itself between refreshes
// would be the obvious way to get concurrency wrong here.
func TestTrackChunksAtFifty(t *testing.T) {
	var (
		mu      sync.Mutex
		batches []int
		peak    int
		live    int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Token k" {
			t.Errorf("auth header %q, want %q (Bearer answers with a login page)", got, "Token k")
		}
		waybills := strings.Split(r.URL.Query().Get("waybill"), ",")

		mu.Lock()
		batches = append(batches, len(waybills))
		live++
		if live > peak {
			peak = live
		}
		mu.Unlock()
		// Long enough that overlapping requests actually overlap, so `peak`
		// measures something.
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		live--
		mu.Unlock()

		out := make([]string, 0, len(waybills))
		for _, wb := range waybills {
			out = append(out, `{"Shipment":{"AWB":"`+wb+`","Status":{"Status":"In Transit"}}}`)
		}
		_, _ = w.Write([]byte(`{"ShipmentData":[` + strings.Join(out, ",") + `]}`))
	}))
	defer server.Close()

	waybills := make([]string, 0, 120)
	for i := 0; i < 120; i++ {
		waybills = append(waybills, fmt.Sprintf("wb%03d", i))
	}
	// Plus a duplicate and a blank, which must not reach the carrier.
	asked := append(append([]string{}, waybills...), waybills[0], "   ")

	got, err := New(Config{APIKey: "k", BaseURL: server.URL}).
		Track(context.Background(), asked)
	if err != nil {
		t.Fatalf("track: %v", err)
	}

	sizes := append([]int{}, batches...)
	sort.Ints(sizes)
	if want := []int{20, 50, 50}; len(sizes) != 3 || sizes[0] != want[0] ||
		sizes[1] != want[1] || sizes[2] != want[2] {
		t.Errorf("batch sizes %v, want %v", sizes, want)
	}
	if peak < 2 {
		t.Errorf("requests never overlapped (peak %d) - the batches are still sequential", peak)
	}
	if peak > trackConcurrency {
		t.Errorf("%d requests in flight at once, limit is %d", peak, trackConcurrency)
	}

	if len(got) != 120 {
		t.Fatalf("tracked %d shipments, want 120", len(got))
	}
	for i, shipment := range got {
		if shipment.AWB.String() != waybills[i] {
			t.Fatalf("result %d is %s, want %s - the order was not preserved",
				i, shipment.AWB, waybills[i])
		}
	}
}

func TestUnconfiguredClientIsNil(t *testing.T) {
	if New(Config{APIKey: "   "}).Configured() {
		t.Error("a blank key must not count as configured")
	}
	var nilClient *Client
	if nilClient.Configured() {
		t.Error("Configured must be safe on a nil client")
	}
}
