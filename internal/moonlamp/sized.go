package moonlamp

// The 3D Moon Lamp in 4, 5 and 6 inch (moon_lamp_sizes): the same lithophane
// moon as the night lamp, but a full ball with no neck, cut near the bottom
// into a top and a glue-on cap so the light can go in. Sizes match the
// reference 3dp.rocks moons: inner diameter 100 / 125 / 150 mm.
//
// Tensor builds the textured ball here (SkinSTL); OpenSCAD then cuts it with
// the shop's own joint - collar, notch, spigot, key, seat, wire hole - in
// assets/moon_lamp_sizes.scad.

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

//go:embed assets/moon_texture_sizes.png
var moonTextureSizesPNG []byte

//go:embed assets/moon_lamp_sizes.scad
var SizedSCAD []byte

//go:embed assets/stand_L.stl
var StandSTL []byte

//go:embed assets/stand_L_groove.stl
var StandGrooveSTL []byte

// Size is one of the three moons.
type Size struct {
	Inches int
	R      float64 // inner radius, mm
	Step   float64 // map resolution, degrees (~0.3 mm on the surface)
}

var sizes = map[int]Size{
	4: {Inches: 4, R: 50, Step: 0.36},
	5: {Inches: 5, R: 62.5, Step: 0.3},
	6: {Inches: 6, R: 75, Step: 0.25},
}

// skuSize reads the size from a SKU ending in 4P, 5P or 6P: 3DMS01-6P is the
// 6 inch moon. The night lamp's 3DMS01-6 has no P and is not one of these.
var skuSize = regexp.MustCompile(`(?i)([456])P$`)

// SizeForSKU is the moon a SKU orders, or false when it orders none.
func SizeForSKU(sku string) (Size, bool) {
	m := skuSize.FindStringSubmatch(strings.TrimSpace(sku))
	if m == nil {
		return Size{}, false
	}
	n, _ := strconv.Atoi(m[1])
	s, ok := sizes[n]
	return s, ok
}

// Sized is the picture placement for the sized moons: the photo centred 10
// degrees up and 116 tall, so it stays above the cap seam on every size (the
// 4 inch seam is at -53 degrees) - make_moon_sizes.ps1.
func Sized() Options {
	return Options{Texture: moonTextureSizesPNG, TexturePhotoLon: 0, PhotoLat: 10, PhotoLatSpan: 116}
}

// SkinSTL is the textured ball as a closed binary STL: radius R plus the wall
// the map gives, at the size's resolution, poles included. It is the surface
// moon_lamp_sizes.scad built as a polyhedron from lamp_map.json.
func (m *Map) SkinSTL(s Size) []byte {
	nlon, nlat := int(math.Round(360/s.Step)), int(math.Round(180/s.Step))
	rings := make([][]vec, 0, nlat-1)
	for j := 1; j < nlat; j++ {
		lat := -90 + float64(j)*s.Step
		la := lat * math.Pi / 180
		ring := make([]vec, nlon)
		for i := 0; i < nlon; i++ {
			lon := -180 + float64(i)*s.Step
			r := s.R + thickness(m.Sample(lon, lat))
			a := lon * math.Pi / 180
			ring[i] = vec{r * math.Cos(la) * math.Cos(a), r * math.Cos(la) * math.Sin(a), r * math.Sin(la)}
		}
		rings = append(rings, ring)
	}
	south := vec{0, 0, -(s.R + thickness(m.Sample(0, -90+s.Step/2)))}
	north := vec{0, 0, s.R + thickness(m.Sample(0, 90-s.Step/2))}

	tris := make([][3]vec, 0, 2*nlon*nlat)
	for k := 0; k+1 < len(rings); k++ {
		lo, hi := rings[k], rings[k+1]
		for i := 0; i < nlon; i++ {
			j := (i + 1) % nlon
			tris = append(tris, [3]vec{lo[i], lo[j], hi[j]}, [3]vec{lo[i], hi[j], hi[i]})
		}
	}
	first, last := rings[0], rings[len(rings)-1]
	for i := 0; i < nlon; i++ {
		j := (i + 1) % nlon
		tris = append(tris, [3]vec{south, first[j], first[i]}, [3]vec{last[i], last[j], north})
	}
	return binarySTL(tris)
}

type vec [3]float64

func binarySTL(tris [][3]vec) []byte {
	var buf bytes.Buffer
	buf.Grow(84 + 50*len(tris))
	header := make([]byte, 80)
	copy(header, "Tensor moon lamp skin")
	buf.Write(header)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(tris)))
	rec := make([]byte, 50)
	for _, t := range tris {
		n := normal(t)
		put := func(off int, v vec) {
			for c := 0; c < 3; c++ {
				binary.LittleEndian.PutUint32(rec[off+4*c:], math.Float32bits(float32(v[c])))
			}
		}
		put(0, n)
		put(12, t[0])
		put(24, t[1])
		put(36, t[2])
		buf.Write(rec)
	}
	return buf.Bytes()
}

func normal(t [3]vec) vec {
	u := vec{t[1][0] - t[0][0], t[1][1] - t[0][1], t[1][2] - t[0][2]}
	v := vec{t[2][0] - t[0][0], t[2][1] - t[0][1], t[2][2] - t[0][2]}
	n := vec{u[1]*v[2] - u[2]*v[1], u[2]*v[0] - u[0]*v[2], u[0]*v[1] - u[1]*v[0]}
	l := math.Sqrt(n[0]*n[0] + n[1]*n[1] + n[2]*n[2])
	if l == 0 {
		return vec{}
	}
	return vec{n[0] / l, n[1] / l, n[2] / l}
}

// Filename names an order's files the way the shop's tool names its folder.
func (s Size) Filename(order, ext string) string {
	return fmt.Sprintf("%s_moon_%din.%s", order, s.Inches, ext)
}
