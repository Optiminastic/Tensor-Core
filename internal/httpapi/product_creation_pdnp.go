package httpapi

// The Premium Dual Name Plank's front text plate (SKU PDNP-*): the order's
// "STEP 6 - Front Text" inlaid in a 192.8 mm plate - see internal/frontplate.
//
// Only the front text is read; the plank's two names are made elsewhere.
// Both the 3MF and its picture come from OpenSCAD, which only the production
// worker has, so Submit starts a background build straight away and the page
// shows the preview drawn from the finished model. A build takes a second or
// two: each part is a 0.2 s render, and the two run side by side.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/frontplate"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

const pdnpProduct = "pdnp-front-text"

// pdnpFrontText finds the premium plank line and its front text, or says why
// it cannot be made.
func pdnpFrontText(order gen.Order) (string, string) {
	var lines []production.LineItem
	if err := jsonUnmarshalLines(order.LineItems, &lines); err != nil {
		return "", "This order's line items could not be read."
	}
	found := false
	for _, li := range lines {
		if !isPDNPLine(li) {
			continue
		}
		found = true
		text := pdnpLineText(li)
		if text == "" {
			continue
		}
		if n := len([]rune(text)); n > frontplate.MaxRunes {
			return "", fmt.Sprintf("The front text is %d characters; at most %d fit on the plate.", n, frontplate.MaxRunes)
		}
		if bad := frontplate.Unsupported(text); len(bad) > 0 {
			return "", fmt.Sprintf("The front text has %s, which the plate's font cannot draw - only letters, numbers, punctuation and hearts print.",
				strings.Join(bad, " "))
		}
		return text, ""
	}
	if found {
		return "", fmt.Sprintf("Order %s's Premium Dual Name Plank has no front text (STEP 6 - Front Text).", order.OrderNumber)
	}
	return "", fmt.Sprintf("Order %s has no Premium Dual Name Plank - its SKU would start PDNP- (e.g. PDNP-YEL).", order.OrderNumber)
}

func isPDNPLine(li production.LineItem) bool {
	sku := strings.ToUpper(strings.TrimSpace(deref(li.SKU)))
	return strings.HasPrefix(sku, "PDNP") ||
		strings.Contains(strings.ToLower(li.ProductName), "premium dual name plank")
}

func pdnpLineText(li production.LineItem) string {
	for _, prop := range li.Properties {
		if strings.HasPrefix(prop.Name, "_") {
			continue
		}
		if strings.Contains(normalisePropKey(prop.Name), "front text") {
			return strings.TrimSpace(prop.Value)
		}
	}
	return ""
}

// startPDNPBuild answers POST .../pdnp-front-text/builds.
func (s *Server) startPDNPBuild(c *gin.Context) {
	order, _, ok := s.productCreationOrder(c, []string{"3mf"})
	if !ok {
		return
	}
	text, problem := pdnpFrontText(order)
	if problem != "" {
		detail(c, http.StatusUnprocessableEntity, problem)
		return
	}
	// The text (hashed to fit the column) is the variant, so a corrected order
	// builds afresh rather than handing back the old plate.
	sum := sha256.Sum256([]byte(text))
	s.startProductBuild(c, order, pdnpProduct, hex.EncodeToString(sum[:8]))
}

// buildPDNPFront renders the plate and the text side by side, then writes
// them as one two-colour object and draws its picture.
func buildPDNPFront(ctx context.Context, s *Server, order gen.Order) (builtFile, error) {
	text, problem := pdnpFrontText(order)
	if problem != "" {
		return builtFile{}, fmt.Errorf("%s", problem)
	}
	type result struct {
		i    int
		mesh orientation.Mesh
		err  error
	}
	results := make(chan result, len(frontplate.Parts))
	for i, p := range frontplate.Parts {
		go func() {
			stl, err := s.renderer.RunSource(ctx, frontplate.SCAD, nil, map[string]string{
				"plate_text": personalise.Quote(text), "part": personalise.Quote(p.Name),
			}, 2*time.Minute, "textmetrics")
			if err != nil {
				results <- result{i: i, err: fmt.Errorf("%s: %w", p.Name, err)}
				return
			}
			mesh, err := orientation.LoadSTL(stl)
			if err == nil && len(mesh.Triangles) == 0 {
				err = fmt.Errorf("%s came out empty", p.Name)
			}
			results <- result{i, mesh, err}
		}()
	}
	parts := make([]meshio.Part, len(frontplate.Parts))
	for range frontplate.Parts {
		r := <-results
		if r.err != nil {
			return builtFile{}, fmt.Errorf("OpenSCAD could not make the plate: %w", r.err)
		}
		p := frontplate.Parts[r.i]
		parts[r.i] = meshio.Part{Name: p.Name, Colour: p.Colour, Material: meshio.DefaultFilamentType, Triangles: r.mesh.Triangles}
	}
	model, err := meshio.WriteModels3MF([]meshio.Model{{Name: order.OrderNumber + " front text", Parts: parts}})
	if err != nil {
		return builtFile{}, fmt.Errorf("could not write the 3MF: %w", err)
	}
	preview, err := meshio.RenderPreview(parts, meshio.PreviewOptions{View: meshio.ViewFront, Width: 1400, Height: 360})
	if err != nil {
		return builtFile{}, fmt.Errorf("could not draw the preview: %w", err)
	}
	return builtFile{name: frontplate.Filename(order.OrderNumber), data: model, preview: preview}, nil
}
