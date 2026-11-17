package httpapi

// The Soulmate Combo: one order, three print files - the Dual Name Plank (the
// single-heart file, its lettering in the colour the customer chose), the 3D
// Rose and the Heart Keychain (each in the colours its own file states).
//
// Every file is rendered exactly as the production worker renders that part
// for a job: resolveRenderPlan reads the registry's file and field mapping
// for the part, and renderColouredPlank builds the coloured 3MF. The job it
// is handed is a stand-in made from the order line - it has no id, so the
// colour bookkeeping that path does against a real job touches nothing.
//
// Both shapes of combo order are read: an older order carrying one SC-xxx
// line (the SC product's plank, rose and keychain parts; an SCWL-xxx "with
// LIGHT" line prints the same three), and a combo sold as
// a Shopify bundle, which arrives as SC-PLANK-xxx, 3DRN and SC-KEY lines
// (SC-ROSE on the first test orders, before the rose became 3D Rose Name).

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/production"
)

const soulmateProduct = "soulmate-combo"

// soulmatePartNames are the combo's files, in the order they are shown.
var soulmatePartNames = []string{"plank", "rose", "keychain"}

// comboPart is one file to render: which part, from which line.
type comboPart struct {
	name string
	sku  string
	role string
	line production.LineItem
}

// soulmateParts finds the combo's plank, rose and keychain in an order, or
// says why it cannot.
func (s *Server) soulmateParts(ctx context.Context, order gen.Order) ([]comboPart, string) {
	var lines []production.LineItem
	if err := jsonUnmarshalLines(order.LineItems, &lines); err != nil {
		return nil, "This order's line items could not be read."
	}
	found := map[string]comboPart{}
	for _, li := range lines {
		li = s.resolveLineSKU(ctx, li)
		sku := strings.TrimSpace(deref(li.SKU))
		upper := strings.ToUpper(sku)
		switch {
		case strings.HasPrefix(upper, "SC-PLANK"):
			found["plank"] = comboPart{"plank", sku, designRoleBody, li}
		case upper == "3DRN" || upper == "SC-ROSE":
			found["rose"] = comboPart{"rose", sku, designRoleBody, li}
		case upper == "SC-KEY":
			found["keychain"] = comboPart{"keychain", sku, designRoleBody, li}
		default:
			// The SC product sold as one line: its parts are roles.
			if fileSKU, ok := s.comboSKU(ctx, upper); ok {
				for _, name := range soulmatePartNames {
					found[name] = comboPart{name, fileSKU, name, li}
				}
			}
		}
	}
	var parts []comboPart
	var missing []string
	for _, name := range soulmatePartNames {
		p, ok := found[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		parts = append(parts, p)
	}
	if len(missing) == len(soulmatePartNames) {
		return nil, fmt.Sprintf("Order %s has no Soulmate Combo.", order.OrderNumber)
	}
	if len(missing) > 0 {
		return nil, fmt.Sprintf("Order %s's Soulmate Combo has no %s line.", order.OrderNumber, strings.Join(missing, " or "))
	}
	return parts, ""
}

// soulmateVariant identifies what the files are made from, so a corrected
// name builds afresh instead of handing back the old files.
func soulmateVariant(parts []comboPart) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%s|%s|%s|", p.name, p.sku, deref(p.line.VariantTitle))
		for _, prop := range p.line.Properties {
			fmt.Fprintf(h, "%s=%s;", prop.Name, prop.Value)
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// startSoulmateBuild answers POST .../soulmate-combo/builds.
func (s *Server) startSoulmateBuild(c *gin.Context) {
	order, _, ok := s.productCreationOrder(c, []string{"3mf"})
	if !ok {
		return
	}
	parts, problem := s.soulmateParts(c.Request.Context(), order)
	if problem != "" {
		detail(c, http.StatusUnprocessableEntity, problem)
		return
	}
	s.startProductBuild(c, order, soulmateProduct, soulmateVariant(parts))
}

// buildSoulmateCombo renders the three files, and returns them one by one
// and together in a ZIP, with a picture of all three side by side.
func buildSoulmateCombo(ctx context.Context, s *Server, order gen.Order) (builtFile, error) {
	parts, problem := s.soulmateParts(ctx, order)
	if problem != "" {
		return builtFile{}, fmt.Errorf("%s", problem)
	}
	var out builtFile
	var previews [][]byte
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for _, p := range parts {
		model, err := s.renderComboPart(ctx, order, p)
		if err != nil {
			return builtFile{}, fmt.Errorf("the %s: %w", p.name, err)
		}
		out.parts = append(out.parts, builtPart{name: p.name, data: model})
		w, err := zw.CreateHeader(&zip.FileHeader{
			Name: fmt.Sprintf("%s_sc_%s.3mf", order.OrderNumber, p.name), Method: zip.Deflate, Modified: time.Now(),
		})
		if err != nil {
			return builtFile{}, err
		}
		if _, err := w.Write(model); err != nil {
			return builtFile{}, err
		}
		previews = append(previews, previewsOf(p.name, model)...)
	}
	if err := zw.Close(); err != nil {
		return builtFile{}, err
	}
	out.name = order.OrderNumber + "_soulmate_combo.zip"
	out.data = archive.Bytes()
	if len(previews) > 0 {
		out.preview = sideBySide(previews)
	}
	return out, nil
}

// renderComboPart renders one part through the production worker's path.
func (s *Server) renderComboPart(ctx context.Context, order gen.Order, p comboPart) ([]byte, error) {
	sku := p.sku
	job := gen.ProductionJob{
		JobNumber:                 order.OrderNumber + "-" + strings.ToUpper(p.name),
		Sku:                       &sku,
		PartRole:                  p.role,
		VariantTitle:              p.line.VariantTitle,
		Colour:                    p.line.Colour,
		Material:                  p.line.Material,
		PersonalisationProperties: propertiesJSON(p.line.Properties),
		Colours:                   []byte("[]"),
	}
	plan, err := s.resolveRenderPlan(ctx, job, p.line.Properties)
	if err != nil {
		return nil, err
	}
	model, _, err := s.renderColouredPlank(ctx, job, plan)
	return model, err
}

// previewsOf draws a part the way its names can be read.
//
// The plank carries one name read from each end, so it gets both
// three-quarter views, as the production preview draws it. The keychain's
// name is inlaid in the face that prints DOWN - drawn as it lies, the picture
// is a plain red heart - so it is turned over first. Best effort: a part that
// cannot be drawn just has no picture.
func previewsOf(name string, model []byte) [][]byte {
	parts, err := meshio.Read3MF(model)
	if err != nil {
		return nil
	}
	views := []meshio.PreviewView{meshio.ViewFront}
	switch name {
	case "plank":
		views = []meshio.PreviewView{meshio.ViewFromLeft, meshio.ViewFromRight}
	case "keychain":
		parts = turnedOver(parts)
	}
	var out [][]byte
	for _, v := range views {
		if pic, err := meshio.RenderPreview(parts, meshio.PreviewOptions{View: v, Width: 600, Height: 480}); err == nil {
			out = append(out, pic)
		}
	}
	return out
}

// turnedOver rotates parts half a turn about the Y axis, so the face that
// printed down faces up - and a name inlaid mirrored left to right there
// reads true.
func turnedOver(parts []meshio.Part) []meshio.Part {
	return turned(parts, func(v orientation.Vec3) orientation.Vec3 { return orientation.Vec3{X: -v.X, Y: v.Y, Z: -v.Z} })
}

// turnedOverTopToBottom is turnedOver about the X axis, for a file that
// mirrors its lettering top to bottom instead (the Duo Snap).
func turnedOverTopToBottom(parts []meshio.Part) []meshio.Part {
	return turned(parts, func(v orientation.Vec3) orientation.Vec3 { return orientation.Vec3{X: v.X, Y: -v.Y, Z: -v.Z} })
}

func turned(parts []meshio.Part, flip func(orientation.Vec3) orientation.Vec3) []meshio.Part {
	out := make([]meshio.Part, len(parts))
	for i, p := range parts {
		p.Triangles = append([]orientation.Triangle(nil), p.Triangles...)
		for j, t := range p.Triangles {
			p.Triangles[j] = orientation.Triangle{V0: flip(t.V0), V1: flip(t.V1), V2: flip(t.V2),
				Normal: flip(t.Normal), Area: t.Area}
		}
		out[i] = p
	}
	return out
}

// sideBySide joins PNGs left to right on the previews' own background.
func sideBySide(pngs [][]byte) []byte {
	var imgs []image.Image
	width, height := 0, 0
	for _, b := range pngs {
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			continue
		}
		imgs = append(imgs, img)
		width += img.Bounds().Dx()
		height = max(height, img.Bounds().Dy())
	}
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.RGBA{0xF4, 0xF3, 0xEF, 0xFF}}, image.Point{}, draw.Src)
	x := 0
	for _, img := range imgs {
		r := image.Rect(x, 0, x+img.Bounds().Dx(), img.Bounds().Dy())
		draw.Draw(canvas, r, img, img.Bounds().Min, draw.Over)
		x += img.Bounds().Dx()
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, canvas)
	return buf.Bytes()
}

// comboSKU is the SKU whose registry setup prints this combo line: the line's
// own when its product has the plank, rose and keychain parts, else - for the
// Soulmate COMBO with LIGHT, SCWL-RED - the plain combo's in the same colour,
// SC-RED. The light is a stand the plank sits in, not a print, so the three
// files are the same; SCWL has no registry setup of its own to drift from SC.
func (s *Server) comboSKU(ctx context.Context, upper string) (string, bool) {
	isCombo := func(sku string) bool {
		roles := s.partRolesForSKU(ctx, sku)
		return slices.Contains(roles, "plank") && slices.Contains(roles, "rose") && slices.Contains(roles, "keychain")
	}
	if isCombo(upper) {
		return upper, true
	}
	if colour, ok := strings.CutPrefix(upper, "SCWL-"); ok && isCombo("SC-"+colour) {
		return "SC-" + colour, true
	}
	return "", false
}
