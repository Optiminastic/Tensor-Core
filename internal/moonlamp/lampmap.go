// Package moonlamp builds the personalised moon lamp: a hollow 60 mm sphere
// printed as a lithophane, its wall 0.6 mm where light should pass and 2.6 mm
// where it should not, so the lit lamp shows the moon with the customer's
// photo and words on it.
//
// A port of the shop's Windows tool (MoonLamp.cs + moon_lamp.scad, sized from
// reference order 114970 NL). The picture is composited in longitude/latitude
// space exactly as the tool does it; the solid is then built here directly
// from the thickness map rather than through OpenSCAD, which needed a 2 MB
// JSON import and a minute per lamp to do the same arithmetic.
//
// PURE: images and text in, a map, a picture and triangles out.
package moonlamp

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

//go:embed assets/moon_texture.png
var moonTexturePNG []byte

//go:embed assets/GOTHICB.TTF
var textFontTTF []byte

// Compositing grid: P pixels per degree over the whole sphere.
const (
	P = 4
	W = 360 * P
	H = 180 * P
)

// The picture's layout, in degrees - MoonLamp.cs, unchanged.
const (
	photoLon        = 0.0   // the photo sits at the centre of the unrolled map
	texturePhotoLon = -90.0 // where moon_texture.png has its filled-in photo area
	photoLatSpan    = 132.0 // photo height (+-66)
	photoMaxLonSpan = 160.0 // never wider than this
	photoFeather    = 8.0   // soft fade of the photo edge into the moon
	textGap         = 15.0  // photo edge to text block
	textLat         = 16.5  // vertical centre of the text block
	capHeight       = 10.3  // capital letter height (~5.6 mm)
	linePitch       = 16.0  // baseline to baseline
	textMaxLonSpan  = 85.0  // longer lines are squeezed sideways
)

// Map is the lamp's brightness over the sphere, 0 (dark, thick wall) to 1
// (bright, thin wall): rows from latitude +90 down to -90, columns from
// longitude -180 to +180.
type Map struct {
	L [H][W]float64
}

// Build composites the moon texture, the customer's photo and up to two lines
// of text, as MoonLamp.Run does. A nil photo or empty text is left out.
func Build(photo image.Image, line1, line2 string) (*Map, error) {
	m := &Map{}
	tex, err := png.Decode(bytes.NewReader(moonTexturePNG))
	if err != nil {
		return nil, err
	}
	m.loadTexture(tex, int(math.Round((photoLon-texturePhotoLon)*P)))
	photoHalfLon := 0.0
	if photo != nil {
		photoHalfLon = m.pastePhoto(photo)
	}
	lines := nonEmpty(line1, line2)
	if len(lines) > 0 {
		mask, err := textMask(lines, photoHalfLon)
		if err != nil {
			return nil, err
		}
		half := float64(mask.Bounds().Dx()) / 2 / P
		m.stamp(mask, photoLon-photoHalfLon-textGap-half)
		m.stamp(mask, photoLon+photoHalfLon+textGap+half)
	}
	return m, nil
}

