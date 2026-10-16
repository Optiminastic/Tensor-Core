package httpapi

// The WhatsApp inbox: every conversation the brand's number has had, the
// messages in one, a reply box, and handing a chat back to the bot.
//
// A REPLY TAKES THE CHAT OVER. Sending from here puts the conversation in human
// mode exactly as a reply from the WhatsApp Business app does, so the bot does
// not answer the customer's next message over the top of a person. "Hand back"
// is the explicit way to return it; otherwise the bot resumes on its own after
// twelve quiet hours.

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/bot"
)

const (
	inboxConversationRows = 200
	inboxMessageRows      = 300
	maxReplyRunes         = 4096
	// customerServiceWindow is how long after a customer's last message Meta
	// allows a free-form reply. After it only an approved template gets through.
	customerServiceWindow = 24 * time.Hour
)

func (s *Server) registerWhatsAppInbox(r *gin.Engine) {
	g := r.Group("/brands/:slug/whatsapp/conversations")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.WhatsAppRead.Key()), s.listWhatsAppConversations)
	g.GET("/:wa_id/messages", s.guards.RequirePermission(auth.WhatsAppRead.Key()), s.listWhatsAppMessages)
	g.POST("/:wa_id/messages", s.guards.RequirePermission(auth.WhatsAppReply.Key()), s.replyWhatsApp)
	g.POST("/:wa_id/handback", s.guards.RequirePermission(auth.WhatsAppReply.Key()), s.handBackWhatsApp)
}

type whatsappConversationJSON struct {
	WaID               string     `json:"wa_id"`
	ProfileName        string     `json:"profile_name"`
	Mode               string     `json:"mode"` // "bot" or "human"
	OptedOut           bool       `json:"opted_out"`
	CanReply           bool       `json:"can_reply"`
	LastCustomerAt     *time.Time `json:"last_customer_at"`
	LastMessageAt      *time.Time `json:"last_message_at"`
	LastMessagePreview string     `json:"last_message_preview"`
}

