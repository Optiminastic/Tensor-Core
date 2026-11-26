package photosheet

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/Optiminastic/tensor-core/internal/photoframe"
)

// ringColour is the dark green ring of the social sticker artwork.
var ringColour = color.RGBA{0x12, 0x4a, 0x3f, 0xff}

// cutColour is the hairline drawn round a square photo in the PNG preview.
var cutColour = color.RGBA{0x9a, 0x9a, 0x9a, 0xff}

// Render draws one sheet at dpi. lines adds the cut lines as pixels - the
// preview; the PDF draws them as vectors instead.
func (s Sheet) Render(dpi float64, lines bool) *image.RGBA {
	k := dpi / 25.4
	pg := image.NewRGBA(image.Rect(0, 0, px(PageW, k), px(PageH, k)))
	draw.Draw(pg, pg.Bounds(), image.White, image.Point{}, draw.Src)
	for _, p := range s.Photos {
		photoR, labelR := p.rects()
		tile := photoTile(p.Item, px(p.WMM, k), px(p.HMM, k))
		label := labelTile(p.Label, px(p.WMM, k), px(LabelMM, k))
		if p.Rotated {
			tile, label = rotateCW(tile), rotateCW(label)
		}
		blit(pg, tile, mmPoint(photoR.Min, k))
		blit(pg, label, mmPoint(labelR.Min, k))
		if lines && !p.Circle {
			outline(pg, mmRect(photoR, k))
		}
	}
	return pg
}

// mmRectF is a rectangle in millimetres.
type mmRectF struct{ Min, Max [2]float64 }

// rects are where the photo and its label land on the page, in millimetres.
// Turned clockwise, the label band that sat below the photo is on its left.
func (p Placed) rects() (photo, label mmRectF) {
	if !p.Rotated {
		photo = mmRectF{[2]float64{p.X, p.Y}, [2]float64{p.X + p.WMM, p.Y + p.HMM}}
		label = mmRectF{[2]float64{p.X, p.Y + p.HMM}, [2]float64{p.X + p.WMM, p.Y + p.HMM + LabelMM}}
		return photo, label
	}
	label = mmRectF{[2]float64{p.X, p.Y}, [2]float64{p.X + LabelMM, p.Y + p.WMM}}
	photo = mmRectF{[2]float64{p.X + LabelMM, p.Y}, [2]float64{p.X + LabelMM + p.HMM, p.Y + p.WMM}}
	return photo, label
}

func px(mm, k float64) int { return int(math.Round(mm * k)) }

func mmPoint(p [2]float64, k float64) image.Point { return image.Pt(px(p[0], k), px(p[1], k)) }

func mmRect(r mmRectF, k float64) image.Rectangle {
	return image.Rect(px(r.Min[0], k), px(r.Min[1], k), px(r.Max[0], k), px(r.Max[1], k))
}

// photoTile is the photo at w x h pixels: the centre of the photo, cut to
// the shape (a cover fit, nothing stretched), round and ringed for a sticker.
func photoTile(it Item, w, h int) *image.RGBA {
	tile := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(tile, tile.Bounds(), image.White, image.Point{}, draw.Src)
	if it.Image == nil || w <= 0 || h <= 0 {
		return tile
	}
	src := photoframe.ToRGBA(it.Image)
	b := src.Bounds()
	iw, ih := float64(b.Dx()), float64(b.Dy())
	aspect := float64(w) / float64(h)
	cw, ch := iw, iw/aspect
	if iw/ih > aspect {
		cw, ch = ih*aspect, ih
	}
	photoframe.Resample(tile, tile.Bounds(), src,
		float64(b.Min.X)+(iw-cw)/2, float64(b.Min.Y)+(ih-ch)/2, cw, ch)
	if it.Circle {
		roundOff(tile, RingMM/it.WMM*float64(w))
	}
	return tile
}

// roundOff whitens everything outside the inscribed circle and paints a
// ring ringPx wide just inside its edge. Each edge pixel is sampled 4x4 so
// the circle prints smooth.
func roundOff(tile *image.RGBA, ringPx float64) {
	b := tile.Bounds()
	r := float64(min(b.Dx(), b.Dy())) / 2
	cx, cy := float64(b.Dx())/2, float64(b.Dy())/2
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			var out, ring float64
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					d := math.Hypot(float64(x)+(float64(sx)+0.5)/4-cx, float64(y)+(float64(sy)+0.5)/4-cy)
					switch {
					case d > r:
						out++
					case d > r-ringPx:
						ring++
					}
				}
			}
			if out == 0 && ring == 0 {
				continue
			}
			c := tile.RGBAAt(x, y)
			tile.SetRGBA(x, y, mix(c, ringColour, out/16, ring/16))
		}
	}
}

// mix blends c with white (by fw) and the ring colour (by fr).
func mix(c, ring color.RGBA, fw, fr float64) color.RGBA {
	fc := 1 - fw - fr
	ch := func(a, b uint8) uint8 { return uint8(math.Round(float64(a)*fc + 255*fw + float64(b)*fr)) }
	return color.RGBA{ch(c.R, ring.R), ch(c.G, ring.G), ch(c.B, ring.B), 255}
}

// labelTile is the label text in a w x h strip, scaled up from the bitmap
// face so it stays about 2 mm tall at print resolution.
func labelTile(text string, w, h int) *image.RGBA {
	tile := image.NewRGBA(image.Rect(0, 0, max(w, 1), max(h, 1)))
	draw.Draw(tile, tile.Bounds(), image.White, image.Point{}, draw.Src)
	face := basicfont.Face7x13
	scale := max(1, int(math.Round(float64(h)*0.68/13)))
	small := image.NewRGBA(image.Rect(0, 0, max(w/scale, 1), 13))
	draw.Draw(small, small.Bounds(), image.White, image.Point{}, draw.Src)
	d := font.Drawer{Dst: small, Src: image.NewUniform(color.RGBA{0x55, 0x55, 0x55, 0xff}), Face: face,
		Dot: fixed.P(0, 10)}
	d.DrawString(text)
	top := max(0, (h-13*scale)/2)
	for y := 0; y < 13*scale && top+y < h; y++ {
		for x := 0; x < w; x++ {
			tile.SetRGBA(x, top+y, small.RGBAAt(x/scale, y/scale))
		}
	}
	return tile
}

// rotateCW turns an image a quarter turn clockwise.
func rotateCW(src *image.RGBA) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dy(), b.Dx()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.SetRGBA(b.Dy()-1-y, x, src.RGBAAt(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func blit(dst, src *image.RGBA, at image.Point) {
	draw.Draw(dst, src.Bounds().Add(at), src, src.Bounds().Min, draw.Src)
}

// outline draws a one-pixel hairline just inside r.
func outline(dst *image.RGBA, r image.Rectangle) {
	u := image.NewUniform(cutColour)
	for _, side := range []image.Rectangle{
		image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+1),
		image.Rect(r.Min.X, r.Max.Y-1, r.Max.X, r.Max.Y),
		image.Rect(r.Min.X, r.Min.Y, r.Min.X+1, r.Max.Y),
		image.Rect(r.Max.X-1, r.Min.Y, r.Max.X, r.Max.Y),
	} {
		draw.Draw(dst, side.Intersect(dst.Bounds()), u, image.Point{}, draw.Src)
	}
}
