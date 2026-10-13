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

func TestSendListShapesAListMessage(t *testing.T) {
	c, got := capture(t)
	rows := []ListRow{{ID: "track", Title: "Where is my order", Description: "Order status"}, {ID: "more", Title: "More"}}
	if _, err := c.SendList(context.Background(), "919876543210", "Pick one", "Choose an option", rows); err != nil {
		t.Fatal(err)
	}
	interactive := (*got)["interactive"].(map[string]any)
	action := interactive["action"].(map[string]any)
	sent := action["sections"].([]any)[0].(map[string]any)["rows"].([]any)
	if interactive["type"] != "list" || action["button"] != "Choose an option" || len(sent) != 2 {
		t.Fatalf("interactive = %v", interactive)
	}
	if _, has := sent[1].(map[string]any)["description"]; has {
		t.Fatalf("an empty description must be left out: %v", sent[1])
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
