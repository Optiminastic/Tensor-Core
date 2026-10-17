package httpapi

// Editing what the bot says.
//
// WHATSAPP'S LIMITS ARE CHECKED HERE, not discovered against the live number.
// Meta refuses the WHOLE message when one row is too long and names neither
// the row nor the rule, so a menu button over 20 characters or a list row
// over 24 would simply stop the bot answering - with nothing in the dashboard
// to say why. Everything is bounded on the way in.

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
	maxMenuTitle   = 20
	maxFaqTitle    = 24
	maxFaqDesc     = 72
	maxFaqAnswer   = 4096
	maxFaqRows     = 10
	maxMenuButtons = 3
)

func (s *Server) registerWhatsAppBotContent(r *gin.Engine) {
	g := r.Group("/brands/:slug/whatsapp/bot-content")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.WhatsAppRead.Key()), s.listBotContent)
	g.PUT("", s.guards.RequirePermission(auth.WhatsAppReply.Key()), s.saveBotContent)
}

// listBotContent is what the page edits: the shop's rows where it has them,
// and the code's where it does not, so there is always something to edit.
func (s *Server) listBotContent(c *gin.Context) {
	ctx := c.Request.Context()
	brand := c.Param("slug")

	stored := map[string]gen.WhatsappBotContent{}
	if rows, err := s.store.Q.ListWhatsAppBotContent(ctx, brand); err == nil {
		for _, r := range rows {
			stored[r.Kind+"/"+r.ContentKey] = r
		}
	}

	menu := make([]botContentJSON, 0, len(content.MainMenu.Options))
	for i, o := range content.MainMenu.Options {
		if r, ok := stored[botContentMenu+"/"+o.ID]; ok {
			menu = append(menu, botContentRowJSON(r))
			continue
		}
		menu = append(menu, botContentJSON{
			Kind: botContentMenu, Key: o.ID, Title: o.Title,
			Position: int32(i), Enabled: true, FromCode: true, Locked: true,
		})
	}

	// The stored list replaces the code's wholesale when there is one, which
	// is the same rule the bot applies - otherwise the page would show a
	// question the customer can no longer be offered.
	var faq []botContentJSON
	for _, r := range stored {
		if r.Kind == botContentFAQ {
			faq = append(faq, botContentRowJSON(r))
		}
	}
	if len(faq) == 0 {
		answers := codeAnswers()
		for i, row := range content.FaqList.Rows {
			faq = append(faq, botContentJSON{
				Kind: botContentFAQ, Key: row.ID, Title: row.Title,
				Description: row.Description, Answer: answers[row.ID],
				Position: int32(i), Enabled: true, FromCode: true,
				// A row that starts a flow rather than printing an answer.
				Locked: answers[row.ID] == "",
			})
		}
	}
	sortByPosition(faq)

	c.JSON(http.StatusOK, gin.H{"menu": menu, "faq": faq, "limits": gin.H{
		"menu_title": maxMenuTitle, "faq_title": maxFaqTitle,
		"faq_description": maxFaqDesc, "faq_rows": maxFaqRows,
	}})
}

type botContentSaveRequest struct {
	Menu []botContentJSON `json:"menu"`
	Faq  []botContentJSON `json:"faq"`
}

// saveBotContent replaces the brand's wording in one go.
//
// THE WHOLE SET, NOT A PATCH. Order, and which questions exist at all, are
// one decision: sending rows individually would leave the page unable to
// express "delete this one" without a second call that could fail on its own.
func (s *Server) saveBotContent(c *gin.Context) {
	ctx := c.Request.Context()
	brand := c.Param("slug")

	var body botContentSaveRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		detail(c, http.StatusUnprocessableEntity, "Send the menu and FAQ rows.")
		return
	}
	if len(body.Menu) > maxMenuButtons {
		detail(c, http.StatusUnprocessableEntity,
			"WhatsApp shows at most 3 buttons under a message.")
		return
	}
	if len(body.Faq) > maxFaqRows {
		detail(c, http.StatusUnprocessableEntity,
			"WhatsApp shows at most 10 questions in a list.")
		return
	}

	rows := make([]gen.UpsertWhatsAppBotContentParams, 0, len(body.Menu)+len(body.Faq))
	by := currentUserID(c)
	seen := map[string]bool{}

	for i, m := range body.Menu {
		title := strings.TrimSpace(m.Title)
		if title == "" {
			detail(c, http.StatusUnprocessableEntity, "A button needs a label.")
			return
		}
		if utf8.RuneCountInString(title) > maxMenuTitle {
			detail(c, http.StatusUnprocessableEntity,
				"“"+title+"” is longer than WhatsApp allows on a button (20 characters).")
			return
		}
		rows = append(rows, gen.UpsertWhatsAppBotContentParams{
			BrandSlug: brand, Kind: botContentMenu, ContentKey: m.Key,
			Title: title, Position: int32(i), Enabled: true, UpdatedBy: &by,
		})
	}

	for i, f := range body.Faq {
		key := strings.TrimSpace(f.Key)
		title := strings.TrimSpace(f.Title)
		if key == "" || title == "" {
			detail(c, http.StatusUnprocessableEntity, "A question needs a title.")
			return
		}
		if seen[key] {
			// Two rows with one id answer each other's taps.
			detail(c, http.StatusUnprocessableEntity, "Two questions share the same id.")
			return
		}
		seen[key] = true
		if utf8.RuneCountInString(title) > maxFaqTitle {
			detail(c, http.StatusUnprocessableEntity,
				"“"+title+"” is longer than WhatsApp allows in a list (24 characters).")
			return
		}
		desc := strings.TrimSpace(f.Description)
		if utf8.RuneCountInString(desc) > maxFaqDesc {
			detail(c, http.StatusUnprocessableEntity,
				"The note under “"+title+"” is longer than 72 characters.")
			return
		}
		answer := strings.TrimSpace(f.Answer)
		if utf8.RuneCountInString(answer) > maxFaqAnswer {
			detail(c, http.StatusUnprocessableEntity,
				"The answer to “"+title+"” is too long for one WhatsApp message.")
			return
		}
		rows = append(rows, gen.UpsertWhatsAppBotContentParams{
			BrandSlug: brand, Kind: botContentFAQ, ContentKey: key,
			Title: title, Description: desc, Answer: answer,
			Position: int32(i), Enabled: f.Enabled, UpdatedBy: &by,
		})
	}

	// Cleared first, so a question removed in the page is removed here. The
	// upserts that follow put back everything that survived.
	existing, err := s.store.Q.ListWhatsAppBotContent(ctx, brand)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the current wording.")
		return
	}
	keep := map[string]bool{}
	for _, r := range rows {
		keep[r.Kind+"/"+r.ContentKey] = true
	}
	for _, old := range existing {
		if keep[old.Kind+"/"+old.ContentKey] {
			continue
		}
		if _, err := s.store.Q.DeleteWhatsAppBotContent(ctx, gen.DeleteWhatsAppBotContentParams{
			BrandSlug: brand, Kind: old.Kind, ContentKey: old.ContentKey,
		}); err != nil {
			detail(c, http.StatusInternalServerError, "Could not save that just now.")
			return
		}
	}
	for _, r := range rows {
		if _, err := s.store.Q.UpsertWhatsAppBotContent(ctx, r); err != nil {
			detail(c, http.StatusInternalServerError, "Could not save that just now.")
			return
		}
	}

	s.listBotContent(c)
}

func sortByPosition(rows []botContentJSON) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].Position < rows[j-1].Position; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}
