package httpapi

// Turning what the screen says into what Delhivery accepts.
//
// Pure: no server, no courier. What is worth pinning is the REFUSALS, because
// each one is a message an operator reads and acts on, and because a defer
// with a date in the past is accepted by Delhivery and then never happens.

import (
	"strings"
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/integrations/delhivery"
)

func TestTheScreensWordsBecomeDelhiverysActs(t *testing.T) {
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	cases := []struct {
		name string
		req  ndrActionRequest
		want delhivery.Act
	}{
		{"reattempt", ndrActionRequest{Act: "reattempt"}, delhivery.Reattempt},
		{"defer", ndrActionRequest{Act: "defer", Date: tomorrow}, delhivery.Defer},
		{"edit", ndrActionRequest{Act: "edit", Address: "12 Hill Road"}, delhivery.EditAddress},
		// The screen should not have to know Delhivery spells it RE-ATTEMPT,
		// and an operator should not be caught by capitals.
		{"mixed case", ndrActionRequest{Act: "ReAttempt"}, delhivery.Reattempt},
	}
	for _, tc := range cases {
		in, refusal := ndrInstruction("69187910001400", tc.req)
		if refusal != "" {
			t.Errorf("%s was refused: %s", tc.name, refusal)
			continue
		}
		if in.Act != tc.want {
			t.Errorf("%s became %q, want %q", tc.name, in.Act, tc.want)
		}
		if in.Waybill != "69187910001400" {
			t.Errorf("%s lost the waybill", tc.name)
		}
	}
}

func TestADeferredDateInThePastIsRefusedHere(t *testing.T) {
	// DELHIVERY ACCEPTS THIS AND NEVER ACTS ON IT. A 200 back, a row that
	// says the delivery was rescheduled, and a parcel that sits where it is.
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	_, refusal := ndrInstruction("1", ndrActionRequest{Act: "defer", Date: yesterday})
	if refusal == "" {
		t.Fatal("a date in the past was sent to the courier")
	}
	if !strings.Contains(strings.ToLower(refusal), "passed") {
		t.Errorf("refusal reads %q, which does not say the date has passed", refusal)
	}
}

func TestEveryRefusalNamesWhatToFix(t *testing.T) {
	cases := []struct {
		name, want string
		req        ndrActionRequest
	}{
		{"defer with no date", "date", ndrActionRequest{Act: "defer"}},
		{"unreadable date", "date", ndrActionRequest{Act: "defer", Date: "3rd Nov"}},
		{"edit with nothing in it", "at least one", ndrActionRequest{Act: "edit"}},
		{"something else entirely", "not something", ndrActionRequest{Act: "teleport"}},
	}
	for _, tc := range cases {
		_, refusal := ndrInstruction("1", tc.req)
		if refusal == "" {
			t.Errorf("%s was accepted", tc.name)
			continue
		}
		if !strings.Contains(strings.ToLower(refusal), tc.want) {
			t.Errorf("%s refused with %q, which does not mention %q", tc.name, refusal, tc.want)
		}
	}
}

func TestReturnIsRefusedByName(t *testing.T) {
	// The one an operator will look for, because the Delhivery dashboard has
	// the button. Saying "that is not something the courier can be asked to
	// do" would read as a bug; saying where to do it is the useful answer.
	_, refusal := ndrInstruction("1", ndrActionRequest{Act: "return"})
	if !strings.Contains(refusal, "Delhivery dashboard") {
		t.Errorf("a return was refused with %q, which does not say where to do it", refusal)
	}
}
