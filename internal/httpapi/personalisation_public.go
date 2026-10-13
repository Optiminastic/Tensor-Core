package httpapi

// The customer's own page, and the only part of Tensor a stranger can reach.
//
// Everything else here is behind a bearer token minted for a signed-in member
// of staff. This group is behind a random string in a URL, because the person
// it is for has no account and will never have one - they bought a plank.
//
// What that costs, stated plainly, is that every handler below has to assume
// its caller is hostile:
//
//   - The token is the ONLY authority. Nothing is taken from the request about
//     which order it concerns - not a query parameter, not a header, not a job
//     id on its own. The link row names the order; the order names its jobs;
//     a job id that is not on that order does not exist as far as this group
//     is concerned.
//   - Every refusal is the same refusal, so probing learns nothing.
//   - Nothing here exposes an address, an email, a phone number or a price.
//     The customer already knows what they ordered; a stranger holding a
//     guessed token must learn as little as possible beyond that.

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

func (s *Server) registerPersonalisationPublic(r *gin.Engine) {
	// NO RequireUser. This is the exception in this codebase, and it is
	// deliberate and narrow - see the file comment.
	g := r.Group("/public/personalisation/:token")
	g.Use(s.publicRateLimit())

	g.GET("", s.publicPersonalisation)
	g.GET("/items/:jobID/model", s.publicPersonalisationModel)
	g.PATCH("/items/:jobID", s.publicPersonalisationEdit)
	// One item, for a page waiting on its own re-render. Cheaper than
	// re-reading the whole order every few seconds, and it is the only thing
	// changing.
	g.GET("/items/:jobID", s.publicPersonalisationItem)
	g.POST("/confirm", s.publicPersonalisationConfirm)
}

// --- what the page is given --------------------------------------------------

type publicFieldResponse struct {
	Label string `json:"label"`
	Value string `json:"value"`
	// Editable is false for anything the customer must not change. A heart
	// count is not a parameter - it selects a different model file - so
	// letting it be edited would quietly change which product is made.
	Editable bool `json:"editable"`
}

type publicItemResponse struct {
	JobID       string                `json:"job_id"`
	JobNumber   string                `json:"job_number"`
	ProductName string                `json:"product_name"`
	Quantity    int                   `json:"quantity"`
	Colour      string                `json:"colour"`
	ColourHex   string                `json:"colour_hex"`
	Fields      []publicFieldResponse `json:"fields"`
	// UnitIndex and UnitCount distinguish three of the same product from each
	// other. Zero when the order holds only one, so the page can say nothing
	// rather than "1 of 1".
	UnitIndex int `json:"unit_index"`
	UnitCount int `json:"unit_count"`
	// Part is which piece of a multi-part product this is - a Soulmate Combo
	// prints as a plank, a rose and a keychain. Empty for a product that is
	// one piece.
	Part string `json:"part"`
	// ModelReady says whether there is something to show yet. The link is sent
	// the moment the order lands and a render is 20-45 seconds behind it, so
	// "not yet" is the ordinary first answer rather than a fault.
	ModelReady bool `json:"model_ready"`
	// ModelVersion changes when the model does.
	//
	// It is the print file's id, and a re-render mints a NEW file rather than
	// overwriting the old one - so this is an exact signal rather than a
	// guess. The page needs it because "is there a model" is true both before
	// and after a save: without a version it would poll, see the old file
	// answer 200, and show the customer the colour they just changed away
	// from.
	ModelVersion string `json:"model_version"`
}

type publicColourResponse struct {
	Name string `json:"name"`
	Hex  string `json:"hex"`
}

type publicPersonalisationResponse struct {
	OrderNumber  string               `json:"order_number"`
	CustomerName string               `json:"customer_name"`
	ExpiresAt    string               `json:"expires_at"`
	SecondsLeft  int                  `json:"seconds_left"`
	Confirmed    bool                 `json:"confirmed"`
	Items        []publicItemResponse `json:"items"`
	// Colours the shop can actually print. Offering one it cannot would turn
	// a confirmation into a stalled order.
	Colours []publicColourResponse `json:"colours"`
}

