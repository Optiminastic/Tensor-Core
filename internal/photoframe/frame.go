// Package photoframe lays out the DNP photo frame print: two photos, each cut
// into nine vertical strips and interleaved A B A B across an A4 landscape
// page, so the folded frame shows one photo from each side.
//
// A port of the shop's Windows tool (DnpFrame.cs, "DNP Photo Frame Maker"),
// whose geometry was copied from the production sample 114764 pp.pdf. The
// numbers below are that tool's, unchanged - the folded frame is cut and
// creased to them, so a layout that drifted by a millimetre would put the
// fold through somebody's face.
//
// PURE: images in, image or bytes out. No network, no storage.
package photoframe

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
)

// Page geometry, in millimetres (DnpFrame.cs).
const (
	PageW, PageH = 297.0, 210.0

	borderX, borderY, borderW, borderH = 6.0, 10.0, 285.0, 190.0

	stripX0, stripY, stripW, stripH = 22.5, 10.0, 14.0, 190.0
	StripsPerPhoto                  = 9

	decorY, decorH, decorW  = 10.21, 188.57, 13.795
	decorLeftX, decorRightX = 8.565, 274.64

	// Line widths in points: the hairline around each strip, the outer border.
	stripLinePt, borderLinePt = 0.216, 0.5669

	// DefaultDPI is the print resolution the shop's tool uses.
	DefaultDPI = 300.0
)

//go:embed assets/decor_left.png
var decorLeftPNG []byte

//go:embed assets/decor_right.png
var decorRightPNG []byte

// Photo is one side of the frame. Zoom 1 just covers the 126 x 190 mm area;
// Pan runs -1 (left/top edge) .. 0 (centre) .. 1 (right/bottom edge).
type Photo struct {
	Image      image.Image
	Zoom       float64
	PanX, PanY float64
}

// Frame is the whole print: photo A in strips 1, 3 .. 17, B in 2, 4 .. 18.
type Frame struct {
	A, B  Photo
	Decor bool // the heart border band at both ends
}

// cropRect is the part of a photo that fills the 126 x 190 mm area: a cover
// fit, then zoom and pan - DnpFrame.CropRect.
func cropRect(p Photo) (x, y, w, h float64) {
	aspect := (stripW * StripsPerPhoto) / stripH
	b := p.Image.Bounds()
	iw, ih := float64(b.Dx()), float64(b.Dy())
	cw, ch := iw, iw/aspect
	if iw/ih > aspect {
		ch, cw = ih, ih*aspect
	}
	z := math.Max(1, p.Zoom)
	cw, ch = cw/z, ch/z
	cx := (iw - cw) / 2 * (1 + clamp(p.PanX))
	cy := (ih - ch) / 2 * (1 + clamp(p.PanY))
	return float64(b.Min.X) + cx, float64(b.Min.Y) + cy, cw, ch
}

func clamp(v float64) float64 { return math.Max(-1, math.Min(1, v)) }

// Render draws the page at dpi, cut lines included - the PNG the shop prints
// from. lines=false leaves them out, for the PDF, which draws them as vectors.
func (f Frame) Render(dpi float64, lines bool) (*image.RGBA, error) {
	k := dpi / 25.4
	page := image.NewRGBA(image.Rect(0, 0, int(math.Round(PageW*k)), int(math.Round(PageH*k))))
	draw.Draw(page, page.Bounds(), image.White, image.Point{}, draw.Src)

	for side, p := range []Photo{f.A, f.B} {
		if p.Image == nil {
			continue
		}
		src := toRGBA(p.Image)
		cx, cy, cw, ch := cropRect(Photo{Image: src, Zoom: p.Zoom, PanX: p.PanX, PanY: p.PanY})
		sw := cw / StripsPerPhoto
		for i := 0; i < StripsPerPhoto; i++ {
			x := stripX0 + float64(2*i+side)*stripW
			resample(page, mmRect(x, stripY, stripW, stripH, k), src, cx+float64(i)*sw, cy, sw, ch)
		}
	}

	if f.Decor {
		for _, d := range []struct {
			data []byte
			x    float64
		}{{decorLeftPNG, decorLeftX}, {decorRightPNG, decorRightX}} {
			img, err := png.Decode(bytes.NewReader(d.data))
			if err != nil {
				return nil, err
			}
			src := toRGBA(img)
			b := src.Bounds()
			resample(page, mmRect(d.x, decorY, decorW, decorH, k), src,
				float64(b.Min.X), float64(b.Min.Y), float64(b.Dx()), float64(b.Dy()))
		}
	}

	if lines {
		thin := int(math.Max(1, math.Round(stripLinePt/72*dpi)))
		thick := int(math.Max(1, math.Round(borderLinePt/72*dpi)))
		for i := 0; i < 2*StripsPerPhoto; i++ {
			outline(page, mmRect(stripX0+float64(i)*stripW, stripY, stripW, stripH, k), thin)
		}
		outline(page, mmRect(borderX, borderY, borderW, borderH, k), thick)
	}
	return page, nil
}

