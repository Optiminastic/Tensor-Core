package httpapi

// Editing the bot's tree.
//
// WHATSAPP'S LIMITS ARE CHECKED HERE, not discovered against the live
// number. Meta refuses the WHOLE message when one row is too long, or when a
// message carries a fourth button, and names neither the row nor the rule -
// so the bot would simply stop answering with nothing in the dashboard to
// say why. Everything is bounded on the way in.
//
// THE TREE'S OWN RULES ARE CHECKED HERE TOO, and they matter more than the
// character counts: an option that opens a node which does not exist is a
// button that answers nothing, and a code action on the wrong id is a button
// the state machine will not recognise. Both are silent failures in a chat,
// where nobody is watching.

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
)

// Meta's own caps, restated where they are enforced.
const (
	maxButtonTitle = 20
	maxRowTitle    = 24
	maxRowDesc     = 72
	maxAnswerRunes = 4096
	maxBodyRunes   = 1024
	maxNodes       = 12
)

func (s *Server) registerWhatsAppBotContent(r *gin.Engine) {
	g := r.Group("/brands/:slug/whatsapp/bot-content")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.WhatsAppRead.Key()), s.listBotContent)
	g.PUT("", s.guards.RequirePermission(auth.WhatsAppReply.Key()), s.saveBotContent)
}

// listBotContent is the tree the page edits: the shop's where it has one,
// the code's where it does not, so there is always something to edit.
func (s *Server) listBotContent(c *gin.Context) {
	nodes, fromCode := s.treeJSON(c.Request.Context(), c.Param("slug"))
	c.JSON(http.StatusOK, gin.H{
		"nodes":     nodes,
		"from_code": fromCode,
		"root":      content.RootNode,
		"limits": gin.H{
			"buttons":      content.MaxButtons,
			"rows":         content.MaxRows,
			"nodes":        maxNodes,
			"title":        maxRowTitle,
			"button_title": maxButtonTitle,
			"description":  maxRowDesc,
		},
	})
}

type botTreeSaveRequest struct {
	Nodes []botNodeJSON `json:"nodes"`
}

// saveBotContent replaces the brand's whole tree in one go.
//
// THE WHOLE TREE, NOT A PATCH. Which nodes exist, which options are on them
// and the order of both are one decision: sending them piecemeal would leave
// the page unable to express "delete this" without a second call that can
// fail on its own, with a half-saved menu as the result.
func (s *Server) saveBotContent(c *gin.Context) {
	ctx := c.Request.Context()
	brand := c.Param("slug")

	var body botTreeSaveRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		detail(c, http.StatusUnprocessableEntity, "Send the bot's messages.")
		return
	}
	if len(body.Nodes) == 0 {
		detail(c, http.StatusUnprocessableEntity, "The bot needs at least one message.")
		return
	}
	if len(body.Nodes) > maxNodes {
		detail(c, http.StatusUnprocessableEntity,
			"That is more messages than this page can keep track of.")
		return
	}
	if msg := validateTree(body.Nodes); msg != "" {
		detail(c, http.StatusUnprocessableEntity, msg)
		return
	}

	by := currentUserID(c)
	// Cleared first, so a node or option removed in the page is removed
	// here. Options cascade from their node.
	if err := s.store.Q.DeleteWhatsAppBotTree(ctx, brand); err != nil {
		detail(c, http.StatusInternalServerError, "Could not save that just now.")
		return
	}
	for i, n := range body.Nodes {
		if err := s.store.Q.UpsertWhatsAppBotNode(ctx, gen.UpsertWhatsAppBotNodeParams{
			BrandSlug: brand, NodeKey: n.Key, Kind: n.Kind,
			Body: strings.TrimSpace(n.Body), ListButton: strings.TrimSpace(n.ListButton),
			Position: int32(i),
		}); err != nil {
			detail(c, http.StatusInternalServerError, "Could not save that just now.")
			return
		}
		for j, o := range n.Options {
			if err := s.store.Q.UpsertWhatsAppBotOption(ctx, gen.UpsertWhatsAppBotOptionParams{
				BrandSlug: brand, NodeKey: n.Key, OptionKey: o.Key,
				Title: strings.TrimSpace(o.Title), Description: strings.TrimSpace(o.Description),
				Action: o.Action, Answer: strings.TrimSpace(o.Answer),
				Target: strings.TrimSpace(o.Target), ButtonLabel: strings.TrimSpace(o.ButtonLabel),
				Position: int32(j), Enabled: o.Enabled, UpdatedBy: &by,
			}); err != nil {
				detail(c, http.StatusInternalServerError, "Could not save that just now.")
				return
			}
		}
	}

	s.listBotContent(c)
}