func (s *Server) publicPersonalisation(c *gin.Context) {
	ctx := c.Request.Context()
	link, ok := s.openLink(c)
	if !ok {
		return
	}

	order, err := s.store.Q.GetOrderByID(ctx, link.OrderID)
	if err != nil {
		refusePublicLink(c)
		return
	}
	jobs, err := s.store.Q.ListProductionJobsForOrder(ctx, &link.OrderID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load this order just now.")
		return
	}

	// First open only; a refresh is not a new visit.
	if err := s.store.Q.MarkPersonalisationLinkOpened(ctx, link.ID); err != nil {
		// Not fatal. Failing to record that somebody looked must not stop
		// them looking.
		obs.FromContext(ctx).Warn("personalisation: could not record the open", "error", err)
	}

	out := publicPersonalisationResponse{
		OrderNumber:  order.OrderNumber,
		CustomerName: deref(order.CustomerName),
		Confirmed:    link.Status == "confirmed",
		Colours:      s.sellableColours(),
		Items:        make([]publicItemResponse, 0, len(jobs)),
	}
	if link.ExpiresAt.Valid {
		out.ExpiresAt = link.ExpiresAt.Time.UTC().Format(time.RFC3339)
		out.SecondsLeft = secondsUntil(link.ExpiresAt.Time)
	}
	for _, j := range jobs {
		if j.PersonalisationStatus == production.PersonalisationNotRequired &&
			len(j.PersonalisationProperties) <= 2 {
			// Nothing on this job was made to instructions, so there is
			// nothing for the customer to check.
			continue
		}
		out.Items = append(out.Items, publicItem(j, s.editableFields(ctx, j)))
	}
	numberSiblings(out.Items)
	c.JSON(http.StatusOK, out)
}

// numberSiblings labels repeats of the same product "1 of 3".
//
// Counted from what is actually on the page rather than from the line's
// quantity, because the two disagree exactly when it matters: a line of three
// becomes three separate jobs, and the quantity on each of them is one.
//
// Done after the list is built, in the order it is drawn, so the numbering
// matches what the customer reads down the screen.
// PART ROLES ARE NOT UNITS, and a live order proved it. A Soulmate Combo
// prints from three design files - a plank, a rose and a keychain - so one
// combo arrives as three jobs sharing a product name. Counting those as
// repeats labelled a single combo "1 of 3", "2 of 3", "3 of 3", which tells
// the customer they bought three of something they bought one of.
//
// So siblings are counted within a part: three combos are three planks, three
// roses and three keychains, and each set of three is numbered 1-3 while the
// three pieces of one combo are not numbered at all.
func numberSiblings(items []publicItemResponse) {
	// A struct key, not a joined string: a separator has to be a character
	// that can appear in neither a product name nor a part, and there is no
	// such character worth betting on.
	type sibling struct{ product, part string }
	key := func(item publicItemResponse) sibling {
		return sibling{item.ProductName, item.Part}
	}

	counts := make(map[sibling]int, len(items))
	for _, item := range items {
		counts[key(item)]++
	}
	seen := make(map[sibling]int, len(items))
	for i := range items {
		k := key(items[i])
		if counts[k] < 2 {
			continue
		}
		seen[k]++
		items[i].UnitIndex = seen[k]
		items[i].UnitCount = counts[k]
	}
}

func publicItem(j gen.ProductionJob, registry map[string]bool) publicItemResponse {
	item := publicItemResponse{
		JobID:        j.ID.String(),
		JobNumber:    j.JobNumber,
		ProductName:  deref(j.ProductName),
		Quantity:     int(j.Quantity),
		Colour:       deref(j.PersonalisationColour),
		ModelReady:   j.PrintFileID != nil,
		ModelVersion: deref(uuidPtrStr(j.PrintFileID)),
		Part:         partLabel(j),
		Fields:       make([]publicFieldResponse, 0, 4),
	}
	if item.Colour == "" {
		item.Colour = deref(j.Colour)
	}
	item.ColourHex = colourHexFor(item.Colour)

	props, _ := jobLineProperties(j)
	for _, p := range props {
		// Shopify's own plumbing - `_`-prefixed keys the storefront uses to
		// carry state - is not something a customer typed and not something
		// they should be shown.
		if p.Hidden() || strings.TrimSpace(p.Value) == "" {
			continue
		}
		item.Fields = append(item.Fields, publicFieldResponse{
			Label: p.Name, Value: p.Value, Editable: canEdit(registry, p.Name),
		})
	}
	return item
}

