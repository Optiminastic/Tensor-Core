package mailer

import (
	"strings"
	"testing"
)

func TestBuildMessageHasHeadersAndAttachment(t *testing.T) {
	msg := string(buildMessage(
		"reports@tensor.local", "lead@example.com", "Cost report: Alpha",
		"Attached is the cost report.",
		Attachment{Filename: "alpha-cost-report.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.4 fake")},
	))

	for _, want := range []string{
		"From: reports@tensor.local\r\n",
		"To: lead@example.com\r\n",
		"Subject: Cost report: Alpha\r\n",
		"MIME-Version: 1.0\r\n",
		"multipart/mixed; boundary=",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Type: application/pdf",
		"Content-Transfer-Encoding: base64",
		`filename="alpha-cost-report.pdf"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q", want)
		}
	}
	// The body text and a base64 payload must be present.
	if !strings.Contains(msg, "Attached is the cost report.") {
		t.Error("message missing the body text")
	}
}
