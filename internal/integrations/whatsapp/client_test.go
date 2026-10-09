package whatsapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(server *httptest.Server) *Client {
	return New(Config{
		AccessToken: "SYSTEM-USER-TOKEN", PhoneNumberID: "3761254503933348",
		TemplateName: "cart_recovery_checkout", TemplateLanguage: "en",
		BaseURL: server.URL, APIVersion: "v25.0",
	})
}

const acceptedBody = `{"messaging_product":"whatsapp",
 "contacts":[{"input":"919799931864","wa_id":"919799931864"}],
 "messages":[{"id":"wamid.TEST","message_status":"accepted"}]}`

func TestNewIsNilWithoutATokenOrANumber(t *testing.T) {
	for _, cfg := range []Config{
		{PhoneNumberID: "123"},
		{AccessToken: "t"},
		{AccessToken: "  ", PhoneNumberID: "  "},
	} {
		if New(cfg).Configured() {
			t.Errorf("%+v should not count as configured", cfg)
		}
	}
	var nilClient *Client
	if nilClient.Configured() {
		t.Error("Configured must be safe on a nil client")
	}
	// The template is NOT required to construct a client - a caller may name
	// one per message.
	if !New(Config{AccessToken: "t", PhoneNumberID: "1"}).Configured() {
		t.Error("a token and a number are enough to send")
	}
}

// The exact JSON shape Meta validates. Two details here are easy to get wrong
// and are refused with an unhelpful generic error: the button's index is the
// STRING "0", and it needs sub_type "url".
func TestTheRequestIsTheShapeMetaValidates(t *testing.T) {
	var got map[string]any
	var path string
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(acceptedBody))
	}))
	defer server.Close()

	result, err := testClient(server).SendTemplate(context.Background(), TemplateMessage{
		// With a '+', to prove it is stripped.
		To:          "+919799931864",
		BodyParams:  []string{"Tushar", "Dual Name Plank", "1", "948", "BACK10"},
		ButtonParam: "discount/BACK10?redirect=%2Fcheckouts%2Fac%2FTOKEN%2Frecover%3Fkey%3DKEY",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	if want := "/v25.0/3761254503933348/messages"; path != want {
		t.Errorf("posted to %q, want %q (the NUMBER's id, not the WABA's)", path, want)
	}
	if auth != "Bearer SYSTEM-USER-TOKEN" {
		t.Errorf("auth header %q", auth)
	}
	if got["messaging_product"] != "whatsapp" {
		t.Errorf("messaging_product %v", got["messaging_product"])
	}
	if got["to"] != "919799931864" {
		t.Errorf("to = %v, want the plus stripped", got["to"])
	}

	tpl, _ := got["template"].(map[string]any)
	if tpl["name"] != "cart_recovery_checkout" {
		t.Errorf("template name %v", tpl["name"])
	}
	if lang, _ := tpl["language"].(map[string]any); lang["code"] != "en" {
		t.Errorf("language %v, want en - en_US is a different template", lang)
	}

	comps, _ := tpl["components"].([]any)
	if len(comps) != 2 {
		t.Fatalf("got %d components, want body + button", len(comps))
	}
	body, _ := comps[0].(map[string]any)
	params, _ := body["parameters"].([]any)
	if len(params) != 5 {
		t.Fatalf("got %d body params, want 5", len(params))
	}
	for i, want := range []string{"Tushar", "Dual Name Plank", "1", "948", "BACK10"} {
		p, _ := params[i].(map[string]any)
		if p["text"] != want {
			t.Errorf("body param %d = %v, want %q - order is the template's", i+1, p["text"], want)
		}
	}

	btn, _ := comps[1].(map[string]any)
	if btn["type"] != "button" || btn["sub_type"] != "url" {
		t.Errorf("button component %v, want type=button sub_type=url", btn)
	}
	if btn["index"] != "0" {
		t.Errorf("button index %#v, want the STRING \"0\" - a number is refused", btn["index"])
	}

	if result.MessageID != "wamid.TEST" || result.WAID != "919799931864" {
		t.Errorf("result %+v", result)
	}
}

// A template with no button must not get an empty button component.
func TestNoButtonParamMeansNoButtonComponent(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(acceptedBody))
	}))
	defer server.Close()

	if _, err := testClient(server).SendTemplate(context.Background(), TemplateMessage{
		To: "919799931864", BodyParams: []string{"a", "b", "c", "d", "e"},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	tpl, _ := got["template"].(map[string]any)
	if comps, _ := tpl["components"].([]any); len(comps) != 1 {
		t.Errorf("got %d components, want body only", len(comps))
	}
}

// Meta refuses an empty parameter and names neither the field nor the
// position. A blank customer name is common on an abandoned checkout, so this
// is caught here rather than becoming a recurring mystery.
func TestAnEmptyParameterIsRefusedBeforeTheNetwork(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requests, 1)
		_, _ = w.Write([]byte(acceptedBody))
	}))
	defer server.Close()

	_, err := testClient(server).SendTemplate(context.Background(), TemplateMessage{
		To: "919799931864", BodyParams: []string{"Tushar", "", "1", "948", "BACK10"},
	})
	if err == nil {
		t.Fatal("an empty parameter must be refused")
	}
	if !strings.Contains(err.Error(), "parameter 2") {
		t.Errorf("error should name which parameter: %v", err)
	}
	if requests != 0 {
		t.Errorf("%d requests reached Meta; it should have failed locally", requests)
	}
}

