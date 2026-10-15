package meshio

// The rasteriser: a z-buffer, one light, and no dependencies.
//
// Deliberately small. This draws solid-coloured triangles with flat shading,
// which is all a product shot of a printed object needs - there is no texture,
// no transparency and no reflection to model. Everything expensive about a
// real renderer exists for effects this picture does not have.

import (
	"image"
	"image/color"
	"math"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

// frame is the colour and depth buffers together.
type frame struct {
	w, h  int
	pix   []color.RGBA
	depth []float64
}

func newFrame(w, h int, bg color.RGBA) *frame {
	f := &frame{w: w, h: h, pix: make([]color.RGBA, w*h), depth: make([]float64, w*h)}
	for i := range f.pix {
		f.pix[i] = bg
		f.depth[i] = math.Inf(1)
	}
	return f
}

// camera turns a point in the model into a pixel and a depth.
type camera struct {
	right, up, forward orientation.Vec3
	centre             orientation.Vec3
	scale              float64
	w, h               int
}

// cameraFor frames the whole model, three quarters on and slightly above.
//
// Orthographic, not perspective: the customer is checking the shape of
// letters, and perspective makes the far end of a 200mm plank smaller than
// the near end for no gain in understanding. It also means the framing cannot
// change with the model's size, so two units of the same order are drawn at
// the same scale and can be compared.
func cameraFor(lo, hi orientation.Vec3, w, h int, view PreviewView) camera {
	centre := orientation.Vec3{X: (lo.X + hi.X) / 2, Y: (lo.Y + hi.Y) / 2, Z: (lo.Z + hi.Z) / 2}

	// Three quarters on and above, from one side or the other. Z is up, as
	// everywhere else in the pipeline.
	forward := unitVec(orientation.Vec3{X: -0.55, Y: 0.72, Z: -0.42})
	if view == ViewFromRight {
		forward = unitVec(orientation.Vec3{X: 0.55, Y: 0.72, Z: -0.42})
	}
	worldUp := orientation.Vec3{X: 0, Y: 0, Z: 1}
	right := unitVec(cross(forward, worldUp))
	up := unitVec(cross(right, forward))

	// The extent of the box once projected, so nothing is clipped whatever
	// shape it is.
	var halfW, halfH float64
	for _, c := range corners(lo, hi) {
		d := sub(c, centre)
		halfW = math.Max(halfW, math.Abs(dot(d, right)))
		halfH = math.Max(halfH, math.Abs(dot(d, up)))
	}
	// A tenth of margin so the model does not touch the edges of the frame.
	const margin = 1.1
	scale := math.Min(float64(w)/(2*halfW*margin), float64(h)/(2*halfH*margin))
	if math.IsInf(scale, 0) || scale <= 0 {
		scale = 1
	}
	return camera{right: right, up: up, forward: forward, centre: centre, scale: scale, w: w, h: h}
}

// project turns a model point into pixel x, y and a depth along the view.
func (c camera) project(p orientation.Vec3) (x, y, z float64) {
	d := sub(p, c.centre)
	x = float64(c.w)/2 + dot(d, c.right)*c.scale
	// Screen y grows downward.
	y = float64(c.h)/2 - dot(d, c.up)*c.scale
	z = dot(d, c.forward)
	return x, y, z
}

// triangle draws one triangle, nearest surface winning.
func (f *frame) triangle(cam camera, st shadedTriangle) {
	x0, y0, z0 := cam.project(st.tri.V0)
	x1, y1, z1 := cam.project(st.tri.V1)
	x2, y2, z2 := cam.project(st.tri.V2)

	// Twice the signed area. Zero means the triangle is edge-on and covers no
	// pixels; dividing by it would be a division by zero.
	area := (x1-x0)*(y2-y0) - (x2-x0)*(y1-y0)
	if area == 0 {
		return
	}

	shade := f.shadeOf(st.tri.Normal, cam)
	c := scaleColour(st.colour, shade)

	minX := clampInt(int(math.Floor(math.Min(x0, math.Min(x1, x2)))), 0, f.w-1)
	maxX := clampInt(int(math.Ceil(math.Max(x0, math.Max(x1, x2)))), 0, f.w-1)
	minY := clampInt(int(math.Floor(math.Min(y0, math.Min(y1, y2)))), 0, f.h-1)
	maxY := clampInt(int(math.Ceil(math.Max(y0, math.Max(y1, y2)))), 0, f.h-1)

	for py := minY; py <= maxY; py++ {
		for px := minX; px <= maxX; px++ {
			// Pixel centres, so a triangle edge falling exactly on a boundary
			// does not paint the pixel twice from both sides.
			fx, fy := float64(px)+0.5, float64(py)+0.5
			w0 := ((x1-fx)*(y2-fy) - (x2-fx)*(y1-fy)) / area
			w1 := ((x2-fx)*(y0-fy) - (x0-fx)*(y2-fy)) / area
			w2 := 1 - w0 - w1
			if w0 < 0 || w1 < 0 || w2 < 0 {
				continue
			}
			z := w0*z0 + w1*z1 + w2*z2
			i := py*f.w + px
			if z >= f.depth[i] {
				continue
			}
			f.depth[i] = z
			f.pix[i] = c
		}
	}
}

// shadeOf is one light in the scene over a generous ambient.
//
// IN THE SCENE, NOT ON THE CAMERA. A light that sits with the viewer lights
// every visible face equally, and the picture comes back as a flat cut-out -
// the top of a letter and its side are the same blue, so the lettering has no
// depth and a plank reads as a sticker. The light is placed above, in front
// and to the left instead, which is where a photographer would put it: tops
// bright, faces mid, sides darker, and the extrusion reads.
//
// The ambient floor is high on purpose. This is a product shot a customer is
// checking spelling on, not a dramatic render, and a letter's unlit side must
// stay legible rather than going black.
func (f *frame) shadeOf(normal orientation.Vec3, cam camera) float64 {
	n := unitVec(normal)
	if n == (orientation.Vec3{}) {
		return 1
	}
	lit := dot(n, sceneLight)
	// A surface facing away from the light gets the ambient only. Its normal
	// may also simply be wound the other way - 3MF does not promise a
	// consistent winding - so the magnitude is taken rather than clamped to
	// zero, which would leave whole letters flat black on a mesh whose
	// normals disagree.
	if lit < 0 {
		lit = -lit * 0.55
	}
	const ambient = 0.52
	return ambient + (1-ambient)*lit
}

// sceneLight is where the light is: above, in front and a little to the left.
var sceneLight = unitVec(orientation.Vec3{X: -0.35, Y: -0.45, Z: 0.82})

// downsample averages the supersampled buffer into the final image.
func (f *frame) downsample(w, h int) image.Image {
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	n := supersample * supersample
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, b int
			for dy := 0; dy < supersample; dy++ {
				for dx := 0; dx < supersample; dx++ {
					p := f.pix[(y*supersample+dy)*f.w+(x*supersample+dx)]
					r += int(p.R)
					g += int(p.G)
					b += int(p.B)
				}
			}
			out.SetRGBA(x, y, color.RGBA{uint8(r / n), uint8(g / n), uint8(b / n), 0xFF})
		}
	}
	return out
}

