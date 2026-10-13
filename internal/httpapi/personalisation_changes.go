package httpapi

// What the customer changed, for the person who has to make it.
//
// Its own endpoint rather than a field on the job, because the answer is only
// wanted for the row somebody is looking at. The queue carries hundreds of
// jobs and a boolean each; the values behind that boolean are fetched when a
// tag is hovered.
//
// DIFFED ON READ, never stored as a diff. A stored diff is a third thing to
// keep in step with the two values it describes, and it still cannot answer
// "what does it say now" without them.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// personalisationSnapshot is what was stored before the first edit.
//
// The colour is carried beside the properties because it is not one of them -
// it lives in its own columns - and a snapshot of the properties alone would
// lose the change most likely to be made.
type personalisationSnapshot struct {
	Properties []production.LineProp `json:"properties"`
	Colour     string                `json:"colour"`
}

type personalisationChange struct {
	Label string `json:"label"`
	From  string `json:"from"`
	To    string `json:"to"`
}

type personalisationChangesResponse struct {
	Changed bool                    `json:"changed"`
	Changes []personalisationChange `json:"changes"`
}

func (s *Server) personalisationChanges(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	job, err := s.store.Q.GetProductionJobByID(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	changes := personalisationChangesFor(gen.ProductionJob(job))
	c.JSON(http.StatusOK, personalisationChangesResponse{
		Changed: len(changes) > 0, Changes: changes,
	})
}

// personalisationChangesFor lists every value the customer moved.
//
// Empty when nothing was edited, which is the ordinary case: the snapshot is
// written only on a first edit, so its absence is the answer rather than
// something to compute.
func personalisationChangesFor(j gen.ProductionJob) []personalisationChange {
	if len(j.PersonalisationOriginal) == 0 {
		return nil
	}
	var before personalisationSnapshot
	if err := json.Unmarshal(j.PersonalisationOriginal, &before); err != nil {
		return nil
	}

	now, _ := jobLineProperties(j)
	out := make([]personalisationChange, 0, 4)

	// Keyed on the LABEL the customer saw, not on a normalised key. The hover
	// card is read beside the order, and "STEP 4 - First Name" is what both
	// the customer and the person packing it are looking at.
	current := make(map[string]string, len(now))
	for _, p := range now {
		current[p.Name] = p.Value
	}
	for _, p := range before.Properties {
		if p.Hidden() {
			continue
		}
		if to, ok := current[p.Name]; ok && to != p.Value {
			out = append(out, personalisationChange{Label: p.Name, From: p.Value, To: to})
		}
	}

	// The colour, which is not a property.
	if after := strings.TrimSpace(deref(j.PersonalisationColour)); after != "" &&
		!strings.EqualFold(after, strings.TrimSpace(before.Colour)) {
		from := before.Colour
		if strings.TrimSpace(from) == "" {
			// A colour added where the order had none. Said as a word rather
			// than as an empty cell, which reads like missing data.
			from = "not set"
		}
		out = append(out, personalisationChange{Label: "Colour", From: from, To: after})
	}
	return out
}

// --- handing the link back to the floor ---------------------------------------

type personalisationLinkResponse struct {
	// URL is empty whenever there is nothing to give: no link was issued, the
	// window has closed, or this deployment has no encryption key and so never
	// kept a copy. One field, three causes, and the page says the same thing
	// for all of them - there is nothing to copy.
	URL string `json:"url"`
	// ExpiresAt lets the page say how long the thing it just copied will work.
	ExpiresAt string `json:"expires_at"`
}

// personalisationLink returns the live customer link for a job's order.
//
// WHILE THE WINDOW IS OPEN, AND NOT A MOMENT LONGER. The sealed copy is nulled
// when the window closes, so this cannot return a stale link even if somebody
// asks - and a staff member who copies a link knows it is the one the customer
// is looking at right now rather than one that quietly stopped working.
//
// production:read, the same gate as the table the button sits in. Whoever is
// making the thing is who takes the phone call about it, and they can already
// see the customer's name, order and personalisation on that row.
func (s *Server) personalisationLink(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	job, err := s.store.Q.GetProductionJobByID(ctx, id)
	if err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	if job.OrderID == nil {
		c.JSON(http.StatusOK, personalisationLinkResponse{})
		return
	}

	link, err := s.store.Q.GetOpenPersonalisationLinkForOrder(ctx, *job.OrderID)
	if err != nil || link.TokenSealed == nil || s.secrets == nil {
		c.JSON(http.StatusOK, personalisationLinkResponse{})
		return
	}
	token, err := s.secrets.Open(*link.TokenSealed)
	if err != nil || strings.TrimSpace(token) == "" {
		// A sealed value that will not open is reported as nothing rather than
		// as an error: the key has changed, which is an operator's problem and
		// not something the floor can act on mid-shift.
		c.JSON(http.StatusOK, personalisationLinkResponse{})
		return
	}

	out := personalisationLinkResponse{URL: personalisationLinkURL(s.cfg.FrontendURL, token)}
	if link.ExpiresAt.Valid {
		out.ExpiresAt = link.ExpiresAt.Time.UTC().Format(time.RFC3339)
	}
	c.JSON(http.StatusOK, out)
}
