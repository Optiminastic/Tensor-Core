package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

// personaliseTimeout bounds one OpenSCAD render + merge so a hung binary can never
// block a request; on timeout the preview falls back to the plain model.
const personaliseTimeout = 30 * time.Second

// previewDecimateGrid controls how aggressively a heavy model is simplified for
// the browser preview (mirrors the frontend fallback).
const previewDecimateGrid = 90

// downloadModelLite streams a lightweight, decimated copy of the design's model
// for the interactive preview, so the browser downloads ~1 MB instead of tens of
// MB (which crawls through the dev server). The lite mesh is built once on first
// request and cached in object storage. The full model - and the slice - are
// never affected; non-STL models and any failure fall back to the original.
func (s *Server) downloadModelLite(c *gin.Context) {
	d, ok := s.designForDownload(c)
	if !ok {
		return
	}
	if d.StlKey == "" {
		detail(c, http.StatusNotFound, "No model file is available for this design.")
		return
	}
	name := safeName(d.Name)
	// Decimation only handles STL; other formats stream as-is.
	if strings.ToLower(filepath.Ext(d.StlKey)) != ".stl" {
		s.streamObject(c, d.StlKey, name+strings.ToLower(filepath.Ext(d.StlKey)))
		return
	}
	liteKey := d.StlKey + ".lite.stl"
	if err := s.ensureLiteModel(c.Request.Context(), d.StlKey, liteKey); err != nil {
		// Any failure: fall back to the full model so the preview still works.
		s.streamObject(c, d.StlKey, name+".stl")
		return
	}
	s.streamObject(c, liteKey, name+"-lite.stl")
}

// ensureLiteModel builds the decimated preview once and caches it under liteKey,
// returning nil once it exists in storage.
func (s *Server) ensureLiteModel(ctx context.Context, srcKey, liteKey string) error {
	if obj, err := s.storage.Get(ctx, liteKey); err == nil {
		_ = obj.Body.Close()
		return nil
	} else if !storage.IsNotFound(err) {
		return err
	}

	src, err := s.storage.Get(ctx, srcKey)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(src.Body)
	_ = src.Body.Close()
	if err != nil {
		return err
	}
	mesh, err := orientation.LoadSTL(data)
	if err != nil {
		return err
	}
	lite := meshio.Decimate(mesh.Triangles, mesh.Min, mesh.Max, previewDecimateGrid)
	if len(lite) < 100 {
		lite = mesh.Triangles // too aggressive for a tiny/flat model; keep detail
	}
	stl := meshio.MergeBinarySTL("tensor-preview", []meshio.Placed{{Triangles: lite}})
	return s.storage.Put(ctx, liteKey, bytes.NewReader(stl), int64(len(stl)), "model/stl")
}

// downloadPersonalisePreview streams a lightweight preview of the model with the
// customer's name rendered as real, printable 3D geometry (OpenSCAD-extruded text
// merged onto the model's top face). It mirrors downloadModelLite; the merged STL
// is built once per (model, text, placement) and cached in object storage, then
// decimated for the browser. Any failure - no text, OpenSCAD missing, a bad mesh -
// falls back to the plain lite model so the viewer never breaks.
func (s *Server) downloadPersonalisePreview(c *gin.Context) {
	d, ok := s.designForDownload(c)
	if !ok {
		return
	}
	if d.StlKey == "" {
		detail(c, http.StatusNotFound, "No model file is available for this design.")
		return
	}
	// Only STL bases can be merged with the text mesh; anything else (or an empty
	// name) just serves the plain lite model.
	spec := personalise.Spec{
		Text:    strings.TrimSpace(c.Query("text")),
		Font:    c.Query("font"),
		Style:   c.Query("font_style"),
		SizeMM:  queryFloat(c, "size_mm", 10),
		DepthMM: queryFloat(c, "depth_mm", 1),
	}
	if spec.Text == "" || strings.ToLower(filepath.Ext(d.StlKey)) != ".stl" {
		s.downloadModelLite(c)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), personaliseTimeout)
	defer cancel()

	place := textPlacement{
		OffsetXMM:   queryFloat(c, "offset_x_mm", 0),
		OffsetYMM:   queryFloat(c, "offset_y_mm", 0),
		RotationDeg: queryFloat(c, "rotation_deg", 0),
	}
	pKey, err := s.ensurePersonalisedModel(ctx, d.StlKey, spec, place)
	if err != nil {
		// OpenSCAD missing or the merge failed: show the plain model, not an error.
		s.logger.Info("personalise fallback", "design", d.ID, "reason", err)
		s.downloadModelLite(c)
		return
	}

	name := safeName(d.Name)
	liteKey := pKey + ".lite.stl"
	if err := s.ensureLiteModel(ctx, pKey, liteKey); err != nil {
		s.streamObject(c, pKey, name+"-personalised.stl")
		return
	}
	s.streamObject(c, liteKey, name+"-personalised.stl")
}

