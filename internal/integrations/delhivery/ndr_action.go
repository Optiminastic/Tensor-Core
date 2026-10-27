package delhivery

// Telling Delhivery what to do about a parcel it could not deliver.
//
// THE FIRST WRITE IN A PACKAGE THAT WAS READ-ONLY ON PURPOSE. The file comment
// on client.go says so: /api/p/update changes a real parcel at somebody's
// address, so nothing here could reach it. That is still the right instinct -
// what changed is that the shop asked to run NDRs from Tensor rather than from
// two dashboards, and an action a person takes deliberately, from a row they
// are looking at, is a different thing from a background job reaching the same
// endpoint. Nothing in this file is called by a worker.
//
// IT IS ASYNCHRONOUS, and that is the trap. The response carries an upload id,
// not an outcome: Delhivery answers 200 to say it accepted the instruction,
// then applies it out of band. A screen that reads 200 as "re-attempt booked"
// is the same mistake the reverse-pickup message made - the API said yes, the
// customer got nothing. Submitted is the most this can honestly report.
//
// ONLY WHAT DELHIVERY DOCUMENTS. Re-attempt, defer, and edit the address.
// There is no return-to-origin here: RTO appears in third-party wrappers
// around this API, not in Delhivery's own action list, and a Return button
// that silently did nothing would be worse than no button.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ndrPath is the action endpoint, beside the tracking one.
const ndrPath = "/api/p/update"

// Act is what to do about an undelivered parcel.
type Act string

const (
	// Reattempt asks for another delivery run, on Delhivery's own schedule.
	// It takes no extra data: "try again" is the whole instruction.
	Reattempt Act = "RE-ATTEMPT"
	// Defer moves the attempt to a date the customer asked for.
	Defer Act = "DEFER_DLV"
	// EditAddress corrects where the parcel is going. The commonest real
	// cause of a failed delivery is an address that was never right.
	EditAddress Act = "EDIT_DETAILS"
)

// NDRInstruction is one parcel and what to do about it.
type NDRInstruction struct {
	// Waybill is the parcel. Required for every act.
	Waybill string
	Act     Act
	// Deferred is the new delivery date, for Defer only. Date, not time:
	// Delhivery schedules the run, not the hour.
	Deferred time.Time
	// Name, Address and Phone are for EditAddress. Empty fields are left as
	// they are rather than blanked - a correction to the house number must
	// not wipe the phone number the driver calls.
	Name    string
	Address string
	Phone   string
}

// NDRResult is what came back for one instruction.
//
// ACCEPTED IS NOT DONE. UploadID is the handle Delhivery gives for an
// instruction it has taken but not yet carried out; the outcome is read back
// separately. Callers must say "asked" rather than "done" on the strength of
// this.
type NDRResult struct {
	// UploadID identifies the submission, for checking what became of it.
	UploadID string
	// Accepted says Delhivery took the instruction. It does NOT say the
	// parcel will be re-attempted.
	Accepted bool
	// Message is Delhivery's own words, kept for a refusal.
	Message string
}

