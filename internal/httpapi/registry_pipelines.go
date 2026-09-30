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

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
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
	g.GET("/slicer-pipelines", read, s.listSlicerPipelines)
	g.GET("/sku-pipelines", read, s.listSKUPipelines)
	g.PUT("/sku-pipelines", manage, s.putSKUPipelines)
}

// listSlicerPipelines passes BambuBuddy's pipelines through for the dropdowns.
func (s *Server) listSlicerPipelines(c *gin.Context) {
	options, err := s.pipelineOptions(c.Request.Context())
	if err != nil {
		detail(c, http.StatusBadGateway, err.Error())
		return
	}
	c.JSON(http.StatusOK, options)
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

// listSKUPipelines answers with every SKU and its mapping, in one read.
//
// Every SKU, including the unmapped ones: "not mapped yet" is the state
// somebody opens this page to fix, and omitting those rows would hide exactly
// the work to be done.
func (s *Server) listSKUPipelines(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := s.store.Q.ListSKUPipelines(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the SKU list.")
		return
	}

	// Best-effort: the table is still worth showing when BambuBuddy is
	// unreachable - the mappings are Tensor's, and only the names and the
	// "missing" flag come from over there.
	options, _ := s.pipelineOptions(ctx)
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

	c.JSON(http.StatusOK, skuPipelinesResponse{
		SKUs: out, Pipelines: options, Families: machineFamilies(),
	})
}

type skuPipelineWrite struct {
	SKU           string `json:"sku" binding:"required,max=128"`
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
		if sku == "" {
			detail(c, http.StatusUnprocessableEntity, "One of the mappings names no SKU.")
			return
		}
		family := strings.ToUpper(strings.TrimSpace(m.MachineFamily))
		if !validMachineFamily(family) {
			detail(c, http.StatusUnprocessableEntity,
				"A mapping names a machine class Tensor does not run: "+m.MachineFamily)
			return
		}

		if m.PipelineID <= 0 {
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

		if _, err := s.store.Q.UpsertSKUPipeline(ctx, gen.UpsertSKUPipelineParams{
			ID: uuid.New(), Sku: sku, MachineFamily: family,
			PipelineID: int32(m.PipelineID), PipelineName: pipeline.Name,
		}); err != nil {
			detail(c, http.StatusInternalServerError, "Could not save a mapping.")
			return
		}
	}

	s.listSKUPipelines(c)
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