// mmRect is a millimetre rectangle in pixels, edges rounded independently so
// neighbouring strips share an edge with no gap - DnpFrame.MmRect.
func mmRect(x, y, w, h, k float64) image.Rectangle {
	return image.Rect(
		int(math.Round(x*k)), int(math.Round(y*k)),
		int(math.Round((x+w)*k)), int(math.Round((y+h)*k)))
}

func toRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok {
		return r
	}
	b := img.Bounds()
	out := image.NewRGBA(b)
	draw.Draw(out, b, img, b.Min, draw.Src)
	return out
}

// resample fills dst with the source area (sx, sy, sw, sh), averaging every
// source pixel a destination pixel covers - so a photo scaled down into a
// strip stays smooth rather than aliased. Edges clamp to the source area,
// which is what TileFlipXY bought the original: no soft border on a strip.
func resample(dst *image.RGBA, r image.Rectangle, src *image.RGBA, sx, sy, sw, sh float64) {
	r = r.Intersect(dst.Bounds())
	if r.Empty() || sw <= 0 || sh <= 0 {
		return
	}
	sb := src.Bounds()
	minX, minY := math.Max(sx, float64(sb.Min.X)), math.Max(sy, float64(sb.Min.Y))
	maxX, maxY := math.Min(sx+sw, float64(sb.Max.X)), math.Min(sy+sh, float64(sb.Max.Y))
	scaleX, scaleY := sw/float64(r.Dx()), sh/float64(r.Dy())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		y0 := sy + float64(y-r.Min.Y)*scaleY
		y1 := y0 + scaleY
		for x := r.Min.X; x < r.Max.X; x++ {
			x0 := sx + float64(x-r.Min.X)*scaleX
			dst.SetRGBA(x, y, boxAverage(src, x0, y0, x0+scaleX, y1, minX, minY, maxX, maxY))
		}
	}
}

// boxAverage is the mean of the source pixels under (x0,y0)-(x1,y1), at least
// one pixel wide, clamped to the crop.
func boxAverage(src *image.RGBA, x0, y0, x1, y1, minX, minY, maxX, maxY float64) color.RGBA {
	ix0 := int(math.Floor(math.Max(minX, math.Min(x0, maxX-1))))
	iy0 := int(math.Floor(math.Max(minY, math.Min(y0, maxY-1))))
	ix1 := int(math.Ceil(math.Min(maxX, math.Max(x1, float64(ix0)+1))))
	iy1 := int(math.Ceil(math.Min(maxY, math.Max(y1, float64(iy0)+1))))
	var r, g, b, n uint32
	for y := iy0; y < iy1; y++ {
		off := src.PixOffset(ix0, y)
		for x := ix0; x < ix1; x++ {
			r += uint32(src.Pix[off])
			g += uint32(src.Pix[off+1])
			b += uint32(src.Pix[off+2])
			off += 4
			n++
		}
	}
	if n == 0 {
		return color.RGBA{255, 255, 255, 255}
	}
	return color.RGBA{uint8(r / n), uint8(g / n), uint8(b / n), 255}
}

// outline draws a black rectangle border t pixels thick, inside r.
func outline(dst *image.RGBA, r image.Rectangle, t int) {
	black := image.NewUniform(color.Black)
	for _, side := range []image.Rectangle{
		image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+t),
		image.Rect(r.Min.X, r.Max.Y-t, r.Max.X, r.Max.Y),
		image.Rect(r.Min.X, r.Min.Y, r.Min.X+t, r.Max.Y),
		image.Rect(r.Max.X-t, r.Min.Y, r.Max.X, r.Max.Y),
	} {
		draw.Draw(dst, side.Intersect(dst.Bounds()), black, image.Point{}, draw.Src)
	}
}
