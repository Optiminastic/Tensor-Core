package delhivery

// What gets sent to Delhivery, and what is refused before it is.
//
// THE PAYLOAD IS THE WHOLE RISK HERE. This endpoint answers 200 to an
// instruction it then ignores, exactly as the booking calls do, so a wrong
// field name does not fail - it quietly leaves a customer waiting for a
// re-attempt nobody asked for.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// captureNDR returns a client pointed at a server that records the one
// instruction it was sent, and answers with `reply`.
func captureNDR(t *testing.T, got *map[string]any, reply string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// JSON, not the form encoding the booking calls use. Getting this
		// wrong is accepted and does nothing.
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q, want JSON", ct)
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Token ") {
			t.Errorf("Authorization = %q, want a Token header", auth)
		}
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("unreadable body: %v", err)
		}
		if len(payload.Data) == 1 {
			*got = payload.Data[0]
		} else {
			t.Errorf("sent %d instructions, want exactly one", len(payload.Data))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return New(Config{APIKey: "k", BaseURL: srv.URL})
}

func TestAReattemptIsJustTheWaybillAndTheAct(t *testing.T) {
	var sent map[string]any
	c := captureNDR(t, &sent, `{"success":true,"upload_id":"UPL-1"}`)

	res, err := c.UpdateNDR(context.Background(), NDRInstruction{
		Waybill: "69187910001400", Act: Reattempt,
	})
	if err != nil {
		t.Fatalf("reattempt: %v", err)
	}
	if sent["waybill"] != "69187910001400" || sent["act"] != "RE-ATTEMPT" {
		t.Errorf("sent %#v", sent)
	}
	// "Try again" takes no data, and inventing some is how a documented
	// field name gets guessed at.
	if _, extra := sent["action_data"]; extra {
		t.Errorf("a reattempt carried action_data: %#v", sent["action_data"])
	}

	// ACCEPTED, NOT DONE. The id is the handle for asking later what became
	// of it; the parcel has not moved yet.
	if !res.Accepted || res.UploadID != "UPL-1" {
		t.Errorf("result = %#v", res)
	}
}

func TestADeferredDeliveryCarriesItsDate(t *testing.T) {
	var sent map[string]any
	c := captureNDR(t, &sent, `{"success":true,"upload_id":"UPL-2"}`)

	when := time.Date(2026, 11, 3, 15, 4, 5, 0, time.UTC)
	if _, err := c.UpdateNDR(context.Background(), NDRInstruction{
		Waybill: "69187910001400", Act: Defer, Deferred: when,
	}); err != nil {
		t.Fatalf("defer: %v", err)
	}
	data, ok := sent["action_data"].(map[string]any)
	if !ok {
		t.Fatalf("no action_data: %#v", sent)
	}
	// A DATE, NOT A TIME. Delhivery schedules the run, not the hour, and a
	// timestamp here is refused.
	if data["deferred_date"] != "2026-11-03" {
		t.Errorf("deferred_date = %v, want 2026-11-03", data["deferred_date"])
	}
}

func TestEditingAnAddressSendsOnlyWhatWasFilledIn(t *testing.T) {
	var sent map[string]any
	c := captureNDR(t, &sent, `{"success":true,"upload_id":"UPL-3"}`)

	// Correcting the house number must not blank the phone number the
	// driver calls.
	if _, err := c.UpdateNDR(context.Background(), NDRInstruction{
		Waybill: "69187910001400", Act: EditAddress,
		Address: "12 Hill Road, Bandra West",
	}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	data, ok := sent["action_data"].(map[string]any)
	if !ok {
		t.Fatalf("no action_data: %#v", sent)
	}
	if data["address"] != "12 Hill Road, Bandra West" {
		t.Errorf("address = %v", data["address"])
	}
	for _, blank := range []string{"name", "phone"} {
		if _, present := data[blank]; present {
			t.Errorf("sent an empty %s, which would overwrite what is on the label", blank)
		}
	}
}

func TestAnInstructionThatCannotWorkIsRefusedHere(t *testing.T) {
	var sent map[string]any
	c := captureNDR(t, &sent, `{"success":true}`)

	cases := []struct {
		name string
		in   NDRInstruction
		want string
	}{
		{"no waybill", NDRInstruction{Act: Reattempt}, "waybill"},
		{"deferred with no date", NDRInstruction{Waybill: "1", Act: Defer}, "date"},
		{"an edit that edits nothing", NDRInstruction{Waybill: "1", Act: EditAddress}, "correct"},
		{"an act Delhivery has no name for", NDRInstruction{Waybill: "1", Act: Act("RETURN")}, "not something"},
	}
	for _, tc := range cases {
		_, err := c.UpdateNDR(context.Background(), tc.in)
		if err == nil {
			t.Errorf("%s was sent to Delhivery", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s refused with %q, which does not mention %q", tc.name, err, tc.want)
		}
	}
}

func TestA200ThatSaysNoIsStillNo(t *testing.T) {
	var sent map[string]any
	// THE SHAPE THAT HIDES A FAILURE. Delhivery reports a rejected
	// instruction in the body, not the status line - the same trap the
	// booking calls have.
	c := captureNDR(t, &sent, `{"success":false,"error":"waybill not in a pending state"}`)

	_, err := c.UpdateNDR(context.Background(), NDRInstruction{
		Waybill: "69187910001400", Act: Reattempt,
	})
	if err == nil {
		t.Fatal("a refusal was read as an accepted instruction")
	}
	if !strings.Contains(err.Error(), "pending state") {
		t.Errorf("the refusal loses Delhivery's reason: %v", err)
	}
}

func TestAnUnrecognisedButSuccessfulReplyIsNotAnError(t *testing.T) {
	var sent map[string]any
	// Live accounts answer in shapes the documentation does not show. A
	// reader that insisted on one of them would turn a working instruction
	// into an error, and the operator would send it again.
	for _, reply := range []string{
		`{"upload_id":"UPL-9"}`,
		`{"request_id":12345}`,
		`{"success":true,"rmk":"accepted"}`,
	} {
		c := captureNDR(t, &sent, reply)
		res, err := c.UpdateNDR(context.Background(), NDRInstruction{
			Waybill: "1", Act: Reattempt,
		})
		if err != nil {
			t.Errorf("%s was read as a failure: %v", reply, err)
			continue
		}
		if !res.Accepted {
			t.Errorf("%s was not accepted", reply)
		}
	}
}