func scaleColour(c color.RGBA, k float64) color.RGBA {
	f := func(v uint8) uint8 {
		s := float64(v) * k
		if s > 255 {
			return 255
		}
		if s < 0 {
			return 0
		}
		return uint8(s)
	}
	return color.RGBA{f(c.R), f(c.G), f(c.B), 0xFF}
}

func corners(lo, hi orientation.Vec3) [8]orientation.Vec3 {
	return [8]orientation.Vec3{
		{X: lo.X, Y: lo.Y, Z: lo.Z}, {X: hi.X, Y: lo.Y, Z: lo.Z},
		{X: lo.X, Y: hi.Y, Z: lo.Z}, {X: hi.X, Y: hi.Y, Z: lo.Z},
		{X: lo.X, Y: lo.Y, Z: hi.Z}, {X: hi.X, Y: lo.Y, Z: hi.Z},
		{X: lo.X, Y: hi.Y, Z: hi.Z}, {X: hi.X, Y: hi.Y, Z: hi.Z},
	}
}

func sub(a, b orientation.Vec3) orientation.Vec3 {
	return orientation.Vec3{X: a.X - b.X, Y: a.Y - b.Y, Z: a.Z - b.Z}
}
func dot(a, b orientation.Vec3) float64 { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }
func cross(a, b orientation.Vec3) orientation.Vec3 {
	return orientation.Vec3{
		X: a.Y*b.Z - a.Z*b.Y, Y: a.Z*b.X - a.X*b.Z, Z: a.X*b.Y - a.Y*b.X,
	}
}
func unitVec(v orientation.Vec3) orientation.Vec3 {
	l := math.Sqrt(dot(v, v))
	if l == 0 {
		return orientation.Vec3{}
	}
	return orientation.Vec3{X: v.X / l, Y: v.Y / l, Z: v.Z / l}
}
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
