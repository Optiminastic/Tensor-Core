package whatsapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
	"time"
)

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"a":1}`)
	if !VerifySignature("s3cret", body, sign("s3cret", body)) {
		t.Error("a valid signature was rejected")
	}
	for _, bad := range []string{"", "sha256=", "sha256=00", sign("other", body), "md5=abc"} {
		if VerifySignature("s3cret", body, bad) {
			t.Errorf("bad signature %q accepted", bad)
		}
	}
	if VerifySignature("", body, sign("", body)) {
		t.Error("an empty secret must never verify")
	}
}

func TestParseWebhookGroupsByNumber(t *testing.T) {
	body, err := os.ReadFile("testdata/webhook.json")
	if err != nil {
		t.Fatal(err)
	}
	changes, err := ParseWebhook(body)
	if err != nil {
		t.Fatal(err)
	}
	ours := changes["PNID"]
	if len(changes) != 2 || len(changes["OTHER"].Messages) != 1 {
		t.Fatalf("changes = %+v", changes)
	}

	want := []Inbound{
		{ID: "wamid.text", From: "919876543210", Kind: KindText, Text: "hi", ProfileName: "Asha"},
		{ID: "wamid.reply", From: "919876543210", Kind: KindButton, ButtonID: "track", ProfileName: "Asha"},
		{ID: "wamid.tpl", From: "919876543210", Kind: KindButton, ButtonID: "confirm:gid://shopify/Order/7", ProfileName: "Asha"},
		{ID: "wamid.img", From: "919876543210", Kind: "image", ProfileName: "Asha"},
	}
	if len(ours.Messages) != len(want) {
		t.Fatalf("messages = %+v", ours.Messages)
	}
	for i, w := range want {
		g := ours.Messages[i]
		g.Timestamp = time.Time{}
		if g != w {
			t.Errorf("message %d = %+v, want %+v", i, g, w)
		}
	}
	if !ours.Messages[0].Timestamp.Equal(time.Unix(1760335200, 0)) {
		t.Errorf("timestamp = %v", ours.Messages[0].Timestamp)
	}
	if len(ours.Statuses) != 1 || ours.Statuses[0].ErrorCode != 131026 || ours.Statuses[0].Status != StatusFailed {
		t.Fatalf("statuses = %+v", ours.Statuses)
	}
	if len(ours.Echoes) != 1 || ours.Echoes[0].To != "919876543210" || ours.Echoes[0].Text != "Hello from the store team" {
		t.Fatalf("echoes = %+v", ours.Echoes)
	}
}

func TestParseWebhookRefusesGarbage(t *testing.T) {
	if _, err := ParseWebhook([]byte("not json")); err == nil {
		t.Error("expected an error")
	}
}