type whatsappMessageJSON struct {
	ID        string    `json:"id"`
	Direction string    `json:"direction"` // in, out, echo
	Author    string    `json:"author"`    // customer, bot, staff
	Kind      string    `json:"kind"`
	Body      string    `json:"body"`
	Status    string    `json:"status"`
	ErrorCode *int32    `json:"error_code"`
	SentBy    *string   `json:"sent_by"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) listWhatsAppConversations(c *gin.Context) {
	rows, err := s.store.Q.ListWhatsAppConversations(c.Request.Context(), gen.ListWhatsAppConversationsParams{
		BrandSlug: c.Param("slug"), RowLimit: inboxConversationRows,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read WhatsApp conversations.")
		return
	}
	now := time.Now()
	items := make([]whatsappConversationJSON, 0, len(rows))
	for _, row := range rows {
		items = append(items, conversationJSON(row, now))
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) listWhatsAppMessages(c *gin.Context) {
	ctx := c.Request.Context()
	brand, waID := c.Param("slug"), c.Param("wa_id")
	conv, err := s.store.Q.GetWhatsAppConversation(ctx, gen.GetWhatsAppConversationParams{BrandSlug: brand, WaID: waID})
	if errors.Is(err, pgx.ErrNoRows) {
		detail(c, http.StatusNotFound, "No WhatsApp conversation with that number.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the conversation.")
		return
	}
	rows, err := s.store.Q.ListWhatsAppMessages(ctx, gen.ListWhatsAppMessagesParams{
		BrandSlug: brand, WaID: waID, RowLimit: inboxMessageRows,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the messages.")
		return
	}
	// Newest first from the database, oldest first on the page.
	items := make([]whatsappMessageJSON, len(rows))
	for i, row := range rows {
		items[len(rows)-1-i] = messageJSON(row)
	}
	c.JSON(http.StatusOK, gin.H{"conversation": conversationJSON(conv, time.Now()), "items": items})
}

type whatsappReplyRequest struct {
	Text string `json:"text"`
}

func (s *Server) replyWhatsApp(c *gin.Context) {
	ctx := c.Request.Context()
	brand, waID := c.Param("slug"), c.Param("wa_id")

	var req whatsappReplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "Send a JSON body with the reply text.")
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || utf8.RuneCountInString(text) > maxReplyRunes {
		detail(c, http.StatusUnprocessableEntity, "A reply must be between 1 and 4096 characters.")
		return
	}
	client := s.whatsappFor(ctx, brand)
	if !client.Configured() {
		detail(c, http.StatusServiceUnavailable, "WhatsApp is not connected for this brand.")
		return
	}

	var sent gen.WhatsappMessage
	status, msg := http.StatusCreated, ""
	err := s.withWhatsAppConversation(ctx, brand, waID, func(q *gen.Queries, conv bot.Conversation) (bot.Conversation, error) {
		if conv.LastCustomerAt == nil {
			status, msg = http.StatusNotFound, "This customer has never messaged the shop, so WhatsApp only allows an approved template."
			return conv, nil
		}
		if time.Since(*conv.LastCustomerAt) > customerServiceWindow {
			status, msg = http.StatusUnprocessableEntity,
				"This customer last wrote more than 24 hours ago. WhatsApp only allows an approved template now."
			return conv, nil
		}
		res, err := client.SendText(ctx, waID, text)
		if err != nil {
			status, msg = http.StatusBadGateway, "WhatsApp refused the reply: "+err.Error()
			return conv, nil
		}
		accepted := res.Status
		if _, err := recordWhatsAppMessage(ctx, q, whatsappMessageRecord{
			Brand: brand, WaID: waID, MessageID: res.MessageID, Direction: directionOut,
			Kind: "text", Body: text, SentBy: currentUserID(c), Status: &accepted, At: time.Now(),
		}); err != nil {
			return conv, err
		}
		sent, err = q.GetWhatsAppMessageByWAID(ctx, res.MessageID)
		if err != nil {
			return conv, err
		}
		// A person is now talking: the bot stays out of it.
		next, _ := bot.Step(conv, bot.StaffEcho{}, time.Now().UTC())
		return next, nil
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "The reply was sent but could not be recorded.")
		return
	}
	if msg != "" {
		detail(c, status, msg)
		return
	}
	c.JSON(http.StatusCreated, messageJSON(sent))
}

func (s *Server) handBackWhatsApp(c *gin.Context) {
	ctx := c.Request.Context()
	brand, waID := c.Param("slug"), c.Param("wa_id")
	if _, err := s.store.Q.GetWhatsAppConversation(ctx, gen.GetWhatsAppConversationParams{BrandSlug: brand, WaID: waID}); err != nil {
		detail(c, http.StatusNotFound, "No WhatsApp conversation with that number.")
		return
	}
	err := s.withWhatsAppConversation(ctx, brand, waID, func(_ *gen.Queries, conv bot.Conversation) (bot.Conversation, error) {
		conv.State = bot.StateIdle
		conv.Ctx = bot.Context{}
		conv.HumanSince = nil
		return conv, nil
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not hand the conversation back.")
		return
	}
	c.Status(http.StatusNoContent)
}

func conversationJSON(row gen.WhatsappConversation, now time.Time) whatsappConversationJSON {
	mode := "bot"
	if bot.State(row.State) == bot.StateHuman {
		mode = "human"
	}
	lastCustomer := timePtr(row.LastCustomerAt)
	return whatsappConversationJSON{
		WaID: row.WaID, ProfileName: row.ProfileName, Mode: mode, OptedOut: row.OptedOut,
		CanReply:       lastCustomer != nil && now.Sub(*lastCustomer) <= customerServiceWindow,
		LastCustomerAt: lastCustomer, LastMessageAt: timePtr(row.LastMessageAt),
		LastMessagePreview: row.LastMessagePreview,
	}
}

func messageJSON(row gen.WhatsappMessage) whatsappMessageJSON {
	author := "customer"
	switch {
	case row.Direction == directionEcho, row.Direction == directionOut && row.SentBy != nil:
		author = "staff"
	case row.Direction == directionOut:
		author = "bot"
	}
	return whatsappMessageJSON{
		ID: row.ID.String(), Direction: row.Direction, Author: author, Kind: row.Kind,
		Body: row.Body, Status: row.Status, ErrorCode: row.ErrorCode, SentBy: row.SentBy,
		CreatedAt: row.CreatedAt.Time,
	}
}
