package whatsapp

// Meta's refusals, kept whole and translated into a sentence that names the fix.
//
// The CODE is the thing to switch on. Meta prefixes the same number to the
// message ("(#132001) Template name does not exist..."), and error_data.details
// is the useful half - it names the template and the locale it looked in.
//
// Every code below was either hit while building this or is a documented
// failure of the exact call this package makes. None of them is hypothetical
// except where the comment says so.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrRateLimited is wrapped by the error returned when Meta is throttling.
var ErrRateLimited = errors.New("whatsapp rate limited")

// Error is Meta's structured refusal.
type Error struct {
	Status  int
	Code    int
	Subcode int
	Message string
	// Details is error_data.details, which usually names the thing that was
	// wrong where Message only names the category.
	Details string
	// TraceID is fbtrace_id, which is what Meta support asks for.
	TraceID string
}

func (e *Error) Error() string {
	if sentence := reason(e.Code); sentence != "" {
		return sentence
	}
	if strings.TrimSpace(e.Details) != "" {
		return fmt.Sprintf("WhatsApp refused the message: %s", e.Details)
	}
	if strings.TrimSpace(e.Message) != "" {
		return fmt.Sprintf("WhatsApp refused the message: %s", e.Message)
	}
	return fmt.Sprintf("WhatsApp refused the message (%d)", e.Status)
}

func (e *Error) Unwrap() error {
	if e.Retryable() {
		return ErrRateLimited
	}
	return nil
}

// Retryable is true for almost nothing, deliberately.
//
// A send is not idempotent and Meta accepts no idempotency key, so retrying
// anything it might have acted on can message somebody twice. Only a response
// Meta describes as temporary says so.
//
// A transport timeout is NOT retryable and never reaches here: the request may
// have been delivered and the response lost, which is exactly the case where a
// retry doubles the message.
func (e *Error) Retryable() bool {
	switch e.Code {
	case 130429, 80007, 131056: // throughput, account rate limit, pair rate limit
		return true
	}
	return e.Status == http.StatusInternalServerError ||
		e.Status == http.StatusBadGateway ||
		e.Status == http.StatusServiceUnavailable
}

// Permanent is the opposite, and is what the caller records rather than retries.
//
// Separate from !Retryable() because they are not the same question: an
// unrecognised code is neither obviously temporary nor obviously permanent, and
// the caller should keep the claim and record it rather than hammer Meta or
// silently re-arm the customer for another attempt.
func (e *Error) Permanent() bool { return !e.Retryable() }

// reason turns a Meta code into a sentence naming what to do about it.
func reason(code int) string {
	switch code {
	case 190:
		return "WhatsApp rejected the access token. It has expired or been revoked - " +
			"a user token lasts about an hour, so this needs a System User token."
	case 131037:
		return "The sending number's display name is not approved, so Meta will not " +
			"deliver templates from it."
	case 132001:
		return "No approved template by that name in that language on this business " +
			"account. “en” and “en_US” are different templates."
	case 132000:
		return "The template expects a different number of parameters than were sent."
	case 132005:
		return "A template parameter was rejected - most often a newline, a tab, or a " +
			"run of spaces, none of which Meta allows in a parameter."
	case 132007, 132015, 132016:
		return "Meta has paused or disabled this template over its quality rating."
	case 131026:
		return "That number cannot receive this message - it may not be on WhatsApp, " +
			"or it has opted out of marketing."
	case 131047:
		return "That conversation needs a template to reopen it, which is what this " +
			"message was."
	case 133010:
		return "The sending number is not registered on the Cloud API."
	case 130429, 80007:
		return "Meta is rate limiting Tensor, so the message could not be sent. " +
			"It clears on its own."
	case 131056:
		return "Too many messages to this same number too quickly; Meta is throttling " +
			"the pair."
	case 100:
		return "Meta rejected the request's shape. The usual cause is the button " +
			"component: index must be the string “0” and sub_type “url”."
	}
	return ""
}

// parseError reads Meta's error envelope.
//
// A body that does not parse still produces an Error carrying the status, so a
// caller never loses the fact that something was refused.
func parseError(status int, raw []byte) *Error {
	out := &Error{Status: status}
	var envelope struct {
		Error struct {
			Message   string `json:"message"`
			Code      int    `json:"code"`
			Subcode   int    `json:"error_subcode"`
			FBTraceID string `json:"fbtrace_id"`
			Data      struct {
				Details string `json:"details"`
			} `json:"error_data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		out.Code = envelope.Error.Code
		out.Subcode = envelope.Error.Subcode
		out.Message = envelope.Error.Message
		out.Details = envelope.Error.Data.Details
		out.TraceID = envelope.Error.FBTraceID
	}
	return out
}
