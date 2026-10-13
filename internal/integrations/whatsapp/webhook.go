package whatsapp

// The receiving side: what Meta POSTs to Tensor's webhook.
//
// ONE WEBHOOK, MANY NUMBERS. Meta delivers every number on every business
// account the app is subscribed to through the same callback, so the payload is
// grouped by phone_number_id - the only field that says which brand a message
// belongs to. The signature can only be checked once that brand is known,
// because each brand's app secret is its own.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const signaturePrefix = "sha256="

// Change fields acted on. smb_message_echoes carries what staff send from the
// WhatsApp Business app on a number in coexistence mode.
const (
	fieldMessages = "messages"
	fieldEchoes   = "smb_message_echoes"
)

// Inbound message kinds. Media keep Meta's own type name (image, video, ...).
const (
	KindText        = "text"
	KindButton      = "button"
	typeInteractive = "interactive"
)

// StatusFailed is the delivery status Meta reports for an undeliverable send.
const StatusFailed = "failed"

// Inbound is one customer message. ButtonID is set for both a list or button
// tap (the row id) and a template quick-reply tap (the payload we attached).
type Inbound struct {
	ID          string
	From        string
	ProfileName string
	Timestamp   time.Time
	Kind        string
	Text        string
	ButtonID    string
}

// Echo is a message staff sent from the WhatsApp Business app.
type Echo struct {
	ID        string
	To        string
	Timestamp time.Time
	Kind      string
	Text      string
}

// Status is a delivery update for a message Tensor sent.
type Status struct {
	ID          string
	Status      string
	RecipientID string
	ErrorCode   int
	Timestamp   time.Time
}

// Change is everything one webhook delivered for one business number.
type Change struct {
	Messages []Inbound
	Echoes   []Echo
	Statuses []Status
}

// VerifySignature checks Meta's X-Hub-Signature-256 header against the raw body.
func VerifySignature(appSecret string, body []byte, header string) bool {
	if appSecret == "" || !strings.HasPrefix(header, signaturePrefix) {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, signaturePrefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// ParseWebhook groups a webhook body by the phone_number_id it was sent to.
func ParseWebhook(body []byte) (map[string]Change, error) {
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("whatsapp webhook: %w", err)
	}
	out := map[string]Change{}
	for _, entry := range p.Entry {
		for _, ch := range entry.Changes {
			id := ch.Value.Metadata.PhoneNumberID
			if id == "" {
				continue
			}
			c := out[id]
			switch ch.Field {
			case fieldMessages:
				names := profileNames(ch.Value.Contacts)
				for _, m := range ch.Value.Messages {
					in := m.toInbound()
					in.ProfileName = names[in.From]
					c.Messages = append(c.Messages, in)
				}
				for _, s := range ch.Value.Statuses {
					c.Statuses = append(c.Statuses, s.toStatus())
				}
			case fieldEchoes:
				for _, e := range ch.Value.MessageEchoes {
					c.Echoes = append(c.Echoes, Echo{
						ID: e.ID, To: e.To, Timestamp: unix(e.Timestamp), Kind: e.Type, Text: e.Text.Body,
					})
				}
			}
			out[id] = c
		}
	}
	return out, nil
}

type payload struct {
	Entry []struct {
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				Metadata struct {
					PhoneNumberID string `json:"phone_number_id"`
				} `json:"metadata"`
				Contacts []struct {
					WAID    string `json:"wa_id"`
					Profile struct {
						Name string `json:"name"`
					} `json:"profile"`
				} `json:"contacts"`
				Messages      []rawMessage `json:"messages"`
				Statuses      []rawStatus  `json:"statuses"`
				MessageEchoes []rawMessage `json:"message_echoes"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

func profileNames(contacts []struct {
	WAID    string `json:"wa_id"`
	Profile struct {
		Name string `json:"name"`
	} `json:"profile"`
}) map[string]string {
	names := make(map[string]string, len(contacts))
	for _, c := range contacts {
		names[c.WAID] = c.Profile.Name
	}
	return names
}

type rawMessage struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Text      struct {
		Body string `json:"body"`
	} `json:"text"`
	Button struct {
		Payload string `json:"payload"`
	} `json:"button"`
	Interactive struct {
		ButtonReply struct {
			ID string `json:"id"`
		} `json:"button_reply"`
		ListReply struct {
			ID string `json:"id"`
		} `json:"list_reply"`
	} `json:"interactive"`
}

func (m rawMessage) toInbound() Inbound {
	in := Inbound{ID: m.ID, From: m.From, Timestamp: unix(m.Timestamp), Kind: m.Type}
	switch m.Type {
	case KindText:
		in.Text = m.Text.Body
	case KindButton:
		in.ButtonID = m.Button.Payload
	case typeInteractive:
		in.Kind = KindButton
		in.ButtonID = m.Interactive.ButtonReply.ID
		if in.ButtonID == "" {
			in.ButtonID = m.Interactive.ListReply.ID
		}
	}
	return in
}

type rawStatus struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Timestamp   string `json:"timestamp"`
	RecipientID string `json:"recipient_id"`
	Errors      []struct {
		Code int `json:"code"`
	} `json:"errors"`
}

func (s rawStatus) toStatus() Status {
	out := Status{ID: s.ID, Status: s.Status, RecipientID: s.RecipientID, Timestamp: unix(s.Timestamp)}
	if len(s.Errors) > 0 {
		out.ErrorCode = s.Errors[0].Code
	}
	return out
}

func unix(s string) time.Time {
	secs, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(secs, 0)
}
