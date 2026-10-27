package delhivery

// The fixtures here are the LIVE shapes, captured from the account on
// 2026-10-27 and trimmed - including the details that break a naive decoder:
// dispatch_count arrives as the string "1", the tool's payload is JSON encoded
// as TEXT inside a JSON-RPC envelope, and a refusal can travel inside that
// text with the JSON-RPC call itself reported as a success.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const liveExceptionPayload = `{
  "total_count": 0,
  "result_count": 1,
  "results": [{
    "awb_number": "69187910000232",
    "exception_id": "9663139c-b0de-4c2d-a81f-891983b9fb41",
    "exception_time": 1792486267,
    "exception_bucket": "EB15",
    "exception_bucket_name": "Pending NDR Verification",
    "cta_list": {"CTA002": "Return shipment", "CTA023": "Reattempt shipment"},
    "sla": 1792659067,
    "exception_nsl_remark": "Consignee Unavailable",
    "geofence": false,
    "call_attempted": true,
    "call_duration": 141,
    "geofence_distance": 2122.28,
    "payment_mode": "Pre-paid",
    "dispatch_count": "1",
    "action": "Auto Re-attempt",
    "pkg_status_type": "DL",
    "pkg_status": "Delivered"
  }]
}`

// rpcReply wraps a tool payload the way the gateway does.
func rpcReply(t *testing.T, payload string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": payload}},
		},
	})
	if err != nil {
		t.Fatalf("rpcReply: %v", err)
	}
	return string(body)
}

type capture struct {
	body   string
	header http.Header
	mints  int
}

// gatewayStub serves the Keycloak token and the MCP gateway.
func gatewayStub(t *testing.T, reply func() (string, int)) (*UCPClient, *capture) {
	t.Helper()
	seen := &capture{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "openid-connect/token") {
			seen.mints++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok-123","expires_in":300}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		seen.body = string(raw)
		seen.header = r.Header.Clone()
		text, status := reply()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(text))
	}))
	t.Cleanup(srv.Close)

	c := NewUCP(UCPConfig{
		ClientID: "id", ClientSecret: "secret",
		Realm: "ucp-TEST", HQClientID: "cms::client::abc",
		GatewayURL: srv.URL + "/delhivery-one/c/test/mcp",
		AuthURL:    srv.URL,
	})
	if c == nil {
		t.Fatal("NewUCP returned nil for a complete credential")
	}
	return c, seen
}

// toolCall is the JSON-RPC request the gateway receives.
type toolCall struct {
	Method string `json:"method"`
	Params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"params"`
}

func decodeCall(t *testing.T, body string) toolCall {
	t.Helper()
	var call toolCall
	if err := json.Unmarshal([]byte(body), &call); err != nil {
		t.Fatalf("request body: %v (%s)", err, body)
	}
	return call
}

// gatewayStubFunc is gatewayStub for tests whose reply depends on the request.
func gatewayStubFunc(t *testing.T, reply func(body string) (string, int)) (*UCPClient, *capture) {
	t.Helper()
	seen := &capture{}
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "openid-connect/token") {
			mu.Lock()
			seen.mints++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok-123","expires_in":300}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen.body = string(raw)
		seen.header = r.Header.Clone()
		mu.Unlock()
		text, status := reply(string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(text))
	}))
	t.Cleanup(srv.Close)

	c := NewUCP(UCPConfig{
		ClientID: "id", ClientSecret: "secret",
		Realm: "ucp-TEST", HQClientID: "cms::client::abc",
		GatewayURL: srv.URL + "/delhivery-one/c/test/mcp",
		AuthURL:    srv.URL,
	})
	if c == nil {
		t.Fatal("NewUCP returned nil for a complete credential")
	}
	return c, seen
}

func decodeCallArgs(t *testing.T, body string) map[string]any {
	t.Helper()
	return decodeCall(t, body).Params.Arguments
}

