package httpapi

// POST /integrations/bambubuddy/events - BambuBuddy telling Tensor what just
// happened, instead of Tensor asking.
//
// Everything Tensor knows about the fleet is currently pulled: a 60-second sync
// that walks every printer, plus a per-planning-run read of queue depth and
// remaining time. That is fine for a board and poor for a scheduler. A print
// that finishes 5 seconds after a sync leaves its machine looking busy for
// another 55, and the plate's real duration and filament are learned only when
// somebody next asks.
//
// BambuBuddy can push instead. Its notification providers include a "webhook"
// type, with a toggle per event - print started/complete/failed/stopped, queue
// job added/assigned/started/failed, printer offline/error, plate not empty -
// and the payloads carry real data rather than a message: print_complete sends
// the duration and the filament actually used, which is a better number for
// projecting when a machine frees than any slicer estimate.
//
// This is the receiving end. It is deliberately forgiving about shape (the
// provider's payload is a template, and an operator may edit it) and strict
// about identity: an unauthenticated endpoint that accepts fleet state has to
// prove the caller is BambuBuddy before believing a word of it.
//
// Polling stays. A push that arrives while Tensor is restarting is simply lost,
// and a board that silently stops updating is worse than one that updates every
// minute; the pull is the floor under the push, not a duplicate of it.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/obs"
)

// registerBambuBuddyEvents mounts the push endpoint.
//
// Outside the guards: BambuBuddy holds no Better Auth token and never will. It
// authenticates with the shared secret below, the same way the Shopify OAuth
// callback authenticates with its HMAC rather than with a user session.
func (s *Server) registerBambuBuddyEvents(r *gin.Engine) {
	r.POST("/integrations/bambubuddy/events", s.bambuBuddyEvent)
}

// bambuBuddyEventPayload is what a notification provider posts.
//
// Only the fields Tensor acts on are named; the rest of the payload is kept as
// raw JSON for the log, because the provider's template is editable and a field
// this code has never heard of is still evidence when something goes wrong.
type bambuBuddyEventPayload struct {
	// Event is the event type - "print_complete", "queue_job_started". Several
	// spellings are accepted because the template controls the key.
	Event     string `json:"event"`
	EventType string `json:"event_type"`
	// Printer is the machine's name as BambuBuddy shows it ("H2", "P1").
	Printer     string `json:"printer"`
	PrinterName string `json:"printer_name"`
	PrinterID   *int   `json:"printer_id"`
	// Filename is the plate. For a Tensor-built bed this is its orders and
	// colour: "114647-114649-...-BLUE_plate_1".
	Filename string `json:"filename"`
	// Duration and FilamentGrams are the MEASURED cost of a finished print -
	// the numbers a slicer estimate only approximates.
	Duration      string   `json:"duration"`
	FilamentGrams *float64 `json:"filament_grams"`
	Progress      *float64 `json:"progress"`
	Reason        string   `json:"reason"`
	Timestamp     string   `json:"timestamp"`
	// EstimatedTime and ETA arrive on print_start, and are the strings
	// BambuBuddy shows a person - "Unknown" when the printer has not said yet.
	EstimatedTime string `json:"estimated_time"`
	ETA           string `json:"eta"`
	// Message is the human line BambuBuddy composed, e.g.
	// "H1: BLUE-114807-114808.stl / Estimated: Unknown".
	Message string `json:"message"`
}

// imageKey is the payload field holding a base64 JPEG of the plate.
//
// Every event carries one, and it is the bulk of the body by far. It is dropped
// before the payload is logged: a thumbnail in a log line is unreadable, buries
// the fields that matter, and turns a few events per print into megabytes of
// log nobody can grep.
const imageKey = "image"

// eventName is the event's type, whichever key the template used.
func (p bambuBuddyEventPayload) eventName() string {
	if e := strings.TrimSpace(p.Event); e != "" {
		return e
	}
	return strings.TrimSpace(p.EventType)
}

// printerName is the machine, whichever key the template used.
func (p bambuBuddyEventPayload) printerName() string {
	if n := strings.TrimSpace(p.Printer); n != "" {
		return n
	}
	return strings.TrimSpace(p.PrinterName)
}