// validateTree returns the one sentence to show, or empty when it is sound.
func validateTree(nodes []botNodeJSON) string {
	keys := map[string]bool{}
	for _, n := range nodes {
		if strings.TrimSpace(n.Key) == "" {
			return "Every message needs an id."
		}
		if keys[n.Key] {
			return "Two messages share the same id."
		}
		keys[n.Key] = true
	}
	if !keys[content.RootNode] {
		return "The first message a customer gets cannot be deleted."
	}

	seenOption := map[string]string{}
	for _, n := range nodes {
		if msg := validateNode(n, keys, seenOption); msg != "" {
			return msg
		}
	}
	return ""
}

func validateNode(n botNodeJSON, nodes map[string]bool, seenOption map[string]string) string {
	if strings.TrimSpace(n.Body) == "" {
		return "A message needs something to say."
	}
	if utf8.RuneCountInString(n.Body) > maxBodyRunes {
		return "One of the messages is too long for WhatsApp."
	}

	list := n.Kind == string(content.NodeList)
	limit, titleCap := content.MaxButtons, maxButtonTitle
	if list {
		limit, titleCap = content.MaxRows, maxRowTitle
	}
	if len(n.Options) > limit {
		if list {
			return "A list shows at most 10 options. Move some onto another message."
		}
		// The sentence names the way out, because there is one: a list.
		return "WhatsApp shows at most 3 buttons under a message. " +
			"Switch this message to a list to offer more."
	}
	if list && utf8.RuneCountInString(strings.TrimSpace(n.ListButton)) > maxButtonTitle {
		return "The button that opens the list is longer than 20 characters."
	}
	if list && strings.TrimSpace(n.ListButton) == "" {
		return "A list needs a word on the button that opens it."
	}

	for _, o := range n.Options {
		if msg := validateOption(o, nodes, seenOption, list, titleCap); msg != "" {
			return msg
		}
	}
	return ""
}

func validateOption(
	o botOptionJSON, nodes map[string]bool, seenOption map[string]string,
	list bool, titleCap int,
) string {
	key, title := strings.TrimSpace(o.Key), strings.TrimSpace(o.Title)
	if key == "" || title == "" {
		return "An option needs a label."
	}
	if where, taken := seenOption[key]; taken {
		// Two options with one id answer each other's taps: WhatsApp sends
		// back only the id, so the bot cannot tell them apart.
		return "“" + title + "” has the same id as an option on " + where + "."
	}
	seenOption[key] = "another message"

	if utf8.RuneCountInString(title) > titleCap {
		if list {
			return "“" + title + "” is longer than WhatsApp allows in a list (24 characters)."
		}
		return "“" + title + "” is longer than WhatsApp allows on a button (20 characters)."
	}
	if utf8.RuneCountInString(strings.TrimSpace(o.Description)) > maxRowDesc {
		return "The note under “" + title + "” is longer than 72 characters."
	}

	action := content.ActionKind(o.Action)
	switch action {
	case content.ActionAnswer:
		if strings.TrimSpace(o.Answer) == "" {
			return "“" + title + "” sends a reply, so it needs something to say."
		}
		if utf8.RuneCountInString(o.Answer) > maxAnswerRunes {
			return "The reply for “" + title + "” is too long for one WhatsApp message."
		}
	case content.ActionOpen:
		if !nodes[strings.TrimSpace(o.Target)] {
			// A button that opens nothing is the quietest way for this page
			// to break a conversation.
			return "“" + title + "” opens a message that no longer exists."
		}
	case content.ActionURL:
		target := strings.TrimSpace(o.Target)
		if !strings.HasPrefix(target, "https://") && !strings.HasPrefix(target, "http://") {
			return "The link for “" + title + "” needs to start with https://."
		}
		if utf8.RuneCountInString(strings.TrimSpace(o.ButtonLabel)) > maxButtonTitle {
			return "The link button for “" + title + "” is longer than 20 characters."
		}
	case content.ActionDamage, content.ActionTrack, content.ActionTeam, content.ActionFeedback:
		// The state machine recognises these by id, so the id is not the
		// shop's to choose - the page sets it when the action is picked.
		if key != content.CodeActionID(action) {
			return "“" + title + "” has the wrong id for what it does. Pick its action again."
		}
	default:
		return "“" + title + "” does not say what it should do."
	}
	return ""
}
