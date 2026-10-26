package httpapi

// Which slicer pipeline each SKU prints with, per machine class.
//
// Settings used to be chosen by printer model alone: pipelineForModel kept the
// pipelines whose target class matched and took the first. So a Dual Name Plank
// and a heart keychain sent to the same H2C were sliced identically, and with
// two H2C pipelines on the floor, which one they got was whichever BambuBuddy
// happened to list first.
//
// The mapping lives here and the settings stay in BambuBuddy. Tensor stores a
// pipeline id and the name it had, never a preset: the slicer configuration has
// one home, and duplicating it is how two sources of truth start disagreeing.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// pipelineOption is one of BambuBuddy's pipelines, for the mapping dropdowns.
type pipelineOption struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// MachineFamily is the class this pipeline is targeted at, empty when it
	// targets none. The dropdown for a class offers only its own, because a
	// P2S pipeline filed under H2C would slice every plank for the wrong bed.
	MachineFamily string `json:"machine_family"`
}

// pipelineTargetRow is one thing that can carry slicer settings: a product,
// whose SKUs all inherit them, or a lone SKU that belongs to no product.
type pipelineTargetRow struct {
	// ShopifyProductID is the parent every printing SKU sits under, whether or
	// not the registry has caught up with it.
	ShopifyProductID string `json:"shopify_product_id"`
	// ProductCode is set on a registry product row, empty otherwise.
	ProductCode string `json:"product_code"`
	Name        string `json:"name"`
	// SKU is set on a lone SKU row, empty on a product.
	SKU string `json:"sku"`
	// SKUCount is how many colourways inherit a product's setting.
	SKUCount  int                          `json:"sku_count"`
	Pipelines map[string]skuPipelineChoice `json:"pipelines"`
}

type pipelineTargetsResponse struct {
	// Products first: assigning one covers every SKU under it.
	Products []pipelineTargetRow `json:"products"`
	// SKUs that belong to no registry product. They cannot be reached by a
	// parent, so they are listed on their own - and most of what prints today
	// is in here, because the registry is behind the floor.
	Loose     []pipelineTargetRow `json:"loose"`
	Pipelines []pipelineOption    `json:"pipelines"`
	Families  []string            `json:"families"`
}

// skuPipelineRow is one SKU and what it is mapped to, per class.
type skuPipelineRow struct {
	SKU string `json:"sku"`
	// ProductCode and ProductName are best-effort context, and often empty:
	// most SKUs that print have no variant row in the registry, so the only
	// name available is the one the order line carried.
	ProductCode string `json:"product_code"`
	ProductName string `json:"product_name"`
	// Pipelines is keyed by machine family: {"H2C": {...}}. A class with no
	// entry uses the class default, which is what every SKU did before this.
	Pipelines map[string]skuPipelineChoice `json:"pipelines"`
}

type skuPipelineChoice struct {
	PipelineID int    `json:"pipeline_id"`
	Name       string `json:"pipeline_name"`
	// Missing marks a mapping whose pipeline BambuBuddy no longer has. Shown
	// rather than silently dropped: this is the state that makes a bed refuse
	// to slice, and the page is where it gets fixed.
	Missing bool `json:"missing"`
}

type skuPipelinesResponse struct {
	SKUs      []skuPipelineRow `json:"skus"`
	Pipelines []pipelineOption `json:"pipelines"`
	// Families is the machine classes a SKU can be mapped for, so the table's
	// columns come from the same place the batching rule does.
	Families []string `json:"families"`
}

// machineFamilies is the classes a mapping may be filed under.
//
// The three the shop runs, from production.BedFamilyForUnits' own vocabulary,
// so a column here cannot name a class the router has never heard of.
func machineFamilies() []string { return []string{"H2C", "A2L", "P2S"} }

func (s *Server) registerRegistryPipelines(g *gin.RouterGroup, read, manage gin.HandlerFunc) {
	g.GET("/sku-pipelines", read, s.listSKUPipelines)
	// Products, and the SKUs that belong to none. What the Slicing tab reads.
	g.GET("/pipeline-targets", read, s.listPipelineTargets)
	g.PUT("/sku-pipelines", manage, s.putSKUPipelines)
}