func TestNewUCPNeedsEveryPart(t *testing.T) {
	full := UCPConfig{ClientID: "a", ClientSecret: "b", Realm: "c",
		HQClientID: "d", GatewayURL: "https://gw.example/mcp"}
	if NewUCP(full) == nil {
		t.Fatal("a complete credential should build a client")
	}
	// A half-configured client reading another account's parcels is worse
	// than one that says no, so every field is load-bearing - including the
	// gateway URL, which is per account and has no safe default.
	for _, drop := range []string{"ClientID", "ClientSecret", "Realm", "HQClientID", "GatewayURL"} {
		cfg := full
		switch drop {
		case "ClientID":
			cfg.ClientID = "  "
		case "ClientSecret":
			cfg.ClientSecret = ""
		case "Realm":
			cfg.Realm = ""
		case "HQClientID":
			cfg.HQClientID = ""
		case "GatewayURL":
			cfg.GatewayURL = "   "
		}
		if NewUCP(cfg) != nil {
			t.Errorf("missing %s should give no client", drop)
		}
	}
}

func TestTokenURLSurvivesARealmsSuffix(t *testing.T) {
	// The auth URL is pasted by hand, and Delhivery's own docs show it both
	// with and without the /realms segment. Building .../realms/realms/...
	// fails at sign-in and looks exactly like a wrong secret.
	for _, given := range []string{"https://auth.example", "https://auth.example/realms/"} {
		c := NewUCP(UCPConfig{ClientID: "a", ClientSecret: "b", Realm: "r",
			HQClientID: "h", GatewayURL: "https://gw/mcp", AuthURL: given})
		if c == nil {
			t.Fatalf("no client for auth URL %q", given)
		}
		if strings.Contains(c.cfg.AuthURL, "realms") {
			t.Errorf("auth URL %q kept a realms segment: %q", given, c.cfg.AuthURL)
		}
	}
}

