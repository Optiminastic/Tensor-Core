package httpapi

// Every call the win-back agent has made, joined back to the basket it was
// about.
//
// Sarvam holds the calls; Tensor holds why each one happened. Neither is much
// use alone - Sarvam's list is a column of hashed phone numbers, and Tensor's
// records say a call was placed without saying what came of it - so this reads
// both and matches them up.
//
// TWO KEYS, in order of trust:
//
//  1. attempt_id. Exact, because Tensor stored it at the moment it dialled.
//  2. the phone number. For calls placed before a record existed, or from the
//     Test call page, Sarvam returns user_contact in full and that is enough
//     to find the checkout it belongs to.
//
// The second is the reason the page is not empty today: every real call on
// this account was placed before the record table existed.

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/sarvam"
)

// defaultCallLogWindowDays bounds the listing. Sarvam requires a window, and
// recent activity is what anybody opens this page for.
const defaultCallLogWindowDays = 30

func (s *Server) registerCallLogs(r *gin.Engine) {
	g := r.Group("/brands/:slug/call-logs")
	g.Use(s.guards.RequireUser())
	// order:read, matching the Abandoned Checkouts page. This is the same
	// customer data reached a different way, so the roles that may see one
	// may see the other.
	read := s.guards.RequirePermission(auth.OrderRead.Key())

	g.GET("", read, s.listCallLogs)
	// The interaction id travels as a QUERY parameter, not a path segment.
	// It looks like "20261007/8dfc36f1-13:19:48-ea3d34fe" - percent-encode the
	// slash and the router decodes it straight back into a path separator, so
	// the route never matches and every transcript answers 404. Measured.
	g.GET("/transcript", read, s.getCallTranscript)
	g.GET("/recording", read, s.getCallRecording)
}

type callLogResponse struct {
	AttemptID     string `json:"attempt_id"`
	InteractionID string `json:"interaction_id"`
	StartedAt     string `json:"started_at"`
	// Connected says whether anybody picked up; Outcome is Sarvam's reason
	// when they did not. Both, because there is no single status field and
	// "connected" alone hides a call that rang out.
	Connected       bool    `json:"connected"`
	FailureReason   string  `json:"failure_reason"`
	EndedBy         string  `json:"ended_by"`
	DurationSeconds float64 `json:"duration_seconds"`
	Messages        int     `json:"messages"`
	Language        string  `json:"language"`
	// Contact is masked, and is NOT always a phone number: a call placed from
	// Sarvam's own console carries the tester's email instead. The page says
	// "contact" for that reason rather than promising a number it may not have.
	Phone string `json:"phone"`
	// FromConsole marks a call placed in Sarvam's console rather than by
	// Tensor. Worth separating: those are somebody testing, not a customer
	// being rung, and counting them as win-back calls would flatter the numbers.
	FromConsole bool `json:"from_console"`
	// RecordingURL plays the call back.
	RecordingURL string `json:"recording_url"`
	// Summary and Disposition are the AGENT'S own output variables, written by
	// Sarvam at the end of the call. Tensor computes neither.
	Summary     string `json:"summary"`
	Disposition string `json:"disposition"`

	// The basket this call was about. Empty when nothing matched - a test call
	// to a number that never abandoned anything is a real and uninteresting
	// case, and inventing a checkout for it would be worse than a blank.
	CheckoutID   string `json:"checkout_id"`
	CheckoutName string `json:"checkout_name"`
	CustomerName string `json:"customer_name"`
	// MatchedBy is "attempt", "phone" or "" - so a reader can tell an exact
	// join from a best-effort one rather than trusting both equally.
	MatchedBy string `json:"matched_by"`
}

type callLogsResponse struct {
	Items []callLogResponse `json:"items"`
}