func (s *Server) pipelineOptions(ctx context.Context) ([]pipelineOption, error) {
	if !s.bambu.Configured() {
		return nil, errBambuNotConfigured
	}
	pipelines, err := s.bambu.ListPipelines(ctx)
	if err != nil {
		return nil, errPipelinesUnreadable
	}
	out := make([]pipelineOption, 0, len(pipelines))
	for _, p := range pipelines {
		out = append(out, pipelineOption{
			ID: p.ID, Name: p.Name, MachineFamily: deref(p.TargetModelClass),
		})
	}
	return out, nil
}

// pipelineOptionsBounded is pipelineOptions with a deadline of its own.
//
// BEST-EFFORT MEANS BOUNDED. The mappings are Tensor's and live in Postgres;
// only the pipeline NAMES and the "missing" flag come from BambuBuddy, so the
// page is worth showing without them. Saying that and then waiting on a dead
// host is not best-effort - a TCP connect to an unreachable box takes about
// twenty seconds here, and the browser gives up at fifteen, so the whole page
// failed over a decoration.
func (s *Server) pipelineOptionsBounded(ctx context.Context) []pipelineOption {
	bounded, cancel := context.WithTimeout(ctx, pipelineOptionsTimeout)
	defer cancel()
	options, err := s.pipelineOptions(bounded)
	if err != nil {
		obs.FromContext(ctx).Warn("listing slicer pipelines failed; showing the table without them",
			"error", err)
		return nil
	}
	return options
}

// Comfortably inside the frontend's own 15s, so the page renders rather than
// erroring when BambuBuddy is slow or gone.
const pipelineOptionsTimeout = 5 * time.Second

// parentTitle is a product's own name, taken from one of its line names.
//
// An order line is named "<product> - <variant>" - "Dual Name Plank - RED /
// NO LIGHT" - so the last " - " separates the two. LastIndex, not Index, so a
// product whose own name contains one keeps it: "Personalized Wall Diya Holder
// - Lotus - Red" is the Lotus, not the Wall Diya Holder.
//
// The same rule matchesProduct already uses, and a heuristic either way: the
// alternative is asking Shopify for every title to render a list that is
// otherwise free.
func parentTitle(lineName string) string {
	name := strings.TrimSpace(lineName)
	if i := strings.LastIndex(name, " - "); i > 0 {
		return strings.TrimSpace(name[:i])
	}
	return name
}

// resolvePipeline picks the slicer settings one SKU prints with.
//
// The SKU's own rule wins; otherwise the product it belongs to. A product with
// forty-four colourways is one assignment rather than forty-four, and the one
// colour that genuinely prints differently still says so for itself.
//
// Kept here rather than in the SQL because it is the rule, and a rule nothing
// can assert without a database is a rule nobody checks. Its siblings
// resolveScheme and resolveBom sit the same way.
func resolvePipeline(own, inherited *gen.SkuSlicerPipeline) *gen.SkuSlicerPipeline {
	if own != nil {
		return own
	}
	return inherited
}

// listSKUPipelines answers with every SKU and its mapping, in one read.
//
// Every SKU, including the unmapped ones: "not mapped yet" is the state
// somebody opens this page to fix, and omitting those rows would hide exactly
// the work to be done.
func (s *Server) listSKUPipelines(c *gin.Context) {
	if numbered, paged, ok := parseOffsetPage(c); !ok {
		return
	} else if paged {
		s.listSKUPipelinesTable(c, numbered)
		return
	}
	ctx := c.Request.Context()
	rows, err := s.store.Q.ListSKUPipelines(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the SKU list.")
		return
	}

	// Best-effort: the table is still worth showing when BambuBuddy is
	// unreachable - the mappings are Tensor's, and only the names and the
	// "missing" flag come from over there.
	options := s.pipelineOptionsBounded(ctx)
	c.JSON(http.StatusOK, skuPipelinesResponse{
		SKUs: foldSKUPipelines(rows, options), Pipelines: options, Families: machineFamilies(),
	})
}

