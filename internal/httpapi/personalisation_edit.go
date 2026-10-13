package httpapi

// What a customer may change about their own order, and what they may not.
//
// The distinction is not about trust. It is that some of these values are
// TEXT the model is engraved with, and others decide WHICH MODEL is made. A
// heart count is the second kind: on the plank path it selects a different
// .scad file, with different margins and a different plate thickness
// (templateForHearts). Letting it be edited would turn "fix my spelling" into
// "make me a different product", silently, at a price nobody agreed.
//
// So the rule is an allow-list, not a deny-list. Anything this file does not
// recognise as editable text is shown and left alone.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// maxCustomerFieldRunes bounds one edited value.
//
// Runes, not bytes: a name in Devanagari is multi-byte and clipping on bytes
// would cut a character in half. Twelve is the plank's own limit
// (personalise.MaxNameLen) and the tightest of the products here; a longer
// value simply does not fit the model, so it is refused in the form rather
// than thirty seconds later in a render that fails.
const maxCustomerFieldRunes = 12

// editableKeys are the property keys a customer may change.
//
// Taken from the same label lists the renderer matches on, so a field that is
// editable here is one the model will actually pick up. DNP is still on the
// hardcoded path and has no product_field_maps rows, which is why this cannot
// yet be derived from the registry for every product.
var editableKeys = map[string]bool{
	"step 4 first name":    true,
	"step 5 second name":   true,
	"first name":           true,
	"second name":          true,
	"left name":            true,
	"right name":           true,
	"name":                 true,
	"personalisation name": true,
	"custom name":          true,
	"engraving text":       true,
	"rose name":            true,
	"keychain name":        true,
}

// editableProperty reports whether a customer may change this field, from the
// hardcoded list alone.
//
// The fallback, for products the registry does not describe - DNP is still on
// the hardcoded render path and has no product_field_maps rows at all.
func editableProperty(label string) bool {
	return editableKeys[personalise.NormaliseKey(label)]
}

// editableFields asks the registry which of a job's properties the model
// actually reads.
//
// THE HARDCODED LIST IS NOT ENOUGH, and only real data showed it. It was
// written from the plank's own key lists - "step 4 first name", "rose name" -
// and a real Soulmate Combo asks "STEP 2 - First Name-", "STEP 4 - Name On 3D
// Rose", "STEP 5 - Name On Heart Keychain". Not one of them matched, so every
// field on a live order came back read-only: a page that looks like it works
// and lets nobody change anything.
//
// product_field_maps is the right source because it is the same table the
// RENDERER matches on, so a field editable here is one the model will pick up.
// Numeric and fixed-value fields are excluded: a number is a dimension or a
// count rather than a word, and a fixed value is the shop's decision, not the
// customer's.
//
// Returns nil when the product is not in the registry, and the caller falls
// back to the hardcoded list.
func (s *Server) editableFields(ctx context.Context, job gen.ProductionJob) map[string]bool {
	sku := strings.TrimSpace(deref(job.Sku))
	if sku == "" {
		return nil
	}
	product, err := s.store.Q.FindProductBySKU(ctx, sku)
	if err != nil {
		return nil
	}
	rows, err := s.store.Q.ListProductFieldMaps(ctx, gen.ListProductFieldMapsParams{
		ProductID: product.ID, Role: partRoleOf(job),
	})
	if err != nil || len(rows) == 0 {
		return nil
	}

	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		if r.ValueType == "number" || strings.TrimSpace(deref(r.FixedValue)) != "" {
			continue
		}
		// Stored already normalised (migration 0079), and normalised again on
		// the way in, so the two sides cannot drift.
		out[personalise.NormaliseKey(r.PropertyKey)] = true
	}
	return out
}

// canEdit answers for one field, registry first and the hardcoded list second.
func canEdit(registry map[string]bool, label string) bool {
	if registry != nil {
		return registry[personalise.NormaliseKey(label)]
	}
	return editableProperty(label)
}

// --- the request --------------------------------------------------------------

type personalisationEditRequest struct {
	// Fields keyed by the LABEL the customer was shown, which is what the page
	// renders and what the properties list stores.
	Fields map[string]string `json:"fields"`
	// Colour is the shop's own name for it - "GOLD" - not a hex. The hex the
	// page draws is for the swatch; the floor works in names, and resolving
	// the name to filament is already somebody else's job.
	Colour string `json:"colour"`
}

