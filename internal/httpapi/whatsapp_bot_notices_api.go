package httpapi

// The Bot Messages page's second half: the one-off messages, read and
// rewritten.
//
// SEPARATE FROM THE TREE's endpoint, because they are a different shape and a
// different risk. The tree is replaced wholesale - the page sends the entire
// menu and the server swaps it - which is right for something edited as one
// structure. These are independent lines, and saving one must not be able to
// revert another somebody else changed a moment ago, so each is written on its
// own and the page sends only what it touched.
//
// REWRITES ARE VALIDATED, NOT TRUSTED. A message too long for WhatsApp is
// refused by Meta at send time, which means a customer gets nothing and the
// shop learns about it from a log. A message that lost its %s would reach a
// customer reading "your order  is confirmed". Both are refused here, in words
// the person typing can act on.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
)

func (s *Server) registerWhatsAppBotNotices(r *gin.Engine) {
	g := r.Group("/brands/:slug/whatsapp/bot-messages")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.WhatsAppRead.Key()), s.listBotNotices)
	// The same permission as editing the tree: whoever may change what the
	// bot says in the menu may change what it says everywhere else.
	g.PUT("/:key", s.guards.RequirePermission(auth.WhatsAppReply.Key()), s.saveBotNotice)
	g.DELETE("/:key", s.guards.RequirePermission(auth.WhatsAppReply.Key()), s.resetBotNotice)
}

// listBotNotices is every editable message, with the shop's wording where it
// has one and the shipped wording where it has not.
func (s *Server) listBotNotices(c *gin.Context) {
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"messages": s.storedNotices(c.Request.Context(), brand)})
}

type botNoticeRequest struct {
	Body string `json:"body"`
}

// saveBotNotice stores one rewritten message.
//
// TAKES EFFECT ON THE NEXT MESSAGE, with nothing to restart. The send path
// reads this table every time rather than caching it, which is the whole
// reason the page can promise that.
func (s *Server) saveBotNotice(c *gin.Context) {
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	key := c.Param("key")
	notice, known := content.NoticeByKey(key)
	if !known {
		// A key nobody defined would be stored and never read - a silent
		// no-op, and the worst kind of success.
		detail(c, http.StatusNotFound, "There is no bot message with that name.")
		return
	}

	var req botNoticeRequest
	if !bindJSON(c, &req) {
		return
	}
	body := strings.TrimSpace(req.Body)
	if refusal := content.ValidateNotice(notice, body); refusal != "" {
		detail(c, http.StatusUnprocessableEntity, refusal)
		return
	}

	// UNCHANGED FROM THE DEFAULT IS NOT AN EDIT. Storing it would freeze this
	// brand on today's wording: a later improvement to the shipped text would
	// never reach them, because a row always wins over the binary.
	if body == strings.TrimSpace(notice.Default) {
		if err := s.store.Q.DeleteBotMessage(c.Request.Context(), gen.DeleteBotMessageParams{
			BrandSlug: brand, MessageKey: key,
		}); err != nil {
			dbError(c, err, "", "Could not save that message.")
			return
		}
		c.JSON(http.StatusOK, gin.H{"messages": s.storedNotices(c.Request.Context(), brand)})
		return
	}

	by := currentUserID(c)
	if err := s.store.Q.UpsertBotMessage(c.Request.Context(), gen.UpsertBotMessageParams{
		BrandSlug: brand, MessageKey: key, Body: body, UpdatedBy: &by,
	}); err != nil {
		dbError(c, err, "", "Could not save that message.")
		return
	}
	// The whole set comes back, so the page shows what is actually stored
	// rather than what it hoped it stored.
	c.JSON(http.StatusOK, gin.H{"messages": s.storedNotices(c.Request.Context(), brand)})
}

// resetBotNotice puts one message back to the wording in the binary.
func (s *Server) resetBotNotice(c *gin.Context) {
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	key := c.Param("key")
	if _, known := content.NoticeByKey(key); !known {
		detail(c, http.StatusNotFound, "There is no bot message with that name.")
		return
	}
	if err := s.store.Q.DeleteBotMessage(c.Request.Context(), gen.DeleteBotMessageParams{
		BrandSlug: brand, MessageKey: key,
	}); err != nil {
		dbError(c, err, "", "Could not reset that message.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"messages": s.storedNotices(c.Request.Context(), brand)})
}