// listPipelineTargets answers with the PRODUCTS settings are assigned to, and
// the SKUs that belong to none.
//
// A product is the unit somebody thinks in: the Lotus prints the same way in
// three colours and the plank in forty-four. The loose list exists because the
// registry is behind the floor - of the SKUs printing today most match no
// variant row, and a page that showed only products would silently drop them.
func (s *Server) listPipelineTargets(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := s.store.Q.ListShopifyProductPipelines(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the products.")
		return
	}
	options := s.pipelineOptionsBounded(ctx)
	known := make(map[int]bool, len(options))
	for _, o := range options {
		known[o.ID] = true
	}

	byID := map[string]int{}
	out := make([]pipelineTargetRow, 0, len(rows))
	for _, r := range rows {
		id := r.ShopifyProductID
		at, seen := byID[id]
		if !seen {
			at = len(out)
			byID[id] = at
			out = append(out, pipelineTargetRow{
				ShopifyProductID: id, Name: parentTitle(r.Name),
				SKUCount: int(r.SkuCount), Pipelines: map[string]skuPipelineChoice{},
			})
		}
		addChoice(out[at].Pipelines, r.MachineFamily, r.PipelineID, r.PipelineName, known)
	}

	c.JSON(http.StatusOK, pipelineTargetsResponse{
		Products: out, Loose: []pipelineTargetRow{},
		Pipelines: options, Families: machineFamilies(),
	})
}

// addChoice records one class's mapping, marking a pipeline BambuBuddy no
// longer lists rather than letting it read as a working setting.
func addChoice(
	into map[string]skuPipelineChoice,
	family *string, id *int32, name *string, known map[int]bool,
) {
	if family == nil || id == nil {
		return
	}
	into[strings.ToUpper(*family)] = skuPipelineChoice{
		PipelineID: int(*id), Name: deref(name),
		Missing: len(known) > 0 && !known[int(*id)],
	}
}

// foldSKUPipelines turns one-row-per-mapping into one row per SKU, marking a
// mapping whose pipeline BambuBuddy no longer lists.
func foldSKUPipelines(rows []gen.ListSKUPipelinesRow, options []pipelineOption) []skuPipelineRow {
	known := make(map[int]bool, len(options))
	for _, o := range options {
		known[o.ID] = true
	}

	index := map[string]int{}
	out := make([]skuPipelineRow, 0, len(rows))
	for _, r := range rows {
		at, seen := index[r.Sku]
		if !seen {
			at = len(out)
			index[r.Sku] = at
			out = append(out, skuPipelineRow{
				SKU: r.DisplaySku, ProductCode: r.ProductCode, ProductName: r.ProductName,
				Pipelines: map[string]skuPipelineChoice{},
			})
		}
		if r.MachineFamily == nil || r.PipelineID == nil {
			continue // a SKU with no mapping at all
		}
		out[at].Pipelines[strings.ToUpper(*r.MachineFamily)] = skuPipelineChoice{
			PipelineID: int(*r.PipelineID),
			Name:       deref(r.PipelineName),
			Missing:    len(known) > 0 && !known[int(*r.PipelineID)],
		}
	}
	return out
}

// skuPipelinesPage is one page of SKUs, with the pipelines and families the
// panel's dropdowns and columns are drawn from.
type skuPipelinesPage struct {
	pageEnvelope[skuPipelineRow]
	Pipelines []pipelineOption `json:"pipelines"`
	Families  []string         `json:"families"`
}

