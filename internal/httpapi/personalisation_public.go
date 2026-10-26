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
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

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
	// The same item drawn as a picture - what the page actually shows. See
	// personalisation_picture.go for why it is not drawn in the browser.
	g.GET("/items/:jobID/picture.png", s.publicPersonalisationPicture)
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
	// MaxLength is how many characters this product's design fits here.
	//
	// SENT, rather than left to the page, because it differs per product:
	// the plank fits twelve and the lotus holder sixteen, and the page had
	// the plank's number typed into it. A box that lets somebody type what
	// the server will refuse is a form that fails on Save for no visible
	// reason.
	MaxLength int `json:"max_length"`
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
	// Editable is whether THIS ITEM can still be changed, as against whether
	// the window is open.
	//
	// They come apart on an order that reached a printer before the link was
	// sent. SaveCustomerPersonalisation writes only to a job the hold
	// reached, and the hold does not touch a job on a locked or dispatched
	// bed - that plate is on its way to a machine. Without this the page
	// offered a field, a clock and a Save button that answered "this link is
	// no longer available", which reads as a broken page rather than as an
	// order already being made.
	Editable bool `json:"editable"`
	// HasPreview says whether a picture is coming AT ALL.
	//
	// Tensor renders the plank family and the products the registry
	// describes; it does not render a Bone Tag, a DUOSNAP or a Couple's Date
	// Plank, and nothing ever will produce a model for those. Without this
	// the page cannot tell "the render is still running" from "there will
	// never be one", so it showed "Building your preview..." forever on an
	// item whose fields were the only thing to check.
	HasPreview bool `json:"has_preview"`
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
		Colours:      s.colourChoicesFor(ctx, jobs),
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
		out.Items = append(out.Items, publicItem(
			j, s.fieldRulesFor(ctx, j),
			s.rendersProduct(ctx, deref(j.Sku), deref(j.ProductName)),
			s.shownModelFor(ctx, j)))
	}
	numberSiblings(out.Items)
	c.JSON(http.StatusOK, out)
}

// shownModelFor is the file this job's picture is drawn from, or nil when
// there is nothing to show yet.
//
// The preview model first and the print model second, which is exactly the
// order previewModelFor reads them in - so what the page SAYS is ready is
// what the picture route will actually serve.
func (s *Server) shownModelFor(ctx context.Context, j gen.ProductionJob) *uuid.UUID {
	if prev, err := s.store.Q.GetProductionJobPreviewFile(ctx, j.ID); err == nil {
		id := prev.ID
		return &id
	}
	return j.PrintFileID
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

// publicItem is one of the order's items as the customer's page reads it.
//
// shown is the file the picture will actually be drawn from: the preview
// model where the product has one, and the print model otherwise. It decides
// BOTH whether there is anything to show yet and which version is showing.
//
// It must not be j.PrintFileID, and that is not a detail. A product with a
// personalisation preview deliberately withholds its print model until the
// window closes, so reading that field leaves model_ready false for the whole
// thirty minutes - the page says "Building your preview..." and never stops,
// while the picture it is waiting for has been ready since before the message
// was sent.
func publicItem(
	j gen.ProductionJob, rules fieldRules, hasPreview bool, shown *uuid.UUID,
) publicItemResponse {
	item := publicItemResponse{
		JobID:        j.ID.String(),
		JobNumber:    j.JobNumber,
		ProductName:  deref(j.ProductName),
		Quantity:     int(j.Quantity),
		Colour:       deref(j.PersonalisationColour),
		HasPreview:   hasPreview,
		ModelReady:   shown != nil,
		ModelVersion: deref(uuidPtrStr(shown)),
		Part:         partLabel(j),
		// The one state the edit path accepts. Anything else means the job
		// has moved on, whatever the clock at the top of the page says.
		Editable: j.PersonalisationStatus == production.PersonalisationAwaitingCustomer,
		Fields:   make([]publicFieldResponse, 0, 4),
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
		// A contact detail is left out of the response entirely, not merely
		// locked: this page is a token in a link, and what a guessed one may
		// expose is a decision already taken. See contactProperty.
		if contactProperty(p.Name) {
			continue
		}
		item.Fields = append(item.Fields, publicFieldResponse{
			Label: p.Name, Value: p.Value,
			// Both have to be true: the field has to be one a customer may
			// set, and the job has to be one that can still accept a write.
			Editable:  item.Editable && rules.canEdit(p.Name, p.Value),
			MaxLength: rules.limitFor(p.Name),
		})
	}
	return item
}

// colourChoicesFor is the colour list THIS order's products actually sell.
//
// The picker used to offer the shop's whole palette whatever was on the page,
// so a Royal diya holder - which Shopify sells in purple, red and green -
// offered fifteen, and a customer could pick one that has never existed as a
// product. They would be choosing something the shop cannot send them.
//
// The variants answer it: PDHR-P, PDHR-R and PDHR-G are named Purple, Red and
// Green in the registry, and those names are the colourway. Falls back to the
// configured list for a product the registry does not describe, or whose
// variants are named something other than a colour - the plank's are "2
// hearts, With light" - so nothing that works today narrows.
func (s *Server) colourChoicesFor(
	ctx context.Context, jobs []gen.ProductionJob,
) []publicColourResponse {
	// One list for the page, so a two-item order does not show two pickers
	// disagreeing. The first job whose product names real colours wins; an
	// order mixing two such products is not a shape the storefront sells.
	for _, j := range jobs {
		sku := strings.TrimSpace(deref(j.Sku))
		if sku == "" {
			continue
		}
		product, err := s.store.Q.FindProductBySKU(ctx, sku)
		if err != nil {
			continue
		}
		variants, err := s.store.Q.ListVariantsForProduct(ctx, product.ID)
		if err != nil {
			continue
		}
		out := make([]publicColourResponse, 0, len(variants))
		seen := map[string]bool{}
		for _, v := range variants {
			if v.Status == "retired" {
				continue
			}
			// Only a name the renderer can resolve to a swatch. A variant
			// called "2 hearts, With light" is not a colourway, and offering
			// it would hold the job at "no swatch for ..." - a choice that
			// looks accepted and stops the order.
			name := strings.ToUpper(strings.TrimSpace(v.Name))
			hex := colourHexFor(name)
			if hex == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, publicColourResponse{Name: name, Hex: hex})
		}
		if len(out) > 0 {
			return out
		}
	}
	return s.sellableColours()
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
	ctx := c.Request.Context()
	link, ok := s.openLink(c)
	if !ok {
		return
	}
	job, ok := s.jobOnLink(c, link)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, publicItem(
		job, s.fieldRulesFor(ctx, job),
		s.rendersProduct(ctx, deref(job.Sku), deref(job.ProductName)),
		s.shownModelFor(ctx, job)))
}

