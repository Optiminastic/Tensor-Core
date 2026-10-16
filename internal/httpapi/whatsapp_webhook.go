package httpapi

// GET/POST /integrations/whatsapp/webhook - Meta telling Tensor what customers
// sent, what staff sent from the WhatsApp Business app, and what happened to
// the messages Tensor sent.
//
// PUBLIC, LIKE THE BAMBUBUDDY CALLBACK: it authenticates by signature, not by a
// session. Every POST carries X-Hub-Signature-256, an HMAC of the raw body with
// the Meta app's secret. Which secret depends on which brand the message is
// for, and that is only known from the body - so the body is parsed first,
// the brand found from its phone_number_id, and nothing is stored or acted on
// until that brand's secret verifies the signature. A brand with no secret is
// refused with 503, never trusted.
//
// FAST AND DUMB. The handler only stores and enqueues; every reply happens in
// a River job. Meta retries a webhook that does not get a quick 2xx, and a
// retry is deduplicated by the message's wamid.

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/whatsapp"
	"github.com/Optiminastic/tensor-core/internal/production"
)

const (
	maxWebhookBytes      = 1 << 20
	headerHubSignature   = "X-Hub-Signature-256"
	hubModeSubscribe     = "subscribe"
	directionIn          = "in"
	directionOut         = "out"
	directionEcho        = "echo"
	messagePreviewLength = 120
)

func (s *Server) registerWhatsAppWebhook(r *gin.Engine) {
	r.GET("/integrations/whatsapp/webhook", s.whatsappHandshake)
	r.POST("/integrations/whatsapp/webhook", s.whatsappWebhook)
}

// whatsappHandshake answers Meta's one-time subscription check. The verify
// token is per brand, and Meta's request names no brand, so it is matched
// against every brand that has one.
func (s *Server) whatsappHandshake(c *gin.Context) {
	mode, token := c.Query("hub.mode"), c.Query("hub.verify_token")
	if mode != hubModeSubscribe || token == "" || !s.knownVerifyToken(c.Request.Context(), token) {
		detail(c, http.StatusForbidden, "Unknown verify token.")
		return
	}
	c.String(http.StatusOK, c.Query("hub.challenge"))
}

func (s *Server) knownVerifyToken(ctx context.Context, token string) bool {
	brands, err := s.store.Q.ListWhatsAppBrands(ctx)
	if err != nil {
		return false
	}
	for _, brand := range brands {
		want := s.whatsappBotSettingsFor(ctx, brand).VerifyToken
		if want != "" && subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1 {
			return true
		}
	}
	return false
}

func (s *Server) whatsappWebhook(c *gin.Context) {
	ctx := c.Request.Context()
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookBytes))
	if err != nil {
		detail(c, http.StatusRequestEntityTooLarge, "Webhook body too large.")
		return
	}
	changes, err := whatsapp.ParseWebhook(body)
	if err != nil {
		detail(c, http.StatusBadRequest, "Not a WhatsApp webhook.")
		return
	}

	signature := c.GetHeader(headerHubSignature)
	type target struct {
		brand    string
		change   whatsapp.Change
		settings whatsappBotSettings
	}
	var targets []target
	for phoneNumberID, change := range changes {
		brand, ok := s.brandForPhoneNumber(ctx, phoneNumberID)
		if !ok {
			continue // a number no brand owns; nothing to do, and nothing to leak
		}
		settings := s.whatsappBotSettingsFor(ctx, brand)
		if settings.AppSecret == "" {
			detail(c, http.StatusServiceUnavailable, "WhatsApp app secret is not set for this brand.")
			return
		}
		if !whatsapp.VerifySignature(settings.AppSecret, body, signature) {
			detail(c, http.StatusUnauthorized, "Invalid signature.")
			return
		}
		targets = append(targets, target{brand: brand, change: change, settings: settings})
	}

	for _, t := range targets {
		if err := s.ingestWhatsApp(ctx, t.brand, t.change, t.settings); err != nil {
			s.logger.Error("store whatsapp webhook", "brand", t.brand, "error", err)
			detail(c, http.StatusInternalServerError, "Could not store the webhook.") // Meta retries
			return
		}
	}
	c.Status(http.StatusOK)
}

// brandForPhoneNumber finds the one brand whose WhatsApp integration sends
// from this number. Two brands claiming one number is a misconfiguration, and
// guessing between them would show one brand's customers to the other.
func (s *Server) brandForPhoneNumber(ctx context.Context, phoneNumberID string) (string, bool) {
	brands, err := s.store.Q.FindWhatsAppBrandsByPhoneNumberID(ctx, phoneNumberID)
	if err != nil || len(brands) != 1 {
		if len(brands) > 1 {
			s.logger.Error("whatsapp number claimed by several brands", "phone_number_id", phoneNumberID)
		}
		return "", false
	}
	return brands[0], true
}