// bambuBuddyEvent receives one pushed event.
//
// Always answers 200 once the caller is authenticated, even for an event Tensor
// does not act on. A notification provider that gets a 4xx may disable itself or
// retry for ever, and neither is worth risking over an event type added in a
// BambuBuddy release Tensor has not caught up with.
func (s *Server) bambuBuddyEvent(c *gin.Context) {
	if !s.bambuEventAuthorised(c) {
		return
	}
	log := obs.FromContext(c.Request.Context())

	body, ok := readBody(c)
	if !ok {
		return
	}
	var payload bambuBuddyEventPayload
	if err := unmarshalEvent(body, &payload); err != nil {
		// Not a rejection: the body is logged so a template that sends
		// something unexpected can be seen and fixed, rather than silently
		// failing at the far end.
		log.Warn("bambubuddy event could not be read",
			"error", err, "body", truncateForLog(string(body)))
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	log.Info("bambubuddy event",
		"event", payload.eventName(), "printer", payload.printerName(),
		"file", payload.Filename, "duration", payload.Duration,
		"grams", payload.FilamentGrams, "reason", payload.Reason,
		// Everything else the template sent, minus the thumbnail. Kept because
		// the payload is editable in BambuBuddy and a field this code has never
		// heard of is still evidence when something goes wrong - the first test
		// event parsed cleanly with every field empty, a failure that looks
		// exactly like success in a log that prints only what it understood.
		"payload", payloadForLog(body))

	// A state change is worth a look at the fleet. Progress ticks and layer
	// milestones are not: they arrive constantly during a print and say nothing
	// the board does not already show.
	if changesFleetState(payload.eventName()) {
		s.refreshFleetAfterEvent(c.Request.Context(), payload)
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}

// bambuEventAuthorised proves the caller is BambuBuddy.
//
// The secret may arrive as a header or as a query parameter, because a
// notification provider's config may not let an operator set headers - and an
// endpoint that can only be secured a way the caller cannot manage is an
// endpoint that ends up unsecured.
//
// Compared in constant time, and refused outright when no secret is configured:
// an unauthenticated endpoint that writes fleet state is worse than one that
// does not exist.
func (s *Server) bambuEventAuthorised(c *gin.Context) bool {
	want := strings.TrimSpace(s.cfg.BambuBuddyWebhookSecret)
	if want == "" {
		detail(c, http.StatusServiceUnavailable,
			"BambuBuddy push events are not configured.")
		return false
	}
	got := strings.TrimSpace(c.GetHeader("X-Tensor-Token"))
	if got == "" {
		got = strings.TrimSpace(c.Query("token"))
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		// No detail about what was wrong: a caller guessing the secret learns
		// nothing from the difference between "missing" and "incorrect".
		detail(c, http.StatusUnauthorized, "Not authorised.")
		return false
	}
	return true
}

// payloadForLog is the event body without its thumbnail, bounded.
//
// Falls back to the raw body when it cannot be re-read as an object, because a
// body this function cannot understand is exactly the one worth seeing.
func payloadForLog(body []byte) string {
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		return truncateForLog(string(body))
	}
	delete(fields, imageKey)
	out, err := json.Marshal(fields)
	if err != nil {
		return truncateForLog(string(body))
	}
	return truncateForLog(string(out))
}

// truncateForLog bounds a body in the log. A pushed payload is small, but a
// misconfigured provider could post anything.
func truncateForLog(s string) string {
	const max = 2000
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// changesFleetState reports whether an event means a machine started, stopped
// or broke - the events after which the board is stale.
//
// Progress, first-layer and queue-position events are deliberately absent: they
// arrive throughout a print and would turn a push into a poll, at a rate the
// fleet refresh cannot keep up with.
func changesFleetState(event string) bool {
	switch strings.ToLower(strings.TrimSpace(event)) {
	case "print_start", "print_complete", "print_failed", "print_stopped",
		"printer_offline", "printer_error", "queue_job_started", "queue_completed":
		return true
	default:
		return false
	}
}

// eventRefreshDebounce is the shortest gap between two event-driven fleet
// refreshes.
//
// A refresh walks every printer, so a burst - a plate finishing on one machine
// while another starts - must not turn into a burst of full fleet reads. Ten
// seconds is far below the 60-second periodic sync, so the board still gains
// most of the immediacy the push was for.
const eventRefreshDebounce = 10 * time.Second

// refreshFleetAfterEvent brings the fleet up to date after a state change.
//
// Synchronous, and deliberately: the refresh is what makes the push worth
// having, and doing it on a goroutine would answer BambuBuddy before Tensor
// knew anything - so a failure would be invisible and the ordering of two close
// events would be undefined. A fleet read is a second or two.
func (s *Server) refreshFleetAfterEvent(ctx context.Context, payload bambuBuddyEventPayload) {
	log := obs.FromContext(ctx)

	s.eventRefreshMu.Lock()
	since := time.Since(s.lastEventRefresh)
	if since < eventRefreshDebounce {
		s.eventRefreshMu.Unlock()
		log.Debug("fleet refresh skipped, one just ran",
			"event", payload.eventName(), "printer", payload.printerName(),
			"since_ms", since.Milliseconds())
		return
	}
	s.lastEventRefresh = time.Now()
	s.eventRefreshMu.Unlock()

	out, err := s.RefreshFleetFromBambuBuddy(ctx)
	if err != nil {
		// Not returned to BambuBuddy: the event was received and understood,
		// and making the provider retry over Tensor's own upstream trouble
		// would only add load to a fleet already having a bad minute.
		log.Warn("fleet refresh after a pushed event failed",
			"event", payload.eventName(), "printer", payload.printerName(), "error", err)
		return
	}
	log.Info("fleet refreshed on a pushed event",
		"event", payload.eventName(), "printer", payload.printerName(), "printers", out.Synced)
}

// unmarshalEvent reads a pushed body into the payload. A named function rather
// than an inline json.Unmarshal so the parse can be exercised against a real
// captured event without going through the HTTP layer.
func unmarshalEvent(body []byte, into *bambuBuddyEventPayload) error {
	return json.Unmarshal(body, into)
}