// listSKUPipelinesTable answers ?page=N: one page of SKUs, counted and cut in
// Postgres over SKUs, searched by sku, product name and product code.
func (s *Server) listSKUPipelinesTable(c *gin.Context, p offsetPage) {
	ctx := c.Request.Context()
	search := searchParam(c)
	total, err := s.store.Q.CountSKUPipelinesTable(ctx, search)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the SKU list.")
		return
	}
	p = p.within(total)
	page, err := s.store.Q.ListSKUPipelinesTable(ctx, gen.ListSKUPipelinesTableParams{
		Search: search, PageLimit: p.Size, PageOffset: p.Offset(),
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the SKU list.")
		return
	}
	rows := make([]gen.ListSKUPipelinesRow, len(page))
	for i, r := range page {
		rows[i] = gen.ListSKUPipelinesRow(r)
	}
	options, _ := s.pipelineOptions(ctx)
	c.JSON(http.StatusOK, skuPipelinesPage{
		pageEnvelope: newPageEnvelope(foldSKUPipelines(rows, options), total, p),
		Pipelines:    options, Families: machineFamilies(),
	})
}

type skuPipelineWrite struct {
	// Exactly one of these. ProductCode assigns the whole product, which every
	// one of its SKUs inherits; SKU overrides it for one colourway.
	SKU         string `json:"sku" binding:"max=128"`
	ProductCode string `json:"product_code" binding:"max=32"`
	// ShopifyProductID is the parent the Slicing page assigns to: every SKU
	// sold under it inherits the setting.
	ShopifyProductID string `json:"shopify_product_id" binding:"max=64"`

	MachineFamily string `json:"machine_family" binding:"required,max=16"`
	// PipelineID of 0 clears the mapping, returning the SKU to the class
	// default. A real choice, so it is spelled rather than left to absence.
	PipelineID int `json:"pipeline_id"`
}

type putSKUPipelinesRequest struct {
	Mappings []skuPipelineWrite `json:"mappings"`
}

// putSKUPipelines writes a set of mappings at once.
//
// A SET, not one row: mapping twenty SKUs to one pipeline is the job this page
// exists for, and twenty round trips would each be able to fail on their own.
func (s *Server) putSKUPipelines(c *gin.Context) {
	var req putSKUPipelinesRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	options, err := s.pipelineOptions(ctx)
	if err != nil {
		detail(c, http.StatusBadGateway, err.Error())
		return
	}
	byID := make(map[int]pipelineOption, len(options))
	for _, o := range options {
		byID[o.ID] = o
	}

	for _, m := range req.Mappings {
		sku := strings.TrimSpace(m.SKU)
		code := strings.TrimSpace(m.ProductCode)
		shopID := strings.TrimSpace(m.ShopifyProductID)
		named := 0
		for _, v := range []string{sku, code, shopID} {
			if v != "" {
				named++
			}
		}
		if named != 1 {
			detail(c, http.StatusUnprocessableEntity,
				"Each mapping names exactly one of a Shopify product, a registry product, or a SKU.")
			return
		}
		family := strings.ToUpper(strings.TrimSpace(m.MachineFamily))
		if !validMachineFamily(family) {
			detail(c, http.StatusUnprocessableEntity,
				"A mapping names a machine class Tensor does not run: "+m.MachineFamily)
			return
		}

		// A product is addressed by code, so it is resolved before anything is
		// written - a typo must refuse rather than create a mapping nothing
		// will ever read.
		var productID *uuid.UUID
		if code != "" {
			product, err := s.store.Q.GetProductByCode(ctx, code)
			if err != nil {
				detail(c, http.StatusUnprocessableEntity,
					"No product is registered under "+code+".")
				return
			}
			productID = &product.ID
		}

		if m.PipelineID <= 0 {
			if shopID != "" {
				if err := s.store.Q.DeleteShopifyProductPipeline(ctx,
					gen.DeleteShopifyProductPipelineParams{
						ShopifyProductID: &shopID, MachineFamily: family,
					}); err != nil {
					detail(c, http.StatusInternalServerError, "Could not clear a mapping.")
					return
				}
				continue
			}
			if productID != nil {
				if err := s.store.Q.DeleteProductPipeline(ctx, gen.DeleteProductPipelineParams{
					ProductID: productID, MachineFamily: family,
				}); err != nil {
					detail(c, http.StatusInternalServerError, "Could not clear a mapping.")
					return
				}
				continue
			}
			if err := s.store.Q.DeleteSKUPipeline(ctx, gen.DeleteSKUPipelineParams{
				Sku: sku, MachineFamily: family,
			}); err != nil {
				detail(c, http.StatusInternalServerError, "Could not clear a mapping.")
				return
			}
			continue
		}

		pipeline, ok := byID[m.PipelineID]
		if !ok {
			detail(c, http.StatusUnprocessableEntity,
				"BambuBuddy has no pipeline with that id any more. Reload the page and pick again.")
			return
		}
		// A pipeline targeted at another class would slice every plate on this
		// SKU for the wrong bed. Caught here because this is the only place the
		// mistake is cheap - after it, the evidence is a ruined plate.
		if target := strings.ToUpper(strings.TrimSpace(pipeline.MachineFamily)); target != "" && target != family {
			detail(c, http.StatusUnprocessableEntity, fmt.Sprintf(
				"%q is a %s pipeline, so it cannot be used for %s.", pipeline.Name, target, family))
			return
		}

		if shopID != "" {
			if _, err := s.store.Q.UpsertShopifyProductPipeline(ctx,
				gen.UpsertShopifyProductPipelineParams{
					ID: uuid.New(), ShopifyProductID: &shopID, MachineFamily: family,
					PipelineID: int32(m.PipelineID), PipelineName: pipeline.Name,
				}); err != nil {
				detail(c, http.StatusInternalServerError, "Could not save a mapping.")
				return
			}
			continue
		}
		if productID != nil {
			if _, err := s.store.Q.UpsertProductPipeline(ctx, gen.UpsertProductPipelineParams{
				ID: uuid.New(), ProductID: productID, MachineFamily: family,
				PipelineID: int32(m.PipelineID), PipelineName: pipeline.Name,
			}); err != nil {
				detail(c, http.StatusInternalServerError, "Could not save a mapping.")
				return
			}
			continue
		}
		if _, err := s.store.Q.UpsertSKUPipeline(ctx, gen.UpsertSKUPipelineParams{
			ID: uuid.New(), Sku: &sku, MachineFamily: family,
			PipelineID: int32(m.PipelineID), PipelineName: pipeline.Name,
		}); err != nil {
			detail(c, http.StatusInternalServerError, "Could not save a mapping.")
			return
		}
	}

	s.listPipelineTargets(c)
}

