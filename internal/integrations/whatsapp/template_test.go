package whatsapp

// What the log will say a customer was sent.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const approvedBody = "Hi {{1}}, your {{2}} ({{3}} item) is still in your cart — ₹{{4}}. " +
	"Use {{5}} for 10% off."

func templateServer(t *testing.T, payload string) (*Client, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.Contains(r.URL.Path, "/message_templates") {
			t.Errorf("asked for %s, want the WABA's templates", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)

	client := New(Config{
		AccessToken: "t", PhoneNumberID: "1", WABAID: "9",
		TemplateName: "cart_recovery_personalised", TemplateLanguage: "en",
		BaseURL: srv.URL,
	})
	if client == nil {
		t.Fatal("client should be configured")
	}
	return client, &calls
}

func TestTheLogQuotesTheApprovedCopy(t *testing.T) {
	client, _ := templateServer(t, `{"data":[{"name":"cart_recovery_personalised",
		"language":"en","status":"APPROVED","components":[
		{"type":"BODY","text":"`+approvedBody+`"},
		{"type":"BUTTONS","buttons":[
			{"type":"URL","url":"https://the3dprintingstore.in/{{1}}"},
			{"type":"URL","url":"https://the3dprintingstore.in/{{1}}"}]}]}]}`)

	tmpl, err := client.Template(context.Background(), "", "")
	if err != nil {
		t.Fatalf("Template: %v", err)
	}
	if tmpl.Body != approvedBody {
		t.Errorf("body = %q", tmpl.Body)
	}
	// One entry per button, because a parameter lands on a button by POSITION
	// and a collapsed list would send the cart link to the wrong one.
	if len(tmpl.ButtonURLs) != 2 {
		t.Errorf("button urls = %v, want one per URL button", tmpl.ButtonURLs)
	}
}

func TestTheWrongLanguageIsADifferentTemplate(t *testing.T) {
	// "en" and "en_US" are separate templates to Meta, and the endpoint
	// returns every translation under one name. Picking the first would show
	// a log in a language the customer was never sent.
	client, _ := templateServer(t, `{"data":[
		{"name":"cart_recovery_personalised","language":"en_US","status":"APPROVED",
		 "components":[{"type":"BODY","text":"American copy"}]}]}`)

	if _, err := client.Template(context.Background(), "", "en"); err == nil {
		t.Fatal("en_US satisfied a request for en")
	}
}

func TestTheCopyIsReadOnceAndRemembered(t *testing.T) {
	// A send must not pay a second round trip to Meta just to write its own
	// log line.
	client, calls := templateServer(t, `{"data":[{"name":"cart_recovery_personalised",
		"language":"en","components":[{"type":"BODY","text":"`+approvedBody+`"}]}]}`)

	for range 3 {
		if _, err := client.Template(context.Background(), "", ""); err != nil {
			t.Fatalf("Template: %v", err)
		}
	}
	if *calls != 1 {
		t.Errorf("fetched %d times, want 1", *calls)
	}
}

func TestTemplatesCannotBeReadWithoutTheBusinessAccount(t *testing.T) {
	// The WABA owns the templates; the phone number id answers 404 - the
	// mirror of the trap on /messages. Better to say so than to let Meta.
	client := New(Config{AccessToken: "t", PhoneNumberID: "1",
		TemplateName: "x", TemplateLanguage: "en"})
	_, err := client.Template(context.Background(), "", "")
	if err == nil || !strings.Contains(err.Error(), "business account") {
		t.Fatalf("err = %v, want it to name the missing WABA id", err)
	}
}

func TestFillPutsTheCustomersOwnValuesIn(t *testing.T) {
	got := Fill(approvedBody, []string{"Rishi Patel", "Dual Name Plank", "1", "948", "RISHI10"})
	want := "Hi Rishi Patel, your Dual Name Plank (1 item) is still in your cart — ₹948. " +
		"Use RISHI10 for 10% off."
	if got != want {
		t.Errorf("filled = %q", got)
	}
}

func TestAPlaceholderWithNoValueIsLeftStanding(t *testing.T) {
	// This renders a record of something already sent. Blanking a placeholder
	// the log cannot account for would read as though the customer saw a gap;
	// leaving {{5}} says plainly that the log and the send disagree.
	got := Fill("Hi {{1}}, use {{5}}", []string{"Rishi"})
	if got != "Hi Rishi, use {{5}}" {
		t.Errorf("filled = %q", got)
	}
}

func TestATemplateWithNoBodyRendersToNothing(t *testing.T) {
	if got := Fill("", []string{"Rishi"}); got != "" {
		t.Errorf("filled = %q, want empty", got)
	}
}
