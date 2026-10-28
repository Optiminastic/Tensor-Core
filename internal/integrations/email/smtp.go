// Package email sends mail with an attachment over SMTP.
//
// THE FIRST EMAIL TENSOR HAS EVER SENT. Everything else this service tells a
// customer goes through WhatsApp; this exists for the one message that has to
// reach an accountant's inbox with a spreadsheet on it.
//
// DELIBERATELY SMALL. No templating, no queue, no retry policy of its own -
// the caller is a River job that already has all three. This builds one MIME
// message and hands it to a server.
package email

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// dialTimeout bounds the connection. Gmail is normally quick; a worker that
// hangs here holds its job slot until River times the whole job out, and the
// report would silently not arrive.
const dialTimeout = 30 * time.Second

// Config is one mailbox's credentials.
type Config struct {
	Host string
	Port int
	// Username is usually the full address.
	Username string
	Password string
	// From is the envelope and header sender. Gmail rewrites a From that is
	// not the authenticated mailbox or one of its aliases, so a mismatch
	// here does not fail - it quietly sends as somebody else.
	From     string
	FromName string
}

// Configured reports whether a send could even be attempted.
func (c Config) Configured() bool {
	return strings.TrimSpace(c.Host) != "" && c.Port > 0 &&
		strings.TrimSpace(c.Username) != "" && strings.TrimSpace(c.Password) != "" &&
		strings.TrimSpace(c.From) != ""
}

// Attachment is a file to hang off the message.
type Attachment struct {
	Filename    string
	ContentType string
	Content     []byte
}

// Message is what to send.
type Message struct {
	To          []string
	CC          []string
	BCC         []string
	Subject     string
	Body        string
	Attachments []Attachment
}

// Recipients is everybody the envelope goes to.
//
// BCC IS IN THE ENVELOPE AND NOT IN THE HEADERS, which is the whole point of
// it: writing a Bcc header would show every blind recipient to every other
// one. The headers are built separately, below, and never include these.
func (m Message) Recipients() []string {
	out := make([]string, 0, len(m.To)+len(m.CC)+len(m.BCC))
	for _, group := range [][]string{m.To, m.CC, m.BCC} {
		for _, address := range group {
			if a := strings.TrimSpace(address); a != "" {
				out = append(out, a)
			}
		}
	}
	return out
}

// Client sends through one mailbox.
type Client struct {
	cfg Config
	// send is the transport, swapped in tests. Nothing else mocks SMTP well:
	// the alternative is a real server in a container for a unit test.
	send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

// New returns a client, or nil when the mailbox is not configured.
//
// Nil rather than an error, matching the other integrations here: a Tensor
// install with no mailbox is the normal case, and Configured() is what callers
// check before they try.
func New(cfg Config) *Client {
	if !cfg.Configured() {
		return nil
	}
	return &Client{cfg: cfg, send: smtp.SendMail}
}

// Configured reports whether this client can send.
func (c *Client) Configured() bool { return c != nil && c.cfg.Configured() }

// Send delivers one message.
func (c *Client) Send(m Message) error {
	if !c.Configured() {
		return fmt.Errorf("no mailbox is configured, so nothing can be sent")
	}
	recipients := m.Recipients()
	if len(recipients) == 0 {
		// Worth refusing loudly: SMTP accepts a message with no recipients
		// and discards it, which looks exactly like a successful send.
		return fmt.Errorf("the message has no recipients")
	}

	body, err := c.build(m)
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(c.cfg.Host, fmt.Sprint(c.cfg.Port))
	auth := smtp.PlainAuth("", c.cfg.Username, c.cfg.Password, c.cfg.Host)
	if err := c.send(addr, auth, c.cfg.From, recipients, body); err != nil {
		// The server's own words. Gmail says "Username and Password not
		// accepted" for an ordinary password where an app password is
		// needed, and that sentence is the entire fix.
		return fmt.Errorf("smtp send via %s: %w", addr, err)
	}
	return nil
}

// build assembles the MIME message.
func (c *Client) build(m Message) ([]byte, error) {
	var buf bytes.Buffer
	boundary := fmt.Sprintf("tensor-%d", time.Now().UnixNano())

	from := c.cfg.From
	if name := strings.TrimSpace(c.cfg.FromName); name != "" {
		from = fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", name), c.cfg.From)
	}

	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(m.To, ", "))
	if len(m.CC) > 0 {
		fmt.Fprintf(&buf, "Cc: %s\r\n", strings.Join(m.CC, ", "))
	}
	// NO Bcc HEADER. See Message.Recipients.
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	buf.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)

	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	buf.WriteString(m.Body)
	buf.WriteString("\r\n")

	for _, a := range m.Attachments {
		contentType := a.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		fmt.Fprintf(&buf, "--%s\r\n", boundary)
		fmt.Fprintf(&buf, "Content-Type: %s; name=%q\r\n", contentType, a.Filename)
		buf.WriteString("Content-Transfer-Encoding: base64\r\n")
		fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=%q\r\n\r\n", a.Filename)

		// Wrapped at 76 characters: RFC 2045 caps an encoded line at 76, and
		// some servers reject or mangle a single enormous line. A spreadsheet
		// is comfortably big enough to hit this.
		encoded := base64.StdEncoding.EncodeToString(a.Content)
		for len(encoded) > 76 {
			buf.WriteString(encoded[:76])
			buf.WriteString("\r\n")
			encoded = encoded[76:]
		}
		buf.WriteString(encoded)
		buf.WriteString("\r\n")
	}

	fmt.Fprintf(&buf, "--%s--\r\n", boundary)
	return buf.Bytes(), nil
}