func nonEmpty(lines ...string) []string {
	var out []string
	for _, l := range lines {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func lum(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	return (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 65535
}

// loadTexture reads the moon into L, rolled sideways by shift pixels so its
// photo area sits where the customer's photo will go.
func (m *Map) loadTexture(tex image.Image, shift int) {
	b := tex.Bounds()
	for j := 0; j < H; j++ {
		sy := b.Min.Y + j*b.Dy()/H
		for i := 0; i < W; i++ {
			sx := b.Min.X + i*b.Dx()/W
			m.L[j][((i+shift)%W+W)%W] = lum(tex.At(sx, sy))
		}
	}
}

func smooth(x float64) float64 {
	x = math.Max(0, math.Min(1, x))
	return x * x * (3 - 2*x)
}

// pastePhoto blends the photo into the centre of the map, fading into the
// moon over its outer photoFeather degrees, and returns its half-width.
func (m *Map) pastePhoto(photo image.Image) float64 {
	b := photo.Bounds()
	aspect := float64(b.Dx()) / float64(b.Dy())
	latSpan, lonSpan := photoLatSpan, photoLatSpan*aspect
	if lonSpan > photoMaxLonSpan {
		lonSpan = photoMaxLonSpan
		latSpan = lonSpan / aspect
	}
	pw, ph := int(math.Round(lonSpan*P)), int(math.Round(latSpan*P))
	fe := math.Max(0.01, photoFeather*P)
	c0 := int(math.Round((photoLon - lonSpan/2 + 180) * P))
	r0 := int(math.Round((90 - latSpan/2) * P))
	scaled := scaleGray(photo, pw, ph)
	for j := 0; j < ph; j++ {
		r := r0 + j
		if r < 0 || r >= H {
			continue
		}
		for i := 0; i < pw; i++ {
			a := smooth(math.Min(float64(i)+0.5, float64(pw-i)-0.5)/fe) *
				smooth(math.Min(float64(j)+0.5, float64(ph-j)-0.5)/fe)
			c := ((c0+i)%W + W) % W
			m.L[r][c] = m.L[r][c]*(1-a) + scaled[j*pw+i]*a
		}
	}
	return lonSpan / 2
}

// scaleGray resizes img to w x h as luminance, averaging the source pixels
// each output pixel covers.
func scaleGray(img image.Image, w, h int) []float64 {
	b := img.Bounds()
	out := make([]float64, w*h)
	sx, sy := float64(b.Dx())/float64(w), float64(b.Dy())/float64(h)
	for j := 0; j < h; j++ {
		y0, y1 := int(float64(j)*sy), int(math.Max(float64(j+1)*sy, float64(j)*sy+1))
		for i := 0; i < w; i++ {
			x0, x1 := int(float64(i)*sx), int(math.Max(float64(i+1)*sx, float64(i)*sx+1))
			var s float64
			n := 0
			for y := y0; y < y1 && y < b.Dy(); y++ {
				for x := x0; x < x1 && x < b.Dx(); x++ {
					s += lum(img.At(b.Min.X+x, b.Min.Y+y))
					n++
				}
			}
			if n > 0 {
				out[j*w+i] = s / float64(n)
			}
		}
	}
	return out
}

// textMask draws the text block white on black at P px per degree, each line
// squeezed sideways if it is wider than the space beside the photo, and
// softened twice like the original's ~0.4 mm edge.
func textMask(lines []string, photoHalfLon float64) (*image.Gray, error) {
	f, err := opentype.Parse(textFontTTF)
	if err != nil {
		return nil, err
	}
	// Size the face so a capital H is capHeight degrees tall.
	const probe = 1000.0
	pf, err := opentype.NewFace(f, &opentype.FaceOptions{Size: probe, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil, err
	}
	hb, _, _ := pf.GlyphBounds('H')
	inkH := float64(hb.Max.Y-hb.Min.Y) / 64 / probe
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: capHeight * P / inkH, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil, err
	}
	maxSpan := math.Min(textMaxLonSpan, 180-photoHalfLon-textGap-8) * P

	rendered := make([]*image.Alpha, len(lines))
	widest := 0.0
	for k, l := range lines {
		img := renderLine(face, l)
		if w := float64(img.Bounds().Dx()); w > maxSpan {
			img = squeeze(img, int(maxSpan))
		}
		rendered[k] = img
		widest = math.Max(widest, float64(img.Bounds().Dx()))
	}
	pad := 3 * P
	cw := int(math.Ceil(widest)) + 2*pad
	ch := int(math.Ceil((capHeight*1.3+linePitch*float64(len(lines)-1))*P)) + 2*pad
	blockH := capHeight + linePitch*float64(len(lines)-1)
	mask := image.NewGray(image.Rect(0, 0, cw, ch))
	for k, img := range rendered {
		baseline := blockH/2 - capHeight - float64(k)*linePitch // degrees above block centre
		y := int(math.Round(float64(ch)/2-baseline*P)) - img.Bounds().Dy()
		x := (cw - img.Bounds().Dx()) / 2
		draw.DrawMask(mask, image.Rect(x, y, x+img.Bounds().Dx(), y+img.Bounds().Dy()),
			image.White, image.Point{}, img, image.Point{}, draw.Over)
	}
	return blur(blur(mask)), nil
}

// renderLine draws one line with its baseline on the image's bottom edge.
func renderLine(face font.Face, s string) *image.Alpha {
	d := &font.Drawer{Face: face}
	w := d.MeasureString(s).Ceil() + 2
	m := face.Metrics()
	h := m.Ascent.Ceil() + 1
	img := image.NewAlpha(image.Rect(0, 0, w, h))
	d.Dst, d.Src = img, image.Opaque
	d.Dot = fixed.P(1, h-1)
	d.DrawString(s)
	return img
}

// squeeze narrows img to width w, keeping its height.
func squeeze(img *image.Alpha, w int) *image.Alpha {
	b := img.Bounds()
	out := image.NewAlpha(image.Rect(0, 0, w, b.Dy()))
	scale := float64(b.Dx()) / float64(w)
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < w; x++ {
			x0, x1 := int(float64(x)*scale), int(math.Max(float64(x+1)*scale, float64(x)*scale+1))
			var s, n int
			for sx := x0; sx < x1 && sx < b.Dx(); sx++ {
				s += int(img.AlphaAt(sx, y).A)
				n++
			}
			if n > 0 {
				out.SetAlpha(x, y, color.Alpha{A: uint8(s / n)})
			}
		}
	}
	return out
}

func blur(a *image.Gray) *image.Gray {
	b := a.Bounds()
	out := image.NewGray(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			s, n := 0, 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if p := image.Pt(x+dx, y+dy); p.In(b) {
						s += int(a.GrayAt(p.X, p.Y).Y)
						n++
					}
				}
			}
			out.SetGray(x, y, color.Gray{Y: uint8(s / n)})
		}
	}
	return out
}