// textPlacement is where the extruded name sits on the model: an XY nudge from the
// model's centre, the surface point's height, the surface normal there, and an
// in-plane twist. With a non-zero normal the name is embossed at the point and
// tilted to face outward; with a zero normal it falls back to the top face.
type textPlacement struct {
	OffsetXMM   float64
	OffsetYMM   float64
	OffsetZMM   float64
	NormalX     float64
	NormalY     float64
	NormalZ     float64
	RotationDeg float64
}

// ensurePersonalisedModel builds the merged (base + extruded name) STL once and
// caches it under a key derived from the source model, spec and placement, so the
// same name/placement is never regenerated. Returns the cached object key.
func (s *Server) ensurePersonalisedModel(
	ctx context.Context, srcKey string, spec personalise.Spec, place textPlacement,
) (string, error) {
	pKey := personalisedKey(srcKey, spec, place)
	if obj, err := s.storage.Get(ctx, pKey); err == nil {
		_ = obj.Body.Close()
		return pKey, nil
	} else if !storage.IsNotFound(err) {
		return "", err
	}

	src, err := s.storage.Get(ctx, srcKey)
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(src.Body)
	_ = src.Body.Close()
	if err != nil {
		return "", err
	}
	base, err := orientation.LoadSTL(data)
	if err != nil {
		return "", err
	}

	textSTL, err := personalise.GenerateTextSTL(ctx, s.cfg.OpenSCADBin, spec)
	if err != nil {
		return "", err
	}
	textMesh, err := orientation.LoadSTL(textSTL)
	if err != nil {
		return "", err
	}

	placed := placeText(scaleTextToFit(textMesh, base, spec.SizeMM), base, place)
	merged := meshio.ConcatBinarySTL("tensor-personalised", base.Triangles, placed)
	return pKey, s.storage.Put(ctx, pKey, bytes.NewReader(merged), int64(len(merged)), "model/stl")
}

// scaleTextToFit resizes the origin-built extruded text so its height matches
// sizeMM, then clamps it so it never exceeds a sensible fraction of the model's
// footprint - so an over-large size can't produce a giant, spiky, overflowing
// emboss. The extrusion depth (Z) is unchanged.
func scaleTextToFit(text orientation.Mesh, base orientation.Mesh, sizeMM float64) []orientation.Triangle {
	th := text.Max.Y - text.Min.Y
	tw := text.Max.X - text.Min.X
	if th < 1e-6 || sizeMM <= 0 {
		return text.Triangles
	}
	// The two largest model extents span its biggest face; keep the text inside it.
	ex := base.Max.X - base.Min.X
	ey := base.Max.Y - base.Min.Y
	ez := base.Max.Z - base.Min.Z
	largest := math.Max(ex, math.Max(ey, ez))
	smallest := math.Min(ex, math.Min(ey, ez))
	second := ex + ey + ez - largest - smallest

	scale := sizeMM / th // height -> sizeMM
	if tw > 1e-6 {
		scale = math.Min(scale, 0.8*largest/tw)
	}
	scale = math.Min(scale, 0.5*second/th)
	return meshio.ScaleTrianglesXY(text.Triangles, scale)
}

