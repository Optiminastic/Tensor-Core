package email

import (
	"encoding/base64"
	"net/smtp"
	"strings"
	"testing"
)

// capture swaps the transport so a test can read the exact bytes that would
// have gone to the server.
func capture(t *testing.T, cfg Config) (*Client, *struct {
	From string
	To   []string
	Body string
}) {
	t.Helper()
	c := New(cfg)
	if c == nil {
		t.Fatal("New returned nil for a complete config")
	}
	got := &struct {
		From string
		To   []string
		Body string
	}{}
	c.send = func(_ string, _ smtp.Auth, from string, to []string, msg []byte) error {
		got.From, got.To, got.Body = from, to, string(msg)
		return nil
	}
	return c, got
}

func goodConfig() Config {
	return Config{
		Host: "smtp.example.com", Port: 587,
		Username: "notification@example.com", Password: "secret",
		From: "notification@example.com", FromName: "Tensor",
	}
}

func TestNewNeedsEveryPart(t *testing.T) {
	if New(goodConfig()) == nil {
		t.Fatal("a complete config should build a client")
	}
	// A half-configured client that sends as somebody else, or silently fails
	// to authenticate, is worse than one that says no.
	for _, drop := range []string{"Host", "Port", "Username", "Password", "From"} {
		cfg := goodConfig()
		switch drop {
		case "Host":
			cfg.Host = "  "
		case "Port":
			cfg.Port = 0
		case "Username":
			cfg.Username = ""
		case "Password":
			cfg.Password = ""
		case "From":
			cfg.From = ""
		}
		if New(cfg) != nil {
			t.Errorf("missing %s should give no client", drop)
		}
	}
}

func TestBccIsInTheEnvelopeButNotTheHeaders(t *testing.T) {
	// THE ENTIRE POINT OF BCC. A Bcc header would show every blind recipient
	// to every other recipient - on a report that goes to an accountant and
	// quietly to a director, that is the one mistake that cannot be recalled.
	c, got := capture(t, goodConfig())
	err := c.Send(Message{
		To:      []string{"to@example.com"},
		CC:      []string{"cc@example.com"},
		BCC:     []string{"hidden@example.com"},
		Subject: "Sales register",
		Body:    "Attached.",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// All three must actually be delivered to.
	for _, want := range []string{"to@example.com", "cc@example.com", "hidden@example.com"} {
		found := false
		for _, a := range got.To {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not in the envelope, so it would never arrive", want)
		}
	}

	headers := got.Body
	if i := strings.Index(headers, "\r\n\r\n"); i > 0 {
		headers = headers[:i]
	}
	if strings.Contains(strings.ToLower(headers), "bcc:") {
		t.Error("a Bcc header was written; every recipient would see the blind ones")
	}
	if strings.Contains(headers, "hidden@example.com") {
		t.Error("the blind recipient appears in the headers")
	}
	if !strings.Contains(headers, "Cc: cc@example.com") {
		t.Error("Cc should be visible - that is what distinguishes it from Bcc")
	}
}

func TestAttachmentIsBase64AndWrapped(t *testing.T) {
	// A single enormous base64 line is rejected or mangled by some servers,
	// and a spreadsheet is easily big enough to hit it.
	content := []byte(strings.Repeat("spreadsheet bytes ", 500))
	c, got := capture(t, goodConfig())
	err := c.Send(Message{
		To: []string{"to@example.com"}, Subject: "s", Body: "b",
		Attachments: []Attachment{{
			Filename:    "sales-register-2026-10-28.xlsx",
			ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			Content:     content,
		}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(got.Body, `filename="sales-register-2026-10-28.xlsx"`) {
		t.Error("the attachment filename is missing")
	}
	if !strings.Contains(got.Body, "Content-Transfer-Encoding: base64") {
		t.Error("the attachment is not base64 encoded")
	}
	for _, line := range strings.Split(got.Body, "\r\n") {
		if len(line) > 998 {
			t.Fatalf("a %d-character line was written; SMTP caps lines well below that", len(line))
		}
	}
	// And it must decode back to exactly what went in.
	start := strings.Index(got.Body, "base64\r\n")
	chunk := got.Body[start:]
	chunk = chunk[strings.Index(chunk, "\r\n\r\n")+4:]
	chunk = chunk[:strings.Index(chunk, "\r\n--")]
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(chunk, "\r\n", ""))
	if err != nil {
		t.Fatalf("attachment does not decode: %v", err)
	}
	if string(decoded) != string(content) {
		t.Error("the attachment does not survive the round trip")
	}
}

func TestSendRefusesWithNoRecipients(t *testing.T) {
	// SMTP accepts a message addressed to nobody and discards it, which looks
	// exactly like a successful send.
	c, _ := capture(t, goodConfig())
	err := c.Send(Message{Subject: "s", Body: "b"})
	if err == nil {
		t.Fatal("a message with no recipients must be refused, not sent into the void")
	}
	if !strings.Contains(err.Error(), "no recipients") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestBlankRecipientsAreDropped(t *testing.T) {
	// The recipient list comes from a text box somebody types into, so a
	// trailing comma is normal input rather than a bug to reject.
	c, got := capture(t, goodConfig())
	err := c.Send(Message{
		To:      []string{"to@example.com", "   ", ""},
		Subject: "s", Body: "b",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(got.To) != 1 {
		t.Errorf("envelope has %d recipients, want the one real address", len(got.To))
	}
}

func TestFromNameIsEncodedForNonAscii(t *testing.T) {
	cfg := goodConfig()
	cfg.FromName = "Tensor — Reports"
	c, got := capture(t, cfg)
	if err := c.Send(Message{To: []string{"a@b.com"}, Subject: "s", Body: "b"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// A raw em dash in a header is not legal; it must be encoded.
	if strings.Contains(got.Body, "Tensor — Reports") {
		t.Error("a non-ASCII display name went out unencoded")
	}
	if !strings.Contains(got.Body, "=?utf-8?") {
		t.Error("the display name was not MIME-encoded")
	}
}