func (s *Server) publicPersonalisationEdit(c *gin.Context) {
	ctx := c.Request.Context()
	link, ok := s.openLink(c)
	if !ok {
		return
	}
	job, ok := s.jobOnLink(c, link)
	if !ok {
		return
	}

	var req personalisationEditRequest
	if !bindJSON(c, &req) {
		return
	}

	current, _ := jobLineProperties(job)
	updated, changed, err := applyCustomerEdit(current, req.Fields, s.editableFields(ctx, job))
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}

	colour := strings.TrimSpace(req.Colour)
	if colour != "" {
		// Only a colour the shop can actually print. Accepting anything else
		// would turn a confirmation into a stalled order: the job would hold
		// at errUnknownColour with the customer believing they had chosen.
		if colourHexFor(colour) == "" {
			detail(c, http.StatusUnprocessableEntity,
				"That colour is not one we can print. Please pick one from the list.")
			return
		}
		if !strings.EqualFold(colour, deref(job.PersonalisationColour)) {
			changed = true
		}
	} else {
		colour = deref(job.PersonalisationColour)
	}

	if !changed {
		// Nothing moved. Not an error, and deliberately not a write: an edit
		// that changes nothing must not stamp personalisation_original, or
		// every job a customer merely opened would read as "Changed".
		c.JSON(http.StatusOK, gin.H{"saved": false, "rendering": false})
		return
	}

	original, err := originalSnapshot(job, current)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save that just now.")
		return
	}
	properties, err := json.Marshal(updated)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save that just now.")
		return
	}

	rows, err := s.store.Q.SaveCustomerPersonalisation(ctx, gen.SaveCustomerPersonalisationParams{
		ID: job.ID, Properties: properties, Original: original,
		Colour:  nonEmptyPtr(colour),
		Colours: coloursJSON(colour),
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save that just now.")
		return
	}
	if rows == 0 {
		// The guard refused it: the window closed between the page loading and
		// Save being pressed, or this job has already gone to the floor.
		refusePublicLink(c)
		return
	}

	// The render is the slow half and the customer is watching it, so it goes
	// on the queue kept for exactly that - not the floor's.
	rendering := false
	if s.modelEnqueuer != nil {
		if err := s.modelEnqueuer.EnqueuePreview(ctx, job.ID); err != nil {
			// The values are saved either way, and that is what gets printed.
			// A preview that did not rebuild is a worse picture, not a wrong
			// product.
			obs.FromContext(ctx).Error("personalisation: could not queue the preview",
				"job", job.ID, "error", err)
		} else {
			rendering = true
		}
	}
	c.JSON(http.StatusOK, gin.H{"saved": true, "rendering": rendering})
}

// applyCustomerEdit folds the submitted values into the property list.
//
// The ORDER OF THE LIST IS PRESERVED, because it is the order the customer
// answered the questions in and the page renders it that way. Rebuilding the
// list from the submitted map would reorder it by whatever the browser sent.
func applyCustomerEdit(
	current []production.LineProp, fields map[string]string, registry map[string]bool,
) (updated []production.LineProp, changed bool, err error) {
	updated = make([]production.LineProp, 0, len(current))
	for _, p := range current {
		value, submitted := fields[p.Name]
		switch {
		case !submitted, p.Hidden(), !canEdit(registry, p.Name):
			// Not offered, or not editable. Kept exactly as it was - a field
			// the page did not let them change must not change because a
			// request said so.
			updated = append(updated, p)
			continue
		}

		value = oneLine(value)
		if strings.TrimSpace(value) == "" {
			return nil, false, errBlankField(p.Name)
		}
		if utf8.RuneCountInString(value) > maxCustomerFieldRunes {
			return nil, false, errFieldTooLong(p.Name)
		}
		if value != p.Value {
			changed = true
		}
		updated = append(updated, production.LineProp{Name: p.Name, Value: value})
	}
	return updated, changed, nil
}

// originalSnapshot is what the order said, for the first edit only.
//
// Built from the values as they are NOW, before this edit is applied - which
// is correct only because the query coalesces: on a second edit the column
// already holds the real original and this value is discarded.
func originalSnapshot(job gen.ProductionJob, current []production.LineProp) ([]byte, error) {
	return json.Marshal(personalisationSnapshot{
		Properties: current,
		Colour:     deref(job.PersonalisationColour),
	})
}

// coloursJSON is the bed-grouping colour set, which is not the same field as
// the personalisation colour and has to move with it.
//
// A job's colours decide which plate it can share, so a customer changing gold
// to black changes which bed it belongs on. Safe here only because the job is
// held out of batching until the window closes.
func coloursJSON(colour string) []byte {
	name := strings.TrimSpace(colour)
	if name == "" {
		return []byte("[]")
	}
	raw, err := json.Marshal([]string{strings.ToUpper(name)})
	if err != nil {
		return []byte("[]")
	}
	return raw
}

// errBlankField and errFieldTooLong are refused in the form rather than in a
// render thirty seconds later, where the failure names neither the field nor
// the reason.
func errBlankField(label string) error {
	return fmt.Errorf("%s cannot be empty", strings.TrimSpace(label))
}

func errFieldTooLong(label string) error {
	return fmt.Errorf("%s is too long - %d characters at most",
		strings.TrimSpace(label), maxCustomerFieldRunes)
}