func validMachineFamily(family string) bool {
	for _, f := range machineFamilies() {
		if f == family {
			return true
		}
	}
	return false
}

var (
	errBambuNotConfigured  = bambubuddy.ReasonError{Reason: "BambuBuddy is not configured on this service."}
	errPipelinesUnreadable = bambubuddy.ReasonError{Reason: "Could not read BambuBuddy's slicer pipelines."}
)

// pipelineForSKU is the SKU's own rule, or the one its product sets.
//
// The product is asked only when the SKU has none, so the common path is the
// single query it has always been. It runs in a loop over every job on a bed.
func (s *Server) pipelineForSKU(
	ctx context.Context, sku, family string,
) *gen.SkuSlicerPipeline {
	var own *gen.SkuSlicerPipeline
	if row, err := s.store.Q.GetPipelineForSKU(ctx, gen.GetPipelineForSKUParams{
		Sku: sku, MachineFamily: family,
	}); err == nil {
		own = &row
	}
	if own != nil {
		return resolvePipeline(own, nil)
	}
	// The registry product first, then the Shopify one every SKU has. Asked in
	// that order because a registry product is the narrower statement: it
	// describes what Tensor knows it makes, while the Shopify parent is
	// whatever the storefront sold it under.
	if row, err := s.store.Q.GetPipelineForProductOfSKU(ctx,
		gen.GetPipelineForProductOfSKUParams{Sku: sku, MachineFamily: family}); err == nil {
		return resolvePipeline(own, &row)
	}
	var inherited *gen.SkuSlicerPipeline
	if row, err := s.store.Q.GetPipelineForShopifyProductOfSKU(ctx,
		gen.GetPipelineForShopifyProductOfSKUParams{
			Sku: sku, MachineFamily: family,
		}); err == nil {
		inherited = &row
	}
	return resolvePipeline(own, inherited)
}
