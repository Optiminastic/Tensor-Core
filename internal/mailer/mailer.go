// Package mailer sends the design cost report by email with the PDF attached,
// over plain net/smtp (STARTTLS on 587). The MIME assembly is pure and testable;
// only Send performs I/O. No third-party dependency.
package mailer

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
)

// Config is the SMTP connection + sender identity.
type Config struct {
	Host string
	Port int
	User string
	Pass string
	From string
}

func (c Config) addr() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

// Attachment is one file to attach (the report PDF).
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// Send delivers a plain-text email with one attachment to a single recipient.
func Send(cfg Config, to, subject, body string, attach Attachment) error {
	msg := buildMessage(cfg.From, to, subject, body, attach)
	var auth smtp.Auth
	if cfg.User != "" {
		auth = smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)
	}
	if err := smtp.SendMail(cfg.addr(), auth, cfg.From, []string{to}, msg); err != nil {
		return fmt.Errorf("mailer: send: %w", err)
	}
	return nil
}

// buildMessage assembles a multipart/mixed message: a text part and a base64
// attachment. Pure - unit-tested without a live SMTP server.
func buildMessage(from, to, subject, body string, attach Attachment) []byte {
	var partBuf bytes.Buffer
	mw := multipart.NewWriter(&partBuf)

	textPart, _ := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type": {"text/plain; charset=utf-8"},
	})
	_, _ = textPart.Write([]byte(body))

	ct := attach.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	filePart, _ := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {ct},
		"Content-Transfer-Encoding": {"base64"},
		"Content-Disposition":       {fmt.Sprintf("attachment; filename=%q", attach.Filename)},
	})
	writeBase64(filePart, attach.Data)
	_ = mw.Close()

	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\n", from)
	fmt.Fprintf(&msg, "To: %s\r\n", to)
	fmt.Fprintf(&msg, "Subject: %s\r\n", subject)
	msg.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&msg, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", mw.Boundary())
	msg.Write(partBuf.Bytes())
	return msg.Bytes()
}

// writeBase64 encodes data and writes it in 76-char CRLF-terminated lines, as
// required for a well-formed MIME base64 body.
func writeBase64(w io.Writer, data []byte) {
	const lineLen = 76
	enc := base64.StdEncoding.EncodeToString(data)
	for i := 0; i < len(enc); i += lineLen {
		end := min(i+lineLen, len(enc))
		_, _ = io.WriteString(w, enc[i:end])
		_, _ = io.WriteString(w, "\r\n")
	}
}
