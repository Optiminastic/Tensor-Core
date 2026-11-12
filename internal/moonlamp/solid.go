package moonlamp

import (
	"math"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

// Sizes in millimetres, from moon_lamp.scad (measured from order 114970 NL):
// a 60 mm ball inside, a lithophane wall over it, and a neck at the bottom
// that sits on the LED base. Overall about 65 mm across and 68 mm tall.
const (
	innerRadius  = 30.0
	neckOuterR   = 12.1    // the tube on the LED base
	neckHoleR    = 9.6     // the light hole
	neckBottomZ  = -35.935 // bottom of the lamp, from the sphere's centre
	neckStraight = 4.265
	cutZ         = -28.73 // where the textured shell stops and the neck begins
	cutR         = 12.77  // the neck's radius there

	tMin, tMax = 0.6, 2.6 // wall: white (bright) -> 0.6 mm, black -> 2.6 mm

	outerStep = 0.4 // degrees per vertex on the textured skin (~0.2 mm)
	innerStep = 1.2 // the smooth inside needs far fewer
)

// thickness is the wall for a brightness, as MoonLamp.Thick.
func thickness(l float64) float64 {
	return tMin + (1-math.Max(0, math.Min(1, l)))*(tMax-tMin)
}

type ring []orientation.Vec3

// Solid builds the lamp as one closed, outward-facing triangle mesh standing
// on its neck (z = 0 at the bottom), as it prints.
//
// It is the solid moon_lamp.scad describes - the textured skin cut at cutZ,
// joined to the neck, with the 60 mm ball and the light hole taken out - laid
// out directly as rings rather than computed with CSG: the outer profile
// climbs neck -> skin -> north pole, the inner one hole -> ball -> pole, and
// a flat annulus closes the bottom between them.
func (m *Map) Solid() []orientation.Triangle {
	n := int(math.Round(360 / outerStep))
	lon := func(i int) float64 { return -180 + float64(i)*outerStep }

	circle := func(r, z float64) ring {
		out := make(ring, n)
		for i := range out {
			a := lon(i) * math.Pi / 180
			out[i] = orientation.Vec3{X: r * math.Cos(a), Y: r * math.Sin(a), Z: z}
		}
		return out
	}
	skin := func(lat float64) ring {
		out := make(ring, n)
		la := lat * math.Pi / 180
		for i := range out {
			r := innerRadius + thickness(m.Sample(lon(i), lat))
			a := lon(i) * math.Pi / 180
			out[i] = orientation.Vec3{X: r * math.Cos(la) * math.Cos(a), Y: r * math.Cos(la) * math.Sin(a), Z: r * math.Sin(la)}
		}
		return out
	}
	ball := func(lat float64) ring {
		la := lat * math.Pi / 180
		return circle(innerRadius*math.Cos(la), innerRadius*math.Sin(la))
	}

	// Outer profile, bottom to top: the neck, then the skin from where its
	// nominal surface crosses cutZ up to the last ring below the pole.
	outer := []ring{
		circle(neckOuterR, neckBottomZ),
		circle(neckOuterR, neckBottomZ+neckStraight),
		circle(cutR, cutZ),
	}
	latCut := math.Asin(cutZ/(innerRadius+(tMin+tMax)/2)) * 180 / math.Pi
	outer = append(outer, skin(latCut))
	for lat := math.Ceil(latCut/outerStep)*outerStep + outerStep; lat < 90-outerStep/2; lat += outerStep {
		outer = append(outer, skin(lat))
	}
	northT := thickness(m.Sample(0, 90-outerStep/2))

	// Inner profile, bottom to top: the light hole, then the ball from where
	// the hole meets it up to the last ring below its pole.
	latHole := -math.Acos(neckHoleR/innerRadius) * 180 / math.Pi
	inner := []ring{circle(neckHoleR, neckBottomZ), ball(latHole)}
	for lat := math.Ceil(latHole/innerStep)*innerStep + innerStep; lat < 90-innerStep/2; lat += innerStep {
		inner = append(inner, ball(lat))
	}

	var tris []orientation.Triangle
	add := func(a, b, c orientation.Vec3) {
		tris = append(tris, triangle(a, b, c))
	}
	band := func(lo, hi ring, outward bool) {
		for i := 0; i < n; i++ {
			j := (i + 1) % n
			if outward {
				add(lo[i], lo[j], hi[j])
				add(lo[i], hi[j], hi[i])
			} else {
				add(lo[i], hi[j], lo[j])
				add(lo[i], hi[i], hi[j])
			}
		}
	}
	cap := func(r ring, pole orientation.Vec3, outward bool) {
		for i := 0; i < n; i++ {
			j := (i + 1) % n
			if outward {
				add(r[i], r[j], pole)
			} else {
				add(r[i], pole, r[j])
			}
		}
	}

	for k := 0; k+1 < len(outer); k++ {
		band(outer[k], outer[k+1], true)
	}
	cap(outer[len(outer)-1], orientation.Vec3{Z: innerRadius + northT}, true)
	for k := 0; k+1 < len(inner); k++ {
		band(inner[k], inner[k+1], false)
	}
	cap(inner[len(inner)-1], orientation.Vec3{Z: innerRadius}, false)
	// The bottom: neck outside to light hole inside, facing down.
	band(inner[0], outer[0], true)

	// Stand it on the neck.
	for i := range tris {
		tris[i].V0.Z -= neckBottomZ
		tris[i].V1.Z -= neckBottomZ
		tris[i].V2.Z -= neckBottomZ
	}
	return tris
}

func triangle(a, b, c orientation.Vec3) orientation.Triangle {
	ux, uy, uz := b.X-a.X, b.Y-a.Y, b.Z-a.Z
	vx, vy, vz := c.X-a.X, c.Y-a.Y, c.Z-a.Z
	nx, ny, nz := uy*vz-uz*vy, uz*vx-ux*vz, ux*vy-uy*vx
	l := math.Sqrt(nx*nx + ny*ny + nz*nz)
	t := orientation.Triangle{V0: a, V1: b, V2: c, Area: l / 2}
	if l > 0 {
		t.Normal = orientation.Vec3{X: nx / l, Y: ny / l, Z: nz / l}
	}
	return t
}
