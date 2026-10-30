package meshio

// A picture of the model, drawn here rather than in the customer's browser.
//
// The page a customer opens from WhatsApp used to show the model in a WebGL
// viewer. The mesh it displayed was 129KB; three.js, which displayed it, is
// about 700KB before @react-three/fiber and drei - five times the weight of
// the thing being shown, downloaded, parsed and executed on a phone on mobile
// data before the first pixel appears. Measured on a real order it did not
// appear at all.
//
// So the server draws it. A PNG of the same model is around 17KB, needs no
// JavaScript and no GPU, and paints as fast as any other image on the page.
//
// FROM THE 3MF, NOT FROM OPENSCAD. OpenSCAD can export a PNG directly and it
// was the obvious first answer, but the colour is not in the SCAD - it is
// applied per part when the 3MF is written - so an OpenSCAD picture comes out
// in its own default green, and the one thing a customer cannot correct after
// printing is the colour. Drawing the 3MF means the picture is the print file:
// one source of truth, the customer's own colours, and a colour-only edit
// needs no render at all, which is what keeps the commonest edit instant.

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

// PreviewView is which side the picture is taken from.
//
// A Dual Name Plank carries ONE NAME READ FROM EACH END - that is the whole
// product - so a single view can only ever show the customer half of what
// they bought. One picture per side is not a nicety here; it is the
// difference between checking your order and checking some of it.
type PreviewView int

const (
	// ViewFromLeft is the three-quarter view from the left.
	ViewFromLeft PreviewView = iota
	// ViewFromRight is the same elevation from the other side.
	ViewFromRight
	// ViewFront is square on to the face, and is what a customer is shown
	// first.
	//
	// The plank's two views answer "is my name spelled right", one name per
	// end, and a three-quarter is the right shape for that. A wall piece's
	// question is "is this the thing I bought", and the answer to that is the
	// front of it - foreshortened at twenty degrees it reads as a shape
	// rather than a product.
	ViewFront
)

// PreviewOptions is how the picture is taken.
type PreviewOptions struct {
	// View is which side to take it from.
	View PreviewView
	// Width and Height in pixels. Zero takes the defaults.
	Width, Height int
	// Background behind the model.
	Background color.RGBA
	// Fallback paints a part whose colour the file does not state.
	Fallback color.RGBA
}

const (
	defaultPreviewWidth  = 900
	defaultPreviewHeight = 700
	// supersample is how many samples per pixel edge. Two means four samples,
	// which is what turns the stepped diagonal of a letter's stem into a clean
	// edge - and lettering at an angle is most of what these pictures show.
	supersample = 2
)

// RenderPreview draws the parts and returns a PNG.
//
// The camera is fixed and chosen rather than free: three quarters from the
// front and slightly above, which is how a plank is photographed and the angle
// at which both the face and the depth of the lettering read. A customer
// checking their spelling should not have to find the right view first.
func RenderPreview(parts []Part, opts PreviewOptions) ([]byte, error) {
	w, h := opts.Width, opts.Height
	if w <= 0 {
		w = defaultPreviewWidth
	}
	if h <= 0 {
		h = defaultPreviewHeight
	}
	if opts.Background == (color.RGBA{}) {
		opts.Background = color.RGBA{0xF4, 0xF3, 0xEF, 0xFF}
	}
	if opts.Fallback == (color.RGBA{}) {
		opts.Fallback = color.RGBA{0xC8, 0xC8, 0xC2, 0xFF}
	}

	sw, sh := w*supersample, h*supersample
	buf := newFrame(sw, sh, opts.Background)

	tris, lo, hi := gatherTriangles(parts, opts.Fallback)
	if len(tris) == 0 {
		return encode(buf.downsample(w, h))
	}

	cam := cameraFor(lo, hi, sw, sh, opts.View, frontSign(tris, lo, hi))
	for _, t := range tris {
		buf.triangle(cam, t)
	}
	return encode(buf.downsample(w, h))
}

// shadedTriangle is one triangle with the colour it will be painted.
type shadedTriangle struct {
	tri    orientation.Triangle
	colour color.RGBA
}

// gatherTriangles flattens the parts and measures what they occupy.
func gatherTriangles(
	parts []Part, fallback color.RGBA,
) (tris []shadedTriangle, lo, hi orientation.Vec3) {
	first := true
	for _, p := range parts {
		c := fallback
		if parsed, ok := parseHexColour(p.Colour); ok {
			c = parsed
		}
		for _, t := range p.Triangles {
			tris = append(tris, shadedTriangle{tri: t, colour: c})
			for _, v := range [3]orientation.Vec3{t.V0, t.V1, t.V2} {
				if first {
					lo, hi, first = v, v, false
					continue
				}
				lo.X, lo.Y, lo.Z = math.Min(lo.X, v.X), math.Min(lo.Y, v.Y), math.Min(lo.Z, v.Z)
				hi.X, hi.Y, hi.Z = math.Max(hi.X, v.X), math.Max(hi.Y, v.Y), math.Max(hi.Z, v.Z)
			}
		}
	}
	return tris, lo, hi
}

// parseHexColour reads "#RRGGBB". Read3MF already guarantees the shape, so a
// failure here means no colour was stated at all.
func parseHexColour(s string) (color.RGBA, bool) {
	if len(s) != 7 || s[0] != '#' {
		return color.RGBA{}, false
	}
	var v [3]uint8
	for i := 0; i < 3; i++ {
		hi, ok1 := hexNibble(s[1+i*2])
		lo, ok2 := hexNibble(s[2+i*2])
		if !ok1 || !ok2 {
			return color.RGBA{}, false
		}
		v[i] = hi<<4 | lo
	}
	return color.RGBA{v[0], v[1], v[2], 0xFF}, true
}

func hexNibble(b byte) (uint8, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}

func encode(img image.Image) ([]byte, error) {
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
