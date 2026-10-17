package whatsapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// capture serves Meta's success answer and hands back the request body.
func capture(t *testing.T) (*Client, *map[string]any) {
	t.Helper()
	got := map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("request is not JSON: %s", raw)
		}
		_, _ = io.WriteString(w, `{"messages":[{"id":"wamid.OUT"}]}`)
	}))
	t.Cleanup(server.Close)
	return testClient(server), &got
}

func TestSendTextIsAFreeFormMessage(t *testing.T) {
	c, got := capture(t)
	res, err := c.SendText(context.Background(), "+919876543210", "hello")
	if err != nil || res.MessageID != "wamid.OUT" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	body := *got
	if body["type"] != "text" || body["to"] != "919876543210" || body["messaging_product"] != "whatsapp" {
		t.Fatalf("body = %v", body)
	}
	if body["text"].(map[string]any)["body"] != "hello" {
		t.Fatalf("text = %v", body["text"])
	}
}

func TestSendTextRefusesAnEmptyMessage(t *testing.T) {
	c, _ := capture(t)
	if _, err := c.SendText(context.Background(), "919876543210", "  "); err == nil {
		t.Fatal("an empty text must be refused before the network")
	}
}

func TestSendButtonsShapesReplyButtons(t *testing.T) {
	c, got := capture(t)
	buttons := []Button{{ID: "track", Title: "Where is my order"}, {ID: "more", Title: "More"}}
	if _, err := c.SendButtons(context.Background(), "919876543210", "How can we help?", buttons); err != nil {
		t.Fatal(err)
	}
	interactive := (*got)["interactive"].(map[string]any)
	sent := interactive["action"].(map[string]any)["buttons"].([]any)
	first := sent[0].(map[string]any)
	reply := first["reply"].(map[string]any)
	if interactive["type"] != "button" || len(sent) != 2 || first["type"] != "reply" || reply["id"] != "track" {
		t.Fatalf("interactive = %v", interactive)
	}
}

func TestSendButtonsRefusesWhatMetaWould(t *testing.T) {
	c, _ := capture(t)
	four := []Button{{ID: "a", Title: "a"}, {ID: "b", Title: "b"}, {ID: "c", Title: "c"}, {ID: "d", Title: "d"}}
	if _, err := c.SendButtons(context.Background(), "91", "x", four); err == nil {
		t.Error("four buttons must be refused before the network")
	}
	long := []Button{{ID: "a", Title: "a title well over twenty characters"}}
	if _, err := c.SendButtons(context.Background(), "91", "x", long); err == nil {
		t.Error("a title over 20 characters must be refused before the network")
	}
}

func TestQuickReplyPayloadsBecomeButtonComponents(t *testing.T) {
	c, got := capture(t)
	_, err := c.SendTemplate(context.Background(), TemplateMessage{
		To: "919876543210", Name: "order_personalisation_confirm", Language: "en",
		BodyParams:         []string{"Asha", "T3DPS-1", "Plank: RAHUL"},
		QuickReplyPayloads: []string{"confirm:1", "change:1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	components := (*got)["template"].(map[string]any)["components"].([]any)
	if len(components) != 3 {
		t.Fatalf("components = %v", components)
	}
	change := components[2].(map[string]any)
	param := change["parameters"].([]any)[0].(map[string]any)
	if change["sub_type"] != "quick_reply" || change["index"] != "1" || param["type"] != "payload" || param["payload"] != "change:1" {
		t.Fatalf("quick reply component = %v", change)
	}
}

func TestSendCTAURLShapesTheLinkButton(t *testing.T) {
	c, got := capture(t)
	if _, err := c.SendCTAURL(context.Background(), "+919876543210",
		"Open the damage form", "LINK", "https://tensor.example/d/abc"); err != nil {
		t.Fatal(err)
	}
	body := *got
	if body["to"] != "919876543210" || body["type"] != "interactive" {
		t.Fatalf("body = %v", body)
	}
	interactive := body["interactive"].(map[string]any)
	action := interactive["action"].(map[string]any)
	params := action["parameters"].(map[string]any)
	if interactive["type"] != "cta_url" || action["name"] != "cta_url" {
		t.Fatalf("interactive = %v", interactive)
	}
	if params["display_text"] != "LINK" || params["url"] != "https://tensor.example/d/abc" {
		t.Fatalf("parameters = %v", params)
	}
	if interactive["body"].(map[string]any)["text"] != "Open the damage form" {
		t.Fatalf("body text = %v", interactive["body"])
	}
}

// Refused here rather than by Meta, which answers a generic 100 and names
// neither the field nor the rule.
func TestSendCTAURLRefusesWhatMetaWould(t *testing.T) {
	c, _ := capture(t)
	cases := []struct {
		name, body, label, url string
	}{
		{"no body", " ", "LINK", "https://x.test/d/a"},
		{"no label", "hello", "", "https://x.test/d/a"},
		{"a label over 20 characters", "hello", "Open the damage form now", "https://x.test/d/a"},
		{"a relative address", "hello", "LINK", "/d/a"},
	}
	for _, tc := range cases {
		if _, err := c.SendCTAURL(context.Background(), "919876543210", tc.body, tc.label, tc.url); err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}