// stamp lightens the map with the text block centred on longitude lonC.
func (m *Map) stamp(mask *image.Gray, lonC float64) {
	b := mask.Bounds()
	cw, ch := b.Dx(), b.Dy()
	c0 := int(math.Round((lonC+180)*P - float64(cw)/2))
	r0 := int(math.Round((90-textLat)*P - float64(ch)/2))
	for j := 0; j < ch; j++ {
		r := r0 + j
		if r < 0 || r >= H {
			continue
		}
		for i := 0; i < cw; i++ {
			a := float64(mask.GrayAt(b.Min.X+i, b.Min.Y+j).Y) / 255
			if a <= 0 {
				continue
			}
			c := ((c0+i)%W + W) % W
			m.L[r][c] = m.L[r][c]*(1-a) + a
		}
	}
}

// Sample is the brightness at (lon, lat), bilinear, wrapping in longitude.
func (m *Map) Sample(lon, lat float64) float64 {
	x := (lon+180)*P - 0.5
	y := (90-lat)*P - 0.5
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(x0), y-float64(y0)
	at := func(yy, xx int) float64 {
		yy = max(0, min(H-1, yy))
		return m.L[yy][((xx%W)+W)%W]
	}
	return (at(y0, x0)*(1-fx)+at(y0, x0+1)*fx)*(1-fy) + (at(y0+1, x0)*(1-fx)+at(y0+1, x0+1)*fx)*fy
}

// Preview is how the lamp looks lit, unrolled with the photo in the centre -
// warm-tinted like the shop's preview, light where the wall is thin.
func (m *Map) Preview() ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	shift := int(math.Round(-photoLon * P))
	for j := 0; j < H; j++ {
		for i := 0; i < W; i++ {
			g := math.Round(255 * math.Max(0, math.Min(1, m.L[j][i])))
			img.SetRGBA(((i+shift)%W+W)%W, j, color.RGBA{uint8(g), uint8(g * 0.93), uint8(g * 0.78), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