// UpdateNDR submits one instruction.
//
// ONE AT A TIME, although the endpoint takes a list. A person is acting on a
// row they are looking at, and a batch that half-succeeds is a worse thing to
// explain than two requests.
func (c *Client) UpdateNDR(ctx context.Context, in NDRInstruction) (NDRResult, error) {
	if !c.Configured() {
		return NDRResult{}, fmt.Errorf("delhivery is not configured")
	}
	item, err := in.payload()
	if err != nil {
		return NDRResult{}, err
	}

	body, err := json.Marshal(map[string]any{"data": []map[string]any{item}})
	if err != nil {
		return NDRResult{}, fmt.Errorf("build the instruction: %w", err)
	}

	// JSON here, unlike the booking calls, which are form-encoded with the
	// JSON in a `data` field. Same vendor, two conventions; sending the
	// booking shape to this endpoint is accepted and does nothing.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.BaseURL+ndrPath, bytes.NewReader(body))
	if err != nil {
		return NDRResult{}, err
	}
	req.Header.Set("Authorization", "Token "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return NDRResult{}, fmt.Errorf("reach delhivery: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	var parsed ndrResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return NDRResult{}, fmt.Errorf("delhivery answered %d with something unreadable: %w",
			res.StatusCode, err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return NDRResult{}, fmt.Errorf("delhivery refused the instruction (%d): %s",
			res.StatusCode, parsed.reason())
	}
	// A 200 WITH success:false IS A REFUSAL. Delhivery reports a rejected
	// instruction in the body, not the status line - the same shape the
	// booking calls have, and the same way a wrong payload looks like a
	// success until nothing happens.
	if !parsed.ok() {
		return NDRResult{}, fmt.Errorf("delhivery refused the instruction: %s", parsed.reason())
	}
	return NDRResult{UploadID: parsed.uploadID(), Accepted: true, Message: parsed.reason()}, nil
}

// isoDate is the only date shape Delhivery accepts here.
var isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// payload turns an instruction into the one item Delhivery expects, refusing
// what it can see is wrong before paying a round trip to be told.
func (in NDRInstruction) payload() (map[string]any, error) {
	waybill := strings.TrimSpace(in.Waybill)
	if waybill == "" {
		return nil, fmt.Errorf("no waybill, so there is no parcel to act on")
	}
	item := map[string]any{"waybill": waybill, "act": string(in.Act)}

	switch in.Act {
	case Reattempt:
		// Nothing to add: "try again" is the whole instruction.
		return item, nil

	case Defer:
		if in.Deferred.IsZero() {
			return nil, fmt.Errorf("a deferred delivery needs the date to defer it to")
		}
		date := in.Deferred.Format("2006-01-02")
		if !isoDate.MatchString(date) {
			return nil, fmt.Errorf("that is not a date Delhivery will accept")
		}
		item["action_data"] = map[string]any{"deferred_date": date}
		return item, nil

	case EditAddress:
		data := map[string]any{}
		// ONLY WHAT WAS FILLED IN. Sending an empty name alongside a
		// corrected house number is how a parcel loses the name on its label.
		for key, value := range map[string]string{
			"name": in.Name, "address": in.Address, "phone": in.Phone,
		} {
			if v := strings.TrimSpace(value); v != "" {
				data[key] = v
			}
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("nothing to correct: give a name, an address or a phone number")
		}
		item["action_data"] = data
		return item, nil
	}
	return nil, fmt.Errorf("%q is not something Delhivery can be asked to do", in.Act)
}

// ndrResponse reads the several shapes this endpoint answers in.
//
// DELIBERATELY LOOSE. The documented response is an upload id; live accounts
// have been seen returning a package list and a bare message, and a reader
// that insisted on one shape would turn a working instruction into an error.
type ndrResponse struct {
	UploadID  any    `json:"upload_id"`
	UPL       any    `json:"upl"`
	RequestID any    `json:"request_id"`
	Success   *bool  `json:"success"`
	Error     any    `json:"error"`
	Message   string `json:"message"`
	Remark    string `json:"rmk"`
}

func (r ndrResponse) ok() bool {
	if r.Success != nil {
		return *r.Success
	}
	// No success field: a refusal still says so in error.
	switch v := r.Error.(type) {
	case nil:
		return true
	case bool:
		return !v
	case string:
		return strings.TrimSpace(v) == ""
	}
	return true
}

func (r ndrResponse) uploadID() string {
	for _, v := range []any{r.UploadID, r.UPL, r.RequestID} {
		switch id := v.(type) {
		case string:
			if strings.TrimSpace(id) != "" {
				return id
			}
		case float64:
			return fmt.Sprintf("%.0f", id)
		}
	}
	return ""
}

func (r ndrResponse) reason() string {
	if s, ok := r.Error.(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	for _, s := range []string{r.Message, r.Remark} {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return "no reason given"
}