func TestListExceptionsDecodesTheLiveShape(t *testing.T) {
	c, seen := gatewayStub(t, func() (string, int) {
		return rpcReply(t, liveExceptionPayload), 200
	})

	rows, err := c.ListExceptions(context.Background(), ExceptionQuery{Tab: TabAutoReattempt})
	if err != nil {
		t.Fatalf("ListExceptions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	got := rows[0]

	if got.Waybill != "69187910000232" {
		t.Errorf("waybill = %q", got.Waybill)
	}
	// The field that makes the page match their dashboard.
	if got.Action != "Auto Re-attempt" {
		t.Errorf("action = %q, want the tab Delhivery files it under", got.Action)
	}
	// Arrives as a string; a plain int field would fail the whole decode.
	if got.Attempts() != 1 {
		t.Errorf("attempts = %d, want 1", got.Attempts())
	}
	if got.CTAList["CTA023"] != "Reattempt shipment" {
		t.Errorf("cta_list not decoded: %v", got.CTAList)
	}
	if !got.CallAttempted || got.CallDuration != 141 {
		t.Errorf("call proof lost: %v %d", got.CallAttempted, got.CallDuration)
	}
	if got.GeofenceDistance != 2122.28 {
		t.Errorf("geofence distance = %v", got.GeofenceDistance)
	}

	// The gateway identifies the account by header, not by URL.
	if seen.header.Get("x-hq-client-id") != "cms::client::abc" {
		t.Errorf("account header missing: %q", seen.header.Get("x-hq-client-id"))
	}
	if seen.header.Get("X-UCP-Realm") != "ucp-TEST" {
		t.Errorf("realm header missing: %q", seen.header.Get("X-UCP-Realm"))
	}
	// Required by the gateway; "--" is its own spelling for "no human here".
	if seen.header.Get("X-UCP-User-Email") != "--" {
		t.Errorf("user header = %q, want the no-human default", seen.header.Get("X-UCP-User-Email"))
	}
	if seen.header.Get("Authorization") != "Bearer tok-123" {
		t.Errorf("bearer missing: %q", seen.header.Get("Authorization"))
	}
	if seen.header.Get("X-Request-ID") == "" {
		t.Error("no request id; Delhivery's logs cannot be searched without one")
	}

	call := decodeCall(t, seen.body)
	if call.Method != "tools/call" || call.Params.Name != toolListExceptions {
		t.Errorf("called %s/%s", call.Method, call.Params.Name)
	}
	if call.Params.Arguments["action"] != "AUTO_REATTEMPT" {
		t.Errorf("action filter = %v", call.Params.Arguments["action"])
	}
}

func TestEverySweepCallCarriesADispatchOperator(t *testing.T) {
	// REGRESSION. Omitting the pair was tried and is refused by the
	// gateway's schema, before Delhivery sees the call:
	// "Input validation error: 'range_dispatch_count_op' is a required
	// property". Every call must carry one.
	var mu sync.Mutex
	var bodies []string
	c, _ := gatewayStubFunc(t, func(body string) (string, int) {
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		return rpcReply(t, `{"result_count":0,"results":[]}`), 200
	})

	if _, err := c.ListExceptions(context.Background(), ExceptionQuery{Tab: TabNeedAction}); err != nil {
		t.Fatalf("ListExceptions: %v", err)
	}
	if len(bodies) != len(dispatchSlices) {
		t.Fatalf("made %d calls, want one per slice (%d)", len(bodies), len(dispatchSlices))
	}
	for _, body := range bodies {
		args := decodeCall(t, body).Params.Arguments
		op, present := args["range_dispatch_count_op"].(string)
		if !present || op == "" {
			t.Errorf("a call went out with no dispatch operator: %v", args["range_dispatch_count_op"])
		}
		if op != "EQ" && op != "GT" {
			t.Errorf("operator %q is not one Delhivery accepts", op)
		}
		// The sort and time-range parameters DO go out empty: the schema
		// requires them and empty clears Delhivery's validator.
		for _, key := range []string{"field", "direction", "op", "value"} {
			if value, ok := args[key]; !ok || value != "" {
				t.Errorf("%s = %v (present=%v), want an empty string", key, value, ok)
			}
		}
	}
}

func TestSweepCoversEveryAttemptCount(t *testing.T) {
	// Nought through three exactly, then everything above three. A gap here
	// is a parcel that exists on Delhivery's dashboard and not on ours.
	seenEQ := map[int]bool{}
	gt := 0
	for _, slice := range dispatchSlices {
		switch slice.Op {
		case "EQ":
			seenEQ[slice.Value] = true
		case "GT":
			if slice.Value != 3 {
				t.Errorf("GT %d is not accepted; Delhivery takes GT 3 only", slice.Value)
			}
			gt++
		default:
			t.Errorf("%s is not an operator Delhivery accepts", slice.Op)
		}
	}
	for n := 0; n <= 3; n++ {
		if !seenEQ[n] {
			t.Errorf("no slice covers exactly %d attempts", n)
		}
	}
	if gt != 1 {
		t.Errorf("want exactly one GT slice, got %d", gt)
	}
}

func TestSweepMergesAcrossSlices(t *testing.T) {
	var mu sync.Mutex
	n := 0
	c, _ := gatewayStubFunc(t, func(string) (string, int) {
		mu.Lock()
		n++
		id := n
		mu.Unlock()
		return rpcReply(t, `{"result_count":1,"results":[{"awb_number":"AWB`+
			strconv.Itoa(id)+`","exception_id":"ex`+strconv.Itoa(id)+`"}]}`), 200
	})

	rows, err := c.ListExceptions(context.Background(), ExceptionQuery{Tab: TabNeedAction})
	if err != nil {
		t.Fatalf("ListExceptions: %v", err)
	}
	if len(rows) != len(dispatchSlices) {
		t.Errorf("merged %d rows, want one per slice (%d)", len(rows), len(dispatchSlices))
	}
}

func TestSweepDeduplicatesByExceptionID(t *testing.T) {
	// A row can answer to more than one slice if Delhivery's count shifts
	// mid-sweep. Showing it twice would have somebody re-attempt it twice.
	c, _ := gatewayStubFunc(t, func(string) (string, int) {
		return rpcReply(t, `{"result_count":1,"results":[{"awb_number":"A","exception_id":"same"}]}`), 200
	})
	rows, err := c.ListExceptions(context.Background(), ExceptionQuery{Tab: TabNeedAction})
	if err != nil {
		t.Fatalf("ListExceptions: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("the same exception came back %d times", len(rows))
	}
}

func TestOneBadSliceStillReturnsTheRest(t *testing.T) {
	// A tab that is short AND SAYS SO beats one that is short and silent.
	var mu sync.Mutex
	n := 0
	c, _ := gatewayStubFunc(t, func(string) (string, int) {
		mu.Lock()
		n++
		first := n == 1
		id := n
		mu.Unlock()
		if first {
			return rpcReply(t, `{"error_code":"X","message":"internal:temporarily unavailable"}`), 200
		}
		return rpcReply(t, `{"result_count":1,"results":[{"awb_number":"A`+
			strconv.Itoa(id)+`","exception_id":"ex`+strconv.Itoa(id)+`"}]}`), 200
	})

	rows, err := c.ListExceptions(context.Background(), ExceptionQuery{Tab: TabNeedAction})
	if len(rows) != len(dispatchSlices)-1 {
		t.Errorf("got %d rows, want the %d that succeeded", len(rows), len(dispatchSlices)-1)
	}
	if err == nil {
		t.Fatal("a partial sweep must say which attempt counts are missing")
	}
	if !strings.Contains(err.Error(), "may be short") {
		t.Errorf("the warning should name the gap: %v", err)
	}
}

func TestEverySliceFailingIsAnError(t *testing.T) {
	c, _ := gatewayStubFunc(t, func(string) (string, int) {
		return rpcReply(t, `{"error_code":"INVALID_OR_MISSING_PARAMS",`+
			`"message":"internal:Bucket is a mandatory field and should be valid !"}`), 200
	})
	rows, err := c.ListExceptions(context.Background(), ExceptionQuery{Tab: TabNeedAction})
	if err == nil {
		t.Fatal("nothing came back; that is an error, not an empty queue")
	}
	if rows != nil {
		t.Errorf("want no rows, got %d", len(rows))
	}
	// Delhivery's own words, so an operator knows what to fix.
	if !strings.Contains(err.Error(), "Bucket is a mandatory field") {
		t.Errorf("refusal lost: %v", err)
	}
}

func TestSliceFollowsItsPages(t *testing.T) {
	// A full page means there may be more. result_count is not trusted for
	// this - it has come back as 3 beside a total_count of 0 on the live
	// account - so the rows themselves are counted.
	var mu sync.Mutex
	pages := map[int]int{}
	c, _ := gatewayStubFunc(t, func(body string) (string, int) {
		args := decodeCallArgs(t, body)
		page := int(args["page"].(float64))
		mu.Lock()
		pages[page]++
		mu.Unlock()
		if page == 1 {
			rows := make([]string, sweepPageSize)
			for i := range rows {
				rows[i] = `{"awb_number":"A` + strconv.Itoa(i) + `","exception_id":"p1-` +
					strconv.Itoa(i) + `"}`
			}
			return rpcReply(t, `{"results":[`+strings.Join(rows, ",")+`]}`), 200
		}
		return rpcReply(t, `{"results":[{"awb_number":"last","exception_id":"p2"}]}`), 200
	})

	rows, err := c.ListExceptions(context.Background(), ExceptionQuery{Tab: TabNeedAction})
	if err != nil {
		t.Fatalf("ListExceptions: %v", err)
	}
	if pages[2] == 0 {
		t.Error("a full first page must be followed by a second")
	}
	if pages[3] != 0 {
		t.Error("a short page is the last page; nothing should ask for a third")
	}
	// Ids are unique per page but shared across slices, so the merge keeps
	// one page-1 set plus one page-2 row.
	if len(rows) != sweepPageSize+1 {
		t.Errorf("got %d rows, want %d", len(rows), sweepPageSize+1)
	}
}

func TestEveryTabAsksForEveryStatus(t *testing.T) {
	// REGRESSION, and the subtlest bug this file has had. Delhivery CLOSES
	// an exception when its own automation takes it over, while still
	// listing the parcel under Auto Reattempt - so filtering to OPEN+OVERDUE
	// returned an empty tab beside a dashboard showing two live parcels.
	// The "action" parameter is what picks the tab; status must stay wide.
	for _, tab := range []Tab{TabNeedAction, TabAutoReattempt, TabReturnRecommendation, TabClosed} {
		c, seen := gatewayStub(t, func() (string, int) {
			return rpcReply(t, `{"results":[]}`), 200
		})
		if _, err := c.ListExceptions(context.Background(), ExceptionQuery{Tab: tab}); err != nil {
			t.Fatalf("%s: %v", tab, err)
		}
		args := decodeCall(t, seen.body).Params.Arguments
		status, _ := args["status"].([]any)
		if len(status) != 3 {
			t.Errorf("%s asked for %v; narrowing status hides live parcels", tab, status)
		}
		if args["action"] != string(tab) {
			t.Errorf("%s sent action %v", tab, args["action"])
		}
	}
}

func TestSSEFramedReplyIsUnwrapped(t *testing.T) {
	// The gateway negotiates text/event-stream and may answer with one
	// "data: {...}" line. Decoding that as JSON fails on the first byte.
	c, _ := gatewayStub(t, func() (string, int) {
		return "event: message\ndata: " + rpcReply(t, liveExceptionPayload) + "\n\n", 200
	})
	rows, err := c.ListExceptions(context.Background(), ExceptionQuery{Tab: TabNeedAction})
	if err != nil {
		t.Fatalf("an SSE-framed reply should decode: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("want 1 row through SSE framing, got %d", len(rows))
	}
}

func TestRefusalInsideTheTextIsAnError(t *testing.T) {
	// Measured: Delhivery's refusal envelope travels inside the tool text
	// while the JSON-RPC call reports success. Decoded naively it becomes an
	// empty result set, which this screen renders as "nothing is stuck".
	const envelope = `{"error_code":"INVALID_OR_MISSING_PARAMS",
		"message":"internal:Bucket is a mandatory field and should be valid !"}`
	c, _ := gatewayStub(t, func() (string, int) { return rpcReply(t, envelope), 200 })

	_, err := c.ListExceptions(context.Background(), ExceptionQuery{Tab: TabNeedAction})
	if err == nil {
		t.Fatal("a refusal inside the text must not read as an empty queue")
	}
	if !strings.Contains(err.Error(), "Bucket is a mandatory field") {
		t.Errorf("Delhivery's own words should survive: %v", err)
	}
	// The "internal:" prefix is theirs and means nothing to an operator.
	if strings.Contains(err.Error(), "internal:") {
		t.Errorf("internal: prefix should be stripped: %v", err)
	}
}

func TestJSONRPCErrorIsAnError(t *testing.T) {
	c, _ := gatewayStub(t, func() (string, int) {
		return `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Unknown tool"}}`, 200
	})
	_, err := c.ListExceptions(context.Background(), ExceptionQuery{Tab: TabNeedAction})
	if err == nil || !strings.Contains(err.Error(), "Unknown tool") {
		t.Errorf("a JSON-RPC error should surface: %v", err)
	}
}

func TestCloudFrontHTMLIsNamedForWhatItIs(t *testing.T) {
	// The first version of this client POSTed to one.delhivery.com and got a
	// CloudFront 403 HTML page. Pasting angle brackets on screen helps
	// nobody; naming it as "never reached the API" is what led to the fix.
	c, _ := gatewayStub(t, func() (string, int) {
		return `<!DOCTYPE HTML><html><body>ERROR: The request could not be satisfied</body></html>`, 403
	})
	_, err := c.ListExceptions(context.Background(), ExceptionQuery{Tab: TabNeedAction})
	if err == nil {
		t.Fatal("a 403 HTML page is an error")
	}
	if !strings.Contains(err.Error(), "HTML error page") {
		t.Errorf("an HTML body should be named, not pasted: %v", err)
	}
}

func TestApplyCTASendsTheRowsOwnCode(t *testing.T) {
	c, seen := gatewayStub(t, func() (string, int) {
		return rpcReply(t, `{"success":true,"message":"Successfully updated the exception details"}`), 200
	})

	err := c.ApplyCTA(context.Background(), CTAAction{
		ExceptionID: "9663139c", Bucket: "EB15", Code: "CTA023",
	})
	if err != nil {
		t.Fatalf("ApplyCTA: %v", err)
	}
	call := decodeCall(t, seen.body)
	if call.Params.Name != toolUpdateExceptions {
		t.Errorf("called tool %q", call.Params.Name)
	}
	if call.Params.Arguments["actionable_id"] != "CTA023" ||
		call.Params.Arguments["bucket_id"] != "EB15" {
		t.Errorf("wrong instruction: %v", call.Params.Arguments)
	}
}

func TestApplyCTARefusesWithoutAnIDOrCode(t *testing.T) {
	c, _ := gatewayStub(t, func() (string, int) { return rpcReply(t, `{"success":true}`), 200 })
	if err := c.ApplyCTA(context.Background(), CTAAction{Code: "CTA023"}); err == nil {
		t.Error("an action with no exception id should be refused here, not sent")
	}
	if err := c.ApplyCTA(context.Background(), CTAAction{ExceptionID: "x"}); err == nil {
		t.Error("an action with no code should be refused here, not sent")
	}
}

func TestApplyCTAPassesDelhiverysRefusalThrough(t *testing.T) {
	c, _ := gatewayStub(t, func() (string, int) {
		return rpcReply(t, `{"success":false,"message":"Shipment is not in a pending state"}`), 200
	})
	err := c.ApplyCTA(context.Background(), CTAAction{ExceptionID: "x", Code: "CTA023"})
	if err == nil {
		t.Fatal("success:false must be an error")
	}
	// "not in a pending state" tells an operator the attempt already
	// happened, which is the one thing they can act on.
	if !strings.Contains(err.Error(), "not in a pending state") {
		t.Errorf("refusal lost: %v", err)
	}
}

func TestBearerIsCachedAcrossCalls(t *testing.T) {
	c, seen := gatewayStub(t, func() (string, int) {
		return rpcReply(t, liveExceptionPayload), 200
	})
	for i := 0; i < 3; i++ {
		if _, err := c.ListExceptions(context.Background(), ExceptionQuery{Tab: TabNeedAction}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if seen.mints != 1 {
		t.Errorf("minted %d tokens for 3 calls; Keycloak counts these", seen.mints)
	}
}

func TestRefusedCredentialSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"Invalid client credentials"}`))
	}))
	defer srv.Close()

	c := NewUCP(UCPConfig{ClientID: "a", ClientSecret: "wrong", Realm: "r",
		HQClientID: "h", GatewayURL: srv.URL + "/mcp", AuthURL: srv.URL})
	_, err := c.ListExceptions(context.Background(), ExceptionQuery{Tab: TabNeedAction})
	if err == nil || !strings.Contains(err.Error(), "Invalid client credentials") {
		t.Errorf("a wrong secret should say so plainly: %v", err)
	}
}

func TestTabValid(t *testing.T) {
	for _, ok := range []Tab{TabNeedAction, TabAutoReattempt, TabReturnRecommendation, TabClosed} {
		if !ok.Valid() {
			t.Errorf("%s should be valid", ok)
		}
	}
	if Tab("RETURN").Valid() || Tab("").Valid() {
		t.Error("an unknown tab must not be sent to Delhivery as a filter")
	}
}
