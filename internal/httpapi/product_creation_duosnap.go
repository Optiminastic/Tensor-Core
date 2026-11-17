package httpapi

// The Duo Snap (SKU T3DPS-DUO-*, "DUOSNAP - Blue"): the order's TOP NAME and
// BOTTOM NAME joined by the fixed "&", outlined and framed in the colour
// bought - see internal/duosnap.
//
// Only the two names and the colour are read. The order's PHOTO is for the
// frame the customer fills themselves and plays no part in the print file.
// Built in the background like the Premium DNP front plate: OpenSCAD runs on
// the production worker, the three pieces side by side.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/duosnap"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

const duoSnapProduct = "duo-snap"

// duoSnapOrder is what the file needs from an order.
type duoSnapOrder struct {
	top, bottom, colour string
}

// duoSnapFields finds the Duo Snap line and its names and colour, or says
// why it cannot be made.
func duoSnapFields(order gen.Order) (duoSnapOrder, string) {
	var lines []production.LineItem
	if err := jsonUnmarshalLines(order.LineItems, &lines); err != nil {
		return duoSnapOrder{}, "This order's line items could not be read."
	}
	for _, li := range lines {
		if !isDuoSnapLine(li) {
			continue
		}
		var d duoSnapOrder
		for _, p := range li.Properties {
			key := normalisePropKey(p.Name)
			switch {
			case strings.HasPrefix(p.Name, "_"):
			case key == "top name" || (d.top == "" && strings.Contains(key, "top name")):
				d.top = strings.TrimSpace(p.Value)
			case key == "bottom name" || (d.bottom == "" && strings.Contains(key, "bottom name")):
				d.bottom = strings.TrimSpace(p.Value)
			case strings.Contains(key, "colour") || strings.Contains(key, "color"):
				d.colour = strings.TrimSpace(p.Value)
			}
		}
		if d.colour == "" {
			d.colour = colourFromVariant(li.VariantTitle)
		}
		if d.colour == "" {
			d.colour = strings.TrimSpace(deref(li.Colour))
		}
		if problem := d.problem(order.OrderNumber); problem != "" {
			return duoSnapOrder{}, problem
		}
		return d, ""
	}
	return duoSnapOrder{}, fmt.Sprintf("Order %s has no Duo Snap - its SKU would be T3DPS-DUO-… (DUOSNAP).", order.OrderNumber)
}

func (d duoSnapOrder) problem(order string) string {
	if d.top == "" || d.bottom == "" {
		return fmt.Sprintf("Order %s's Duo Snap needs both a TOP NAME and a BOTTOM NAME.", order)
	}
	if d.colour == "" {
		return fmt.Sprintf("Order %s's Duo Snap names no colour.", order)
	}
	for _, name := range []string{d.top, d.bottom} {
		if n := len([]rune(name)); n > duosnap.MaxRunes {
			return fmt.Sprintf("%q is %d characters; at most %d fit on the Duo Snap.", name, n, duosnap.MaxRunes)
		}
		if bad := duosnap.Unsupported(name); len(bad) > 0 {
			return fmt.Sprintf("%q has %s, which the Duo Snap's font cannot draw - only plain letters, numbers and punctuation.",
				name, strings.Join(bad, " "))
		}
	}
	return ""
}

func isDuoSnapLine(li production.LineItem) bool {
	for _, seg := range strings.Split(strings.ToUpper(deref(li.SKU)), "-") {
		if seg == "DUO" {
			return true
		}
	}
	name := strings.ToLower(li.ProductName)
	return strings.Contains(name, "duosnap") || strings.Contains(name, "duo snap")
}

// startDuoSnapBuild answers POST .../duo-snap/builds.
func (s *Server) startDuoSnapBuild(c *gin.Context) {
	order, _, ok := s.productCreationOrder(c, []string{"3mf"})
	if !ok {
		return
	}
	d, problem := duoSnapFields(order)
	if problem != "" {
		detail(c, http.StatusUnprocessableEntity, problem)
		return
	}
	// The names and colour are the variant, so a corrected order builds
	// afresh rather than handing back the old file.
	// "name-case" marks files written with NameCase, so a build made before it
	// (names in capitals) is not handed back.
	sum := sha256.Sum256([]byte(d.top + "|" + d.bottom + "|" + d.colour + "|name-case"))
	s.startProductBuild(c, order, duoSnapProduct, hex.EncodeToString(sum[:8]))
}

// buildDuoSnap renders the three pieces side by side and writes them as one
// coloured object, with a picture taken from the display side.
func buildDuoSnap(ctx context.Context, s *Server, order gen.Order) (builtFile, error) {
	d, problem := duoSnapFields(order)
	if problem != "" {
		return builtFile{}, errors.New(problem)
	}
	body, err := s.resolveColourHex(ctx, d.colour)
	if err != nil {
		return builtFile{}, fmt.Errorf("no swatch for %q; add the colour to the filament shelf", d.colour)
	}
	params := map[string]string{
		"name_1": personalise.Quote(duosnap.NameCase(d.top)), "name_2": personalise.Quote(duosnap.NameCase(d.bottom)),
		"body_color": personalise.Quote(body),
	}
	type result struct {
		i    int
		mesh orientation.Mesh
		err  error
	}
	results := make(chan result, len(duosnap.Parts))
	for i, p := range duosnap.Parts {
		go func() {
			args := map[string]string{"part": personalise.Quote(p.Name)}
			for k, v := range params {
				args[k] = v
			}
			stl, err := s.renderer.RunSource(ctx, duosnap.Source(), duosnap.Files(), args, 3*time.Minute)
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
	parts := make([]meshio.Part, len(duosnap.Parts))
	for range duosnap.Parts {
		r := <-results
		if r.err != nil {
			return builtFile{}, fmt.Errorf("OpenSCAD could not make the Duo Snap: %w", r.err)
		}
		p := duosnap.Parts[r.i]
		colour := duosnap.LetterColour
		if p.BodyColoured {
			colour = body
		}
		parts[r.i] = meshio.Part{Name: p.Name, Colour: colour, Material: meshio.DefaultFilamentType, Triangles: r.mesh.Triangles}
	}
	model, err := meshio.WriteModels3MF([]meshio.Model{{Name: order.OrderNumber + " Duo Snap", Parts: parts}})
	if err != nil {
		return builtFile{}, fmt.Errorf("could not write the 3MF: %w", err)
	}
	// The letters sit in the face that prints down - the display side - so
	// the picture is taken with it turned up.
	preview, err := meshio.RenderPreview(turnedOverTopToBottom(parts), meshio.PreviewOptions{View: meshio.ViewFront, Width: 900, Height: 700})
	if err != nil {
		return builtFile{}, fmt.Errorf("could not draw the preview: %w", err)
	}
	return builtFile{name: duosnap.Filename(order.OrderNumber), data: model, preview: preview}, nil
}
