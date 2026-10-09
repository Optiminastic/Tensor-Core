package sarvam

// Parsing what the analytics API actually returns.
//
// The fixture below is a real attempt from the live account, trimmed. It is
// here rather than invented because the two fields that matter most -
// call_summary and call_disposition - are not in Sarvam's documented response
// schema at all. They arrive inside agent_variables, written by the agent
// itself, and nothing but a real payload would have shown that.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const realAttempt = `{"items":[{
 "attempt_id":"076cf0c6-44ec-483b-bffb-5d167591b1c3",
 "interaction_id":"20261007/8dfc36f1-13:19:48-ea3d34fe",
 "connectivity_status":"connected","failure_reason":"NO_FAILURE_REASON",
 "ended_by":"USER_ENDS","duration_in_seconds":21.22993278503418,
 "start_datetime":"2026-10-07T07:49:48","language_name":"English","num_messages":3,
 "user_contact_masked":"+91******0003","user_contact":"+919000000003",
 "channel_direction":"outbound",
 "audio_url":"https://indus.sarvam.ai/media?x=1",
 "agent_variables":{"cart_value":"1099","customer_name":"Rishi Patel","item_count":"2",
   "Product_name":"Dual name plank and soulmate combo",
   "call_summary":"The call concluded abruptly.","call_disposition":"no_response",
   "nothing_here":null,"a_number":7}
}],"total":1}`

func analyticsServer(t *testing.T, body string) (*Client, *string) {
	t.Helper()
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath, not Path: net/http decodes %2F back into a slash,
		// which would hide the very thing this is checking.
		gotPath = r.URL.EscapedPath()
		if r.Header.Get("X-API-Key") == "" {
			t.Error("the analytics call carried no X-API-Key")
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return New(testConfig(server.URL)), &gotPath
}

func TestAnAttemptCarriesTheAgentsOwnSummaryAndOutcome(t *testing.T) {
	client, _ := analyticsServer(t, realAttempt)

	attempts, err := client.ListAttempts(context.Background(), time.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("got %d attempts, want 1", len(attempts))
	}
	a := attempts[0]

	// These two are the page's whole value and neither is in the documented
	// schema: Sarvam writes them into agent_variables at the end of the call.
	if a.Summary() != "The call concluded abruptly." {
		t.Errorf("summary = %q", a.Summary())
	}
	if a.Disposition() != "no_response" {
		t.Errorf("disposition = %q", a.Disposition())
	}
	// The full number is what joins a call back to its checkout.
	if a.UserContact != "+919000000003" {
		t.Errorf("user contact = %q", a.UserContact)
	}
	if a.InteractionID == "" {
		t.Error("no interaction id; the transcript could never be fetched")
	}
}

func TestAVariableThatIsNotAStringDoesNotBreakTheListing(t *testing.T) {
	// agent_variables is typed string-to-ANY on the wire. A null or a number
	// in there used to take the whole listing down with it, which would mean
	// one odd call hiding every other call on the page.
	client, _ := analyticsServer(t, realAttempt)

	attempts, err := client.ListAttempts(context.Background(), time.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatalf("a non-string variable broke the decode: %v", err)
	}
	vars := attempts[0].AgentVariables
	if _, present := vars["nothing_here"]; present {
		t.Error(`a null variable was kept; it would render as "<nil>"`)
	}
	if vars["a_number"] != "7" {
		t.Errorf("a_number = %q, want \"7\"", vars["a_number"])
	}
}

func TestTheInteractionIDIsEscapedIntoThePath(t *testing.T) {
	// "20261007/8dfc36f1-13:19:48-ea3d34fe" carries slashes and colons.
	// Interpolated raw they read as extra path segments and the request 404s.
	client, path := analyticsServer(t, `{"interaction_id":"x","messages":[]}`)

	if _, err := client.Transcript(context.Background(), "20261007/8dfc-13:19:48"); err != nil {
		t.Fatalf("transcript: %v", err)
	}
	id := strings.TrimPrefix(*path, "/api/analytics/v1/org-1/ws-1/agent-1/transcripts/")
	if strings.Contains(id, "/") {
		t.Errorf("the interaction id was not escaped, so it reads as extra path segments: %s", *path)
	}
	if !strings.Contains(id, "%2F") {
		t.Errorf("the slash was not percent-encoded: %s", id)
	}
}

func TestAnAnalyticsRefusalKeepsSarvamsOwnWords(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":"start_datetime is required"}`))
	}))
	defer server.Close()

	_, err := New(testConfig(server.URL)).ListAttempts(context.Background(), time.Now(), 10)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "start_datetime is required") {
		t.Errorf("Sarvam's sentence was lost: %v", err)
	}
}