// placeText positions the origin-built extruded text on the base model. With a
// surface normal (a placement from the editor's click), it tilts the text to face
// along that normal and sits it at the clicked point, embedding the base slightly
// so the slicer fuses it into the surface as a raised emboss. With no normal
// (designs personalised before surface placement, or the merged-preview path), it
// keeps the original behaviour: spin about Z and raise onto the model's top face.
func placeText(text []orientation.Triangle, base orientation.Mesh, place textPlacement) []orientation.Triangle {
	cx := (base.Min.X + base.Max.X) / 2
	cy := (base.Min.Y + base.Max.Y) / 2
	nLen := math.Sqrt(place.NormalX*place.NormalX + place.NormalY*place.NormalY + place.NormalZ*place.NormalZ)
	if nLen < 1e-6 {
		spun := meshio.RotateZTriangles(text, place.RotationDeg)
		return meshio.TranslateTriangles(spun, cx+place.OffsetXMM, cy+place.OffsetYMM, base.Max.Z)
	}

	// A right-handed basis (tangent, bitangent, normal), twisted in-plane by the
	// rotation - matching the viewer's decal orientation so the print agrees with
	// the preview. local +X -> ax, +Y -> ay, +Z -> the surface normal.
	n := orientation.Vec3{X: place.NormalX / nLen, Y: place.NormalY / nLen, Z: place.NormalZ / nLen}
	ref := orientation.Vec3{X: 0, Y: 0, Z: 1}
	if math.Abs(n.Z) > 0.99 {
		ref = orientation.Vec3{X: 0, Y: 1, Z: 0}
	}
	tangent := normaliseVec(crossVec(ref, n))
	bitangent := normaliseVec(crossVec(n, tangent))
	rad := place.RotationDeg * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	ax := addVec(scaleVec(tangent, cos), scaleVec(bitangent, sin))
	ay := addVec(scaleVec(bitangent, cos), scaleVec(tangent, -sin))
	oriented := meshio.OrientTriangles(text, ax, ay, n)

	// The clicked point in the base-model frame: XY from the centred offsets, Z
	// lifted off the model's min so the viewer's rest-on-zero frame lines up. Embed
	// the base a little so the emboss overlaps the surface and fuses at slice time.
	const embed = 0.6
	px := cx + place.OffsetXMM - n.X*embed
	py := cy + place.OffsetYMM - n.Y*embed
	pz := base.Min.Z + place.OffsetZMM - n.Z*embed
	return meshio.TranslateTriangles(oriented, px, py, pz)
}

func crossVec(a, b orientation.Vec3) orientation.Vec3 {
	return orientation.Vec3{X: a.Y*b.Z - a.Z*b.Y, Y: a.Z*b.X - a.X*b.Z, Z: a.X*b.Y - a.Y*b.X}
}

func normaliseVec(v orientation.Vec3) orientation.Vec3 {
	l := math.Sqrt(v.X*v.X + v.Y*v.Y + v.Z*v.Z)
	if l == 0 {
		return v
	}
	return orientation.Vec3{X: v.X / l, Y: v.Y / l, Z: v.Z / l}
}

func addVec(a, b orientation.Vec3) orientation.Vec3 {
	return orientation.Vec3{X: a.X + b.X, Y: a.Y + b.Y, Z: a.Z + b.Z}
}

func scaleVec(v orientation.Vec3, s float64) orientation.Vec3 {
	return orientation.Vec3{X: v.X * s, Y: v.Y * s, Z: v.Z * s}
}

// personalisedKey is the deterministic cache key for one merged model: the source
// key plus a short hash of the exact spec and placement, so identical requests hit
// the cache and different names/placements never collide.
func personalisedKey(srcKey string, spec personalise.Spec, place textPlacement) string {
	sig := fmt.Sprintf("%s|%s|%s|%.3f|%.3f|%.3f|%.3f|%.3f|%.3f|%.3f|%.3f|%.3f",
		spec.Text, spec.Font, spec.Style, spec.SizeMM, spec.DepthMM,
		place.OffsetXMM, place.OffsetYMM, place.OffsetZMM,
		place.NormalX, place.NormalY, place.NormalZ, place.RotationDeg)
	sum := sha256.Sum256([]byte(sig))
	return srcKey + ".p-" + hex.EncodeToString(sum[:8]) + ".stl"
}

// queryFloat reads a float query param, returning def when absent or unparseable.
func queryFloat(c *gin.Context, key string, def float64) float64 {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	return v
}