// sellableColours is the list the picker offers.
//
// Taken from the built-in table rather than from the spools currently loaded:
// a colour the shop sells is a colour it can order filament for, and offering
// only what happens to be on a printer this afternoon would narrow the
// customer's choice to the state of the floor at one moment.
func (s *Server) sellableColours() []publicColourResponse {
	out := make([]publicColourResponse, 0, len(s.cfg.PersonalisationColours))
	for _, name := range s.cfg.PersonalisationColours {
		hex := colourHexFor(name)
		if hex == "" {
			// A name the renderer cannot resolve is dropped rather than
			// offered. Showing it would let a customer pick a colour that
			// holds their own job at "no swatch for ..." - a choice that
			// looks accepted and stops the order.
			continue
		}
		out = append(out, publicColourResponse{Name: strings.ToUpper(name), Hex: hex})
	}
	// NOT SORTED. The order is the shop's, and the list is short enough that
	// where a colour sits is a deliberate choice rather than an accident of
	// the alphabet.
	return out
}

func colourHexFor(name string) string {
	if hex, ok := fallbackColours[strings.ToLower(strings.TrimSpace(name))]; ok {
		return strings.ToUpper(hex)
	}
	return ""
}

// partLabel names the piece, for a product that prints as several.
//
// Empty for the default role, so a one-piece product says nothing rather than
// "body" - a word that means something to the floor and nothing to a customer.
func partLabel(j gen.ProductionJob) string {
	role := strings.TrimSpace(j.PartRole)
	if role == "" || strings.EqualFold(role, "body") {
		return ""
	}
	// The registry stores these as identifiers - dnp_plank, heart_keychain -
	// which are the floor's words and the template's. A customer reading
	// "Heart_keychain" is being shown a database value.
	words := strings.FieldsFunc(role, func(r rune) bool { return r == '_' || r == '-' })
	for i, w := range words {
		if i == 0 && w != "" {
			words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
			continue
		}
		words[i] = strings.ToLower(w)
	}
	return strings.Join(words, " ")
}

func secondsUntil(t time.Time) int {
	left := int(time.Until(t).Seconds())
	if left < 0 {
		return 0
	}
	return left
}

func (s *Server) publicPersonalisationItem(c *gin.Context) {
	link, ok := s.openLink(c)
	if !ok {
		return
	}
	job, ok := s.jobOnLink(c, link)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, publicItem(job, s.editableFields(c.Request.Context(), job)))
}

// --- the model ----------------------------------------------------------------

func (s *Server) publicPersonalisationModel(c *gin.Context) {
	ctx := c.Request.Context()
	link, ok := s.openLink(c)
	if !ok {
		return
	}
	job, ok := s.jobOnLink(c, link)
	if !ok {
		return
	}
	if job.PrintFileID == nil {
		// Still rendering. 404 rather than an error: the page polls, and this
		// is the ordinary first answer.
		detail(c, http.StatusNotFound, "This preview is still being built.")
		return
	}
	if !s.filesReady(c) {
		return
	}

	row, err := s.store.Q.GetFileAsset(ctx, *job.PrintFileID)
	if err != nil {
		detail(c, http.StatusNotFound, "This preview is still being built.")
		return
	}
	obj, err := s.storage.Get(ctx, row.StorageKey)
	if err != nil {
		if storage.IsNotFound(err) {
			detail(c, http.StatusNotFound, "This preview is still being built.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not read the preview.")
		return
	}
	defer func() { _ = obj.Body.Close() }()

	model, err := io.ReadAll(io.LimitReader(obj.Body, maxRecolourBytes))
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the preview.")
		return
	}

	// THE HEART GOES IN HERE AND NOWHERE ELSE.
	//
	// A one- or two-heart plank is printed without its hearts - they are
	// separate pieces fitted into the margins the template reserves - so the
	// stored model has a blank end, and a customer asked to approve that is
	// being shown something they did not order. Composed on the way out, so
	// the file on disk stays the file the floor slices.
	//
	// Read rather than streamed for the same reason recolourModel reads: the
	// bytes have to be understood, not forwarded. Bounded by the same limit.
	hearts := heartsForPreview(job, row.RenderParams)
	withHearts := heartedPreview(model, hearts)
	if hearts > 0 && len(withHearts) == len(model) {
		// Not fatal - the preview is still a true picture of what prints - but
		// it is the whole reason this product reserves that margin, so a plank
		// that quietly lost its heart should be findable.
		obs.FromContext(ctx).Warn("personalisation: the preview kept no heart",
			"job", job.ID, "hearts", hearts, "content_type", row.ContentType)
	}

	// Inline, not an attachment: the browser hands these bytes to a 3D viewer
	// rather than to the downloads folder.
	c.Header("Content-Disposition", "inline")
	c.Header("Cache-Control", "private, max-age=60")
	c.Data(http.StatusOK, row.ContentType, withHearts)
}

