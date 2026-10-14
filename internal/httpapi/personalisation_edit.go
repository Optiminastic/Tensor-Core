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

// maxFreeTextRunes bounds a field on a product Tensor does not render.
//
// The twelve-rune cap above is the PLANK's: a longer name does not fit the
// model, so refusing it in the form beats failing a render. A product nobody
// renders has no such model to overflow - a Couple's Date Plank carries
// "10-07-2016" and a Bone Tag a ten-digit number, both close enough to twelve
// to be refused by accident - so the limit there is only against abuse of a
// public form.
const maxFreeTextRunes = 60

// fieldRules decides which of one job's properties the customer may change,
// and how long a value may be.
//
// THE ALLOW-LIST EXISTS BECAUSE AN EDIT CAN CHANGE WHICH MODEL IS MADE. A
// heart count picks a different .scad; a number is a dimension. That reasoning
// is about a product Tensor RENDERS. For one it does not - a Bone Tag, a
// DUOSNAP, a Couple's Date Plank - there is no template to re-select and no
// render to feed: a person reads these values and makes the thing by hand. The
// customer correcting their own spelling there is the entire point of the
// page, and an allow-list written from plank keys refuses every one of them.
//
// So those products edit freely, minus the two kinds of value that are not
// theirs to retype - see canEdit.
type fieldRules struct {
	// registry is the product's field map, nil when it has none.
	registry map[string]bool
	// open is true for a product Tensor does not render, where every text
	// property the customer filled in is theirs to correct.
	open     bool
	maxRunes int
}

// canEdit answers for one field.
func (r fieldRules) canEdit(label, value string) bool {
	// NEVER, whatever else says so. A contact detail is how the shop reaches
	// them rather than something printed, and this page is reachable by
	// anyone holding the link - see the note on contactProperty. A URL is an
	// upload's address, and a text box over it only offers the chance to
	// break it.
	if contactProperty(label) || looksLikeURL(value) {
		return false
	}
	if r.open {
		return true
	}
	if r.registry != nil {
		return r.registry[personalise.NormaliseKey(label)]
	}
	return editableProperty(label)
}

// fieldRulesFor reads the rules for one job.
func (s *Server) fieldRulesFor(ctx context.Context, job gen.ProductionJob) fieldRules {
	registry := s.editableFields(ctx, job)
	if registry != nil {
		return fieldRules{registry: registry, maxRunes: maxCustomerFieldRunes}
	}
	if s.rendersProduct(ctx, deref(job.Sku), deref(job.ProductName)) {
		// A rendered product with no field map: the plank, still on the
		// hardcoded path. Its keys are the ones editableKeys was written from.
		return fieldRules{maxRunes: maxCustomerFieldRunes}
	}
	return fieldRules{open: true, maxRunes: maxFreeTextRunes}
}

// contactWords name a way of reaching somebody rather than something printed.
//
// Matched as whole words against the normalised label, NOT as substrings, and
// that distinction is the whole care in this list: a Bone Tag asks for
// "number-1" and engraves it on the tag, because a pet tag carries its owner's
// phone number - that one IS the personalisation. "Whatsapp Number", on the
// DUOSNAP, is not: it is how the shop reaches the buyer.
var contactWords = map[string]bool{
	"whatsapp": true, "email": true, "e mail": true, "mail": true,
	"phone": true, "mobile": true, "contact": true, "telephone": true,
}

// contactProperty reports whether a field is a contact detail, which is never
// shown on the public page at all.
//
// The page is a token in a link. What a guessed one may expose was decided
// when it was built - the order number, the product names, the personalisation
// text and the models, and deliberately no address, no payment and no contact
// details. A phone number rendered into the form would quietly break that, so
// it is dropped before the response is built rather than merely locked.
func contactProperty(label string) bool {
	for _, word := range strings.Fields(personalise.NormaliseKey(label)) {
		if contactWords[word] {
			return true
		}
	}
	return false
}

// looksLikeURL reports whether a value is a link rather than something typed.
//
// The DUOSNAP's PHOTO property is the address of the image the customer
// uploaded at checkout. It is theirs and it is shown, but it is not text to
// retype: an edit here can only turn a working link into a broken one.
func looksLikeURL(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")
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
	updated, changed, err := applyCustomerEdit(current, req.Fields, s.fieldRulesFor(ctx, job))
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}

	colourChanged := false
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
			colourChanged = true
		}
	} else {
		colour = deref(job.PersonalisationColour)
	}

	if !changed && !colourChanged {
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
		Colour: nonEmptyPtr(colour),
		// Only when there is one to write. See the note on the query: a job
		// whose colours came from its uploaded model has no
		// personalisation_colour, and overwriting with an empty list took it
		// out of colour grouping entirely.
		SetColours: strings.TrimSpace(colour) != "",
		Colours:    coloursJSON(colour),
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

	// A COLOUR ON ITS OWN NEEDS NO RENDER. Not one triangle moves: the plank
	// is the same plank and the lettering the same lettering, so the model is
	// read back, the hex is swapped on the pieces that follow the customer,
	// and it is written out again. That is the edit people make most, and it
	// is the difference between a moment and most of a minute.
	//
	// Attempted only when the text is untouched. A name change really is
	// different geometry, and the render it needs applies the new colour on
	// the way through anyway.
	if !changed && colourChanged {
		if s.recolourModel(ctx, job, colour) {
			c.JSON(http.StatusOK, gin.H{"saved": true, "rendering": false})
			return
		}
		// Fell through - no model yet, an STL, a design whose pieces are all
		// fixed. The render below is the answer for all of them.
	}

	// The render is the slow half and the customer is watching it, so it goes
	// on the queue kept for exactly that - not the floor's.
	//
	// ONLY FOR A PRODUCT TENSOR RENDERS. A Bone Tag or a Couple's Date Plank
	// was being queued too, where GenerateModelForJob returns
	// errNotPersonalisable and the worker shrugs - a wasted round trip that
	// also left the job holding model_render_queued_at for a rebuild that was
	// never going to happen.
	rendering := false
	if s.rendersProduct(ctx, deref(job.Sku), deref(job.ProductName)) && s.modelEnqueuer != nil {
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
	if !rendering {
		// SaveCustomerPersonalisation stamped the hold on the way in, because
		// it cannot know whether a render will follow. Nothing is coming, so
		// release it here rather than leaving the job out of batching until
		// the sweep notices.
		if err := s.store.Q.ClearJobModelRenderHold(ctx, job.ID); err != nil {
			obs.FromContext(ctx).Warn("personalisation: could not release the render hold",
				"job", job.ID, "error", err)
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
	current []production.LineProp, fields map[string]string, rules fieldRules,
) (updated []production.LineProp, changed bool, err error) {
	updated = make([]production.LineProp, 0, len(current))
	for _, p := range current {
		value, submitted := fields[p.Name]
		switch {
		case !submitted, p.Hidden(), !rules.canEdit(p.Name, p.Value):
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
		if utf8.RuneCountInString(value) > rules.maxRunes {
			return nil, false, errFieldTooLong(p.Name, rules.maxRunes)
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

// The limit is passed in rather than read from the constant: it differs by
// product, and a message naming twelve while the form accepts sixty is worse
// than no message.
func errFieldTooLong(label string, max int) error {
	return fmt.Errorf("%s is too long - %d characters at most",
		strings.TrimSpace(label), max)
}