// --- the model ----------------------------------------------------------------

func (s *Server) publicPersonalisationModel(c *gin.Context) {
	model, job, row, ok := s.previewModelFor(c)
	if !ok {
		return
	}
	_ = job
	// Inline, not an attachment: the browser hands these bytes to a 3D viewer
	// rather than to the downloads folder.
	c.Header("Content-Disposition", "inline")
	c.Header("Cache-Control", "private, max-age=60")
	c.Data(http.StatusOK, row.ContentType, model)
}

// previewModelFor loads the exact bytes a customer should be shown for one
// item: their model, with the hearts composed in.
//
// Shared by the 3MF route and the picture route so the two can never disagree
// about what the customer is approving. Every refusal has already been
// written to the response when this returns false.
func (s *Server) previewModelFor(
	c *gin.Context,
) (model []byte, job gen.ProductionJob, row gen.FileAsset, ok bool) {
	ctx := c.Request.Context()
	link, ok := s.openLink(c)
	if !ok {
		return nil, job, row, false
	}
	job, ok = s.jobOnLink(c, link)
	if !ok {
		return nil, job, row, false
	}
	if !s.filesReady(c) {
		return nil, job, row, false
	}

	// THE PICTURE'S OWN MODEL FIRST, when the product has one.
	//
	// The lotus holder's plate and its picture are different models: the diya
	// cup is a separate print, so the plate that goes to batching does not
	// carry it and the picture does, standing on the plate where the customer
	// will push it. Falling back to the print model is every other product,
	// whose picture has always been a picture of what prints.
	//
	// ASKED BEFORE THE PRINT FILE IS REQUIRED, not after. A product with a
	// preview withholds its print model for the whole thirty minutes, so a
	// guard on print_file_id ahead of this answers "still being built" to
	// every request for a picture that has been ready since before the
	// customer was messaged.
	shown := s.shownModelFor(ctx, job)
	if shown == nil {
		// Genuinely still rendering. 404 rather than an error: the page
		// polls, and this is the ordinary first answer.
		detail(c, http.StatusNotFound, "This preview is still being built.")
		return nil, job, row, false
	}
	fileID := *shown

	row, err := s.store.Q.GetFileAsset(ctx, fileID)
	if err != nil {
		detail(c, http.StatusNotFound, "This preview is still being built.")
		return nil, job, row, false
	}
	obj, err := s.storage.Get(ctx, row.StorageKey)
	if err != nil {
		if storage.IsNotFound(err) {
			detail(c, http.StatusNotFound, "This preview is still being built.")
			return nil, job, row, false
		}
		detail(c, http.StatusInternalServerError, "Could not read the preview.")
		return nil, job, row, false
	}
	defer func() { _ = obj.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(obj.Body, maxRecolourBytes))
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the preview.")
		return nil, job, row, false
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
	withHearts := heartedPreview(raw, hearts)
	if hearts > 0 && len(withHearts) == len(raw) {
		// Not fatal - the preview is still a true picture of what prints - but
		// it is the whole reason this product reserves that margin, so a plank
		// that quietly lost its heart should be findable.
		obs.FromContext(ctx).Warn("personalisation: the preview kept no heart",
			"job", job.ID, "hearts", hearts, "content_type", row.ContentType)
	}
	// AND THE REST OF THE PRODUCT.
	//
	// The hearts are a piece of the plank; the foot and the cup are pieces of
	// a diya holder, printed from their own files and attached at the bench.
	// Neither has ever reached the picture, so the customer approved an arch
	// plate with nothing to stand on. Same seam, same rule: composed on the
	// way out, and the file on disk is untouched.
	return s.assembledPreview(ctx, job, withHearts), job, row, true
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
	s.closePersonalisationWindow(ctx, link.OrderID, "customer")
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