func (s *Server) listCallLogs(c *gin.Context) {
	ctx := c.Request.Context()
	slug := c.Param("slug")

	client := s.sarvamFor(ctx, slug)
	if !client.Configured() {
		detail(c, http.StatusServiceUnavailable,
			"Sarvam is not connected for this brand, so there are no call logs to read. "+
				"Connect it in Settings → Integrations.")
		return
	}

	days := defaultCallLogWindowDays
	if raw := c.Query("days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 180 {
			detail(c, http.StatusUnprocessableEntity,
				"days must be a whole number between 1 and 180.")
			return
		}
		days = parsed
	}

	attempts, err := client.ListAttempts(ctx, time.Now().AddDate(0, 0, -days), 200)
	if err != nil {
		// Sarvam's own sentence. Its refusals name the window or the key.
		detail(c, http.StatusBadGateway, err.Error())
		return
	}

	// Our side of the join, read once rather than per row.
	records, err := s.store.Q.ListCheckoutCalls(ctx, gen.ListCheckoutCallsParams{
		BrandSlug: slug, RowLimit: 500,
	})
	if err != nil {
		// A call log is still worth reading without the mapping: Sarvam's half
		// is the half that says what happened.
		records = nil
	}

	byAttempt := make(map[string]int, len(records))
	byPhone := make(map[string]int, len(records))
	for i, rec := range records {
		if rec.AttemptID != nil && *rec.AttemptID != "" {
			byAttempt[*rec.AttemptID] = i
		}
		if key := normaliseIndianMobile(rec.Phone); key != "" {
			byPhone[key] = i
		}
	}

	items := make([]callLogResponse, 0, len(attempts))
	for _, attempt := range attempts {
		row := toCallLog(attempt)

		idx, matchedBy := -1, ""
		if i, ok := byAttempt[attempt.AttemptID]; ok {
			idx, matchedBy = i, "attempt"
		} else if i, ok := byPhone[normaliseIndianMobile(attempt.UserContact)]; ok {
			idx, matchedBy = i, "phone"
		}
		if idx >= 0 {
			rec := records[idx]
			row.CheckoutID, row.CheckoutName = rec.CheckoutID, rec.CheckoutName
			row.CustomerName, row.MatchedBy = rec.CustomerName, matchedBy

			// Learned here and cached, so a later transcript fetch does not
			// have to list every attempt again to find one id.
			if rec.InteractionID == nil && attempt.InteractionID != "" {
				interaction, attemptID := attempt.InteractionID, attempt.AttemptID
				_ = s.store.Q.SetCallInteractionID(ctx, gen.SetCallInteractionIDParams{
					InteractionID: &interaction, AttemptID: &attemptID,
				})
			}
		}
		items = append(items, row)
	}

	c.JSON(http.StatusOK, callLogsResponse{Items: items})
}

// getCallRecording streams one call's audio through Tensor.
//
// Proxied rather than linked. The audio_url on an attempt points at
// indus.sarvam.ai, which authenticates with a browser session and answers 403
// to an API key - so a browser following it from Tensor gets nothing. The
// analytics recordings endpoint does take the key, which means Tensor can
// fetch the audio and pass it on without the key ever reaching a page.
//
// Streamed, not buffered: a call is half a megabyte and reading it into memory
// would hold one per listener for no reason. Range headers are forwarded both
// ways so the player can seek.
func (s *Server) getCallRecording(c *gin.Context) {
	client := s.sarvamFor(c.Request.Context(), c.Param("slug"))
	if !client.Configured() {
		detail(c, http.StatusServiceUnavailable, "Sarvam is not connected for this brand.")
		return
	}
	interaction := c.Query("interaction")
	if interaction == "" {
		detail(c, http.StatusBadRequest, "Name the call to play.")
		return
	}

	resp, err := client.Recording(c.Request.Context(), interaction)
	if err != nil {
		detail(c, http.StatusBadGateway, err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "audio/wav"
	}
	// Lets the player seek rather than only play from the start.
	c.Header("Accept-Ranges", "bytes")
	if r := resp.Header.Get("Content-Range"); r != "" {
		c.Header("Content-Range", r)
	}
	c.DataFromReader(resp.StatusCode, resp.ContentLength, contentType, resp.Body, nil)
}

func toCallLog(a sarvam.Attempt) callLogResponse {
	return callLogResponse{
		AttemptID:       a.AttemptID,
		InteractionID:   a.InteractionID,
		StartedAt:       a.StartDatetime,
		Connected:       a.ConnectivityStatus == "connected",
		FailureReason:   a.FailureReason,
		EndedBy:         a.EndedBy,
		DurationSeconds: a.DurationSeconds,
		Messages:        a.NumMessages,
		Language:        a.LanguageName,
		Phone:           a.UserContactMasked,
		FromConsole:     a.IsDebugCall == 1,
		RecordingURL:    a.AudioURL,
		Summary:         a.Summary(),
		Disposition:     a.Disposition(),
	}
}

type transcriptTurnResponse struct {
	Turn int `json:"turn"`
	// Role is "assistant" or "user".
	Role    string `json:"role"`
	Content string `json:"content"`
}

type transcriptResponse struct {
	InteractionID string                   `json:"interaction_id"`
	Turns         []transcriptTurnResponse `json:"turns"`
}

// getCallTranscript returns one call, turn by turn.
func (s *Server) getCallTranscript(c *gin.Context) {
	client := s.sarvamFor(c.Request.Context(), c.Param("slug"))
	if !client.Configured() {
		detail(c, http.StatusServiceUnavailable, "Sarvam is not connected for this brand.")
		return
	}
	interaction := c.Query("interaction")
	if interaction == "" {
		detail(c, http.StatusBadRequest, "Name the call to read.")
		return
	}

	transcript, err := client.Transcript(c.Request.Context(), interaction)
	if err != nil {
		detail(c, http.StatusBadGateway, err.Error())
		return
	}

	turns := make([]transcriptTurnResponse, 0, len(transcript.Messages))
	for _, m := range transcript.Messages {
		turns = append(turns, transcriptTurnResponse{
			Turn: m.TurnID, Role: m.Role, Content: m.Content,
		})
	}
	c.JSON(http.StatusOK, transcriptResponse{
		InteractionID: transcript.InteractionID, Turns: turns,
	})
}
