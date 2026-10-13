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
	"net/http"
	"sort"
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
	// ModelReady says whether there is something to show yet. The link is sent
	// the moment the order lands and a render is 20-45 seconds behind it, so
	// "not yet" is the ordinary first answer rather than a fault.
	ModelReady bool `json:"model_ready"`
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
		Colours:      sellableColours(),
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
		out.Items = append(out.Items, publicItem(j))
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
func numberSiblings(items []publicItemResponse) {
	counts := make(map[string]int, len(items))
	for _, item := range items {
		counts[item.ProductName]++
	}
	seen := make(map[string]int, len(items))
	for i := range items {
		name := items[i].ProductName
		if counts[name] < 2 {
			continue
		}
		seen[name]++
		items[i].UnitIndex = seen[name]
		items[i].UnitCount = counts[name]
	}
}

func publicItem(j gen.ProductionJob) publicItemResponse {
	item := publicItemResponse{
		JobID:       j.ID.String(),
		JobNumber:   j.JobNumber,
		ProductName: deref(j.ProductName),
		Quantity:    int(j.Quantity),
		Colour:      deref(j.PersonalisationColour),
		ModelReady:  j.PrintFileID != nil,
		Fields:      make([]publicFieldResponse, 0, 4),
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
			Label: p.Name, Value: p.Value, Editable: editableProperty(p.Name),
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
func sellableColours() []publicColourResponse {
	out := make([]publicColourResponse, 0, len(fallbackColours))
	for name, hex := range fallbackColours {
		out = append(out, publicColourResponse{
			Name: strings.ToUpper(name), Hex: strings.ToUpper(hex),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func colourHexFor(name string) string {
	if hex, ok := fallbackColours[strings.ToLower(strings.TrimSpace(name))]; ok {
		return strings.ToUpper(hex)
	}
	return ""
}

func secondsUntil(t time.Time) int {
	left := int(time.Until(t).Seconds())
	if left < 0 {
		return 0
	}
	return left
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

	// Inline, not an attachment: the browser hands these bytes to a 3D viewer
	// rather than to the downloads folder.
	c.Header("Content-Disposition", "inline")
	c.Header("Cache-Control", "private, max-age=60")
	c.DataFromReader(http.StatusOK, obj.Size, row.ContentType, obj.Body, nil)
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