// ingestWhatsApp stores one brand's share of a webhook and enqueues the jobs
// that answer it, in one transaction: a retried webhook finds its messages
// already stored and enqueues nothing twice.
func (s *Server) ingestWhatsApp(ctx context.Context, brand string, ch whatsapp.Change, settings whatsappBotSettings) error {
	return pgx.BeginFunc(ctx, s.store.Pool, func(tx pgx.Tx) error {
		q := s.store.Q.WithTx(tx)
		var jobs []river.InsertManyParams

		for _, m := range ch.Messages {
			fresh, err := recordWhatsAppMessage(ctx, q, brand, m.From, m.ID, directionIn, m.Kind, inboundBody(m), "", nil, m.Timestamp, m.ProfileName)
			if err != nil {
				return err
			}
			if fresh && settings.answers(m.From) {
				jobs = append(jobs, river.InsertManyParams{Args: production.WhatsAppInboundArgs{
					BrandSlug: brand, WaID: m.From, MessageID: m.ID,
					MessageKind: m.Kind, Text: m.Text, ButtonID: m.ButtonID,
				}})
			}
		}
		for _, e := range ch.Echoes {
			fresh, err := recordWhatsAppMessage(ctx, q, brand, e.To, e.ID, directionEcho, e.Kind, e.Text, "", nil, e.Timestamp, "")
			if err != nil {
				return err
			}
			if fresh {
				jobs = append(jobs, river.InsertManyParams{Args: production.WhatsAppEchoArgs{BrandSlug: brand, WaID: e.To, At: e.Timestamp}})
			}
		}
		for _, st := range ch.Statuses {
			if err := q.UpdateWhatsAppMessageStatus(ctx, gen.UpdateWhatsAppMessageStatusParams{
				WaMessageID: st.ID, Status: st.Status, ErrorCode: errorCode(st.ErrorCode),
			}); err != nil {
				return err
			}
			if st.Status == whatsapp.StatusFailed {
				jobs = append(jobs, river.InsertManyParams{Args: production.WhatsAppFailedArgs{
					BrandSlug: brand, MessageID: st.ID, ErrorCode: st.ErrorCode,
				}})
			}
		}
		return s.whatsappQueue.EnqueueTx(ctx, tx, jobs)
	})
}

// recordWhatsAppMessage stores one message and keeps the conversation's
// chat-list fields current. It reports false for a message already stored.
func recordWhatsAppMessage(
	ctx context.Context, q *gen.Queries, brand, waID, messageID, direction, kind, body, sentBy string,
	status *string, at time.Time, profileName string,
) (bool, error) {
	if at.IsZero() {
		at = time.Now()
	}
	stored := ""
	if status != nil {
		stored = *status
	}
	var sender *string
	if sentBy != "" {
		sender = &sentBy
	}
	rows, err := q.InsertWhatsAppMessage(ctx, gen.InsertWhatsAppMessageParams{
		BrandSlug: brand, WaID: waID, WaMessageID: messageID, Direction: direction,
		Kind: kind, Body: body, Status: stored, SentBy: sender, CreatedAt: db.Timestamptz(&at),
	})
	if err != nil || rows == 0 {
		return false, err
	}
	if err := q.EnsureWhatsAppConversation(ctx, gen.EnsureWhatsAppConversationParams{BrandSlug: brand, WaID: waID}); err != nil {
		return false, err
	}
	return true, q.TouchWhatsAppConversation(ctx, gen.TouchWhatsAppConversationParams{
		BrandSlug: brand, WaID: waID, At: db.Timestamptz(&at), FromCustomer: direction == directionIn,
		Preview: preview(body, kind), ProfileName: profileName,
	})
}

// inboundBody is what the inbox shows for a customer message: the text, the
// tapped option's id, or the media kind.
func inboundBody(m whatsapp.Inbound) string {
	switch {
	case m.Text != "":
		return m.Text
	case m.ButtonID != "":
		return m.ButtonID
	default:
		return ""
	}
}

func preview(body, kind string) string {
	text := strings.Join(strings.Fields(body), " ")
	if text == "" {
		return fmt.Sprintf("[%s]", kind)
	}
	runes := []rune(text)
	if len(runes) > messagePreviewLength {
		return string(runes[:messagePreviewLength]) + "..."
	}
	return text
}

func errorCode(code int) *int32 {
	if code == 0 {
		return nil
	}
	v := int32(code)
	return &v
}
