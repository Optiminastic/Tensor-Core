package sarvam

// What the request body has to look like.
//
// Worth pinning precisely, because Sarvam's own quickstart gets it wrong in
// three places and each one costs a round trip to a 422 to discover. If these
// tests fail after an edit, the edit is wrong - not the test - unless the live
// API has actually changed.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testConfig(baseURL string) Config {
	return Config{
		BaseURL:          baseURL,
		APIKey:           "sk_samvaad_test",
		OrgID:            "org-1",
		WorkspaceID:      "ws-1",
		AgentID:          "agent-1",
		AgentVersion:     2,
		ConnectionID:     "conn-1",
		AgentPhoneNumber: "+918065589544",
	}
}

// The shape Sarvam actually accepts, which is NOT the one in its quickstart:
// app_version is an integer, connection_config nests inside app_config, the
// customer travels in user_config, and agent_variables sit in APP_CONFIG.
func TestTheRequestMatchesTheShapeSarvamAccepts(t *testing.T) {
	var got map[string]any
	var gotKey, gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"attempt_id":"att-1"}`))
	}))
	defer server.Close()

	client := New(testConfig(server.URL))
	result, err := client.Call(context.Background(), CallRequest{
		CustomerNumber: "+919000000001",
		Variables:      map[string]string{"customer_name": "Priya"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if result.AttemptID != "att-1" {
		t.Errorf("attempt id = %q, want att-1", result.AttemptID)
	}
	if gotKey != "sk_samvaad_test" {
		t.Errorf("X-API-Key = %q; Sarvam rejects every other header name", gotKey)
	}
	if want := "/api/outbounds/v1/orgs/org-1/workspaces/ws-1/outbounds"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}

	app, ok := got["app_config"].(map[string]any)
	if !ok {
		t.Fatalf("no app_config in %v", got)
	}
	// An INTEGER. "v2" is what the console displays and what the quickstart
	// sends, and the API answers "unable to parse string as an integer".
	if version, isNumber := app["app_version"].(float64); !isNumber || version != 2 {
		t.Errorf("app_version = %#v, want the number 2", app["app_version"])
	}
	// NESTED, not a sibling of app_config.
	conn, ok := app["connection_config"].(map[string]any)
	if !ok {
		t.Fatalf("connection_config must sit inside app_config; got %v", app)
	}
	if conn["agent_phone_number"] != "+918065589544" {
		t.Errorf("agent_phone_number = %v", conn["agent_phone_number"])
	}

	// agent_variables belong to the AGENT, so they sit in app_config beside it.
	//
	// The sharpest assertion here. Put them in user_config and Sarvam accepts
	// the body, places the call and hands back an attempt id - it just runs the
	// agent with no values, so the customer hears a script with the
	// personalisation missing. There is no error to notice, which is why this
	// is pinned rather than trusted.
	vars, ok := app["agent_variables"].(map[string]any)
	if !ok || vars["customer_name"] != "Priya" {
		t.Errorf("agent_variables = %v, want them inside app_config", app["agent_variables"])
	}

	// The customer goes in user_config.user_phone_number, not at the top level.
	user, ok := got["user_config"].(map[string]any)
	if !ok {
		t.Fatalf("no user_config in %v", got)
	}
	if user["user_phone_number"] != "+919000000001" {
		t.Errorf("user_phone_number = %v", user["user_phone_number"])
	}
	if _, misplaced := user["agent_variables"]; misplaced {
		t.Error("agent_variables in user_config are silently ignored; they belong in app_config")
	}
	if _, stray := got["customer_number"]; stray {
		t.Error("customer_number is the quickstart's field name and is not accepted")
	}
}

// Sarvam's refusals name the thing to go and fix - a number missing from a
// connection, a field in the wrong place. Losing that sentence for a status
// code is how an afternoon disappears.
func TestARefusalKeepsSarvamsOwnExplanation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"(404) Not Found","type":"not_found",` +
			`"code":404,"data":{"details":"Agent phone number '+918065354620' not found ` +
			`under org 'o', workspace 'w', and connection 'c'."}}}`))
	}))
	defer server.Close()

	_, err := New(testConfig(server.URL)).
		Call(context.Background(), CallRequest{CustomerNumber: "+919000000001"})
	if err == nil {
		t.Fatal("a 404 was reported as success")
	}
	if want := "not found under org"; !contains(err.Error(), want) {
		t.Errorf("error = %q, want it to carry Sarvam's own detail", err)
	}
}

// A deployment with no Sarvam credentials is the normal case - every Tensor
// that does not ring customers - and must still start and serve everything
// else. So an unconfigured client is nil and says so, rather than panicking
// when something calls it.
func TestAnUnconfiguredClientIsSafeToHold(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  Config
	}{
		{"no key", Config{OrgID: "o", WorkspaceID: "w", AgentID: "a", ConnectionID: "c", AgentPhoneNumber: "+91"}},
		{"no connection", Config{APIKey: "k", OrgID: "o", WorkspaceID: "w", AgentID: "a", AgentPhoneNumber: "+91"}},
		{"no number", Config{APIKey: "k", OrgID: "o", WorkspaceID: "w", AgentID: "a", ConnectionID: "c"}},
		{"nothing at all", Config{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			client := New(c.cfg)
			if client.Configured() {
				t.Fatal("an incomplete configuration reported itself as ready to call")
			}
			if got := client.AgentPhoneNumber(); got != "" {
				t.Errorf("AgentPhoneNumber = %q on a nil client", got)
			}
			if _, err := client.Call(context.Background(),
				CallRequest{CustomerNumber: "+919000000001"}); err == nil {
				t.Error("a nil client placed a call")
			}
		})
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle || len(needle) == 0 ||
			indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