// Each of these was either hit while building this or is a documented failure
// of exactly this call. The test is that the sentence names the fix, because
// the raw code does not.
func TestMetaErrorsNameTheFix(t *testing.T) {
	cases := []struct {
		code      int
		status    int
		contains  string
		retryable bool
	}{
		{190, 401, "System User token", false},
		{131037, 400, "display name is not approved", false},
		{132001, 404, "different templates", false},
		{132000, 400, "number of parameters", false},
		{132005, 400, "newline", false},
		{132015, 400, "paused or disabled", false},
		{131026, 400, "cannot receive", false},
		{133010, 400, "not registered", false},
		{130429, 429, "rate limiting", true},
		{131056, 429, "throttling the pair", true},
		{100, 400, "index must be the string", false},
	}
	for _, tc := range cases {
		body := `{"error":{"message":"(#` + itoa(tc.code) + `) something",` +
			`"code":` + itoa(tc.code) + `,"fbtrace_id":"ABC",` +
			`"error_data":{"details":"the detail"}}}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(body))
		}))

		_, err := testClient(server).SendTemplate(context.Background(), TemplateMessage{
			To: "919799931864", BodyParams: []string{"a", "b", "c", "d", "e"},
		})
		server.Close()

		if err == nil {
			t.Errorf("code %d: expected an error", tc.code)
			continue
		}
		if !strings.Contains(err.Error(), tc.contains) {
			t.Errorf("code %d: %q does not mention %q", tc.code, err.Error(), tc.contains)
		}
		var metaErr *Error
		if !asMetaError(err, &metaErr) {
			t.Errorf("code %d: not a *whatsapp.Error", tc.code)
			continue
		}
		if metaErr.Retryable() != tc.retryable {
			t.Errorf("code %d: Retryable()=%v, want %v", tc.code, metaErr.Retryable(), tc.retryable)
		}
		if metaErr.Permanent() == tc.retryable {
			t.Errorf("code %d: Permanent() and Retryable() must disagree", tc.code)
		}
		if metaErr.TraceID != "ABC" {
			t.Errorf("code %d: lost the fbtrace_id, which is what Meta support asks for", tc.code)
		}
	}
}

// A refusal whose body does not parse must still be an error carrying the
// status, so a caller never loses the fact that something was refused.
func TestAnUnparseableRefusalIsStillAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte("<html>gateway</html>"))
	}))
	defer server.Close()

	_, err := testClient(server).SendTemplate(context.Background(), TemplateMessage{
		To: "919799931864", BodyParams: []string{"a", "b", "c", "d", "e"},
	})
	if err == nil {
		t.Fatal("a 502 must be an error")
	}
	var metaErr *Error
	if !asMetaError(err, &metaErr) || metaErr.Status != 502 {
		t.Errorf("lost the status: %v", err)
	}
	if !metaErr.Retryable() {
		t.Error("a 502 is temporary and should be retryable")
	}
}

// A send is not idempotent and Meta accepts no idempotency key, so a retry
// after a timeout can message somebody twice. This client must never retry.
func TestATransportFailureIsNeverRetried(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requests, 1)
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte(acceptedBody))
	}))
	defer server.Close()

	client := testClient(server)
	client.http.Timeout = 30 * time.Millisecond

	if _, err := client.SendTemplate(context.Background(), TemplateMessage{
		To: "919799931864", BodyParams: []string{"a", "b", "c", "d", "e"},
	}); err == nil {
		t.Fatal("expected a timeout")
	}
	time.Sleep(200 * time.Millisecond)
	if n := atomic.LoadInt32(&requests); n != 1 {
		t.Errorf("%d requests sent, want exactly 1 - a retry here messages somebody twice", n)
	}
}

// A 2xx with no message id is not a send, and must not be recorded as one.
func TestAcceptedWithoutAMessageIDIsAFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"messaging_product":"whatsapp","messages":[]}`))
	}))
	defer server.Close()

	if _, err := testClient(server).SendTemplate(context.Background(), TemplateMessage{
		To: "919799931864", BodyParams: []string{"a", "b", "c", "d", "e"},
	}); err == nil {
		t.Fatal("no message id means no send")
	}
}

// Nothing in this package may claim delivery. Meta's word is "accepted", and
// without the webhook that is the strongest claim the data supports.
func TestAnAcceptedMessageIsNotADeliveredOne(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(acceptedBody))
	}))
	defer server.Close()

	result, err := testClient(server).SendTemplate(context.Background(), TemplateMessage{
		To: "919799931864", BodyParams: []string{"a", "b", "c", "d", "e"},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result.Status != "accepted" {
		t.Errorf("status %q, want Meta's own word", result.Status)
	}
	if strings.Contains(strings.ToLower(result.Status), "deliver") {
		t.Error("nothing here may say delivered")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func asMetaError(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