// --- confirming ----------------------------------------------------------------

func (s *Server) publicPersonalisationConfirm(c *gin.Context) {
	ctx := c.Request.Context()
	link, ok := s.openLink(c)
	if !ok {
		return
	}

	// Released FIRST, confirmed second - the same order as the expiry sweep,
	// and for the same reason: the other way round leaves a link that reads
	// 'confirmed' while its jobs still wait for a customer who can no longer
	// reach them.
	by := "customer"
	if err := s.store.Q.ReleaseOrderPersonalisationToPrint(ctx,
		gen.ReleaseOrderPersonalisationToPrintParams{
			OrderID: &link.OrderID, ValidatedBy: &by,
		}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not confirm this just now.")
		return
	}
	if err := s.store.Q.ConfirmPersonalisationLink(ctx, link.ID); err != nil {
		detail(c, http.StatusInternalServerError, "Could not confirm this just now.")
		return
	}
	s.triggerBatchPlan(ctx)

	obs.FromContext(ctx).Info("personalisation confirmed by the customer",
		"brand", link.BrandSlug, "order", link.OrderID)
	c.JSON(http.StatusOK, gin.H{"confirmed": true})
}

// --- the gate ------------------------------------------------------------------

// openLink resolves the token, or refuses.
//
// ONE REFUSAL FOR EVERY CAUSE. Expired, already confirmed, never existed,
// malformed: the same status and the same sentence. Distinguishing them would
// tell somebody working through guesses which of them was a real token.
func (s *Server) openLink(c *gin.Context) (gen.PersonalisationLink, bool) {
	token := strings.TrimSpace(c.Param("token"))
	if token == "" {
		refusePublicLink(c)
		return gen.PersonalisationLink{}, false
	}
	link, err := s.store.Q.GetPersonalisationLinkByTokenHash(
		c.Request.Context(), hashPersonalisationToken(token))
	if err != nil {
		refusePublicLink(c)
		return gen.PersonalisationLink{}, false
	}
	// Judged here rather than in SQL, so that an expired link and an unknown
	// one come back through exactly the same branch.
	if !link.ExpiresAt.Valid || !time.Now().UTC().Before(link.ExpiresAt.Time) {
		refusePublicLink(c)
		return gen.PersonalisationLink{}, false
	}
	if link.Status == "expired" || link.Status == "failed" {
		refusePublicLink(c)
		return gen.PersonalisationLink{}, false
	}
	return link, true
}

// jobOnLink resolves a job id, but only one belonging to this link's order.
//
// The check is the whole point: without it, a token for any order would read
// any job in the database by id.
func (s *Server) jobOnLink(
	c *gin.Context, link gen.PersonalisationLink,
) (gen.ProductionJob, bool) {
	id, ok := parseUUIDParam(c, "jobID")
	if !ok {
		return gen.ProductionJob{}, false
	}
	job, err := s.store.Q.GetProductionJobByID(c.Request.Context(), id)
	if err != nil {
		refusePublicLink(c)
		return gen.ProductionJob{}, false
	}
	if job.OrderID == nil || *job.OrderID != link.OrderID {
		refusePublicLink(c)
		return gen.ProductionJob{}, false
	}
	return gen.ProductionJob(job), true
}

func refusePublicLink(c *gin.Context) {
	// 404, not 403: "you may not see this" confirms there is something here.
	detail(c, http.StatusNotFound, invalidLinkMessage)
}
