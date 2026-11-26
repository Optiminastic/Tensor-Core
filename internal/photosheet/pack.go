package photosheet

import "math"

// A MaxRects packer: it keeps every maximal free rectangle of the page, not
// the two halves of a guillotine cut, so a photo can use the space beside
// and below its neighbours at once. The printer beds use a guillotine packer
// (internal/bedpack), which is right for a handful of planks and leaves a
// column of paper empty beside a row of DuoSnaps - the waste this exists to
// avoid.
//
// The cutting gap is handled by growing every photo and the page by GapMM on
// the right and bottom: two photos that touch in that space are a gap apart.

type rect struct{ x, y, w, h float64 }

// rule scores putting a w x h box into free rectangle f; lower is better.
type rule func(f rect, w, h float64) (float64, float64)

var rules = []rule{
	// Best short side fit.
	func(f rect, w, h float64) (float64, float64) {
		dw, dh := f.w-w, f.h-h
		return math.Min(dw, dh), math.Max(dw, dh)
	},
	// Best area fit.
	func(f rect, w, h float64) (float64, float64) {
		return f.w*f.h - w*h, math.Min(f.w-w, f.h-h)
	},
	// Bottom-left: top-most, then left-most.
	func(f rect, _, h float64) (float64, float64) { return f.y + h, f.x },
	// Best long side fit.
	func(f rect, w, h float64) (float64, float64) {
		dw, dh := f.w-w, f.h-h
		return math.Max(dw, dh), math.Min(dw, dh)
	},
}

// packed is one item index and where it went.
type packed struct {
	idx     int
	x, y    float64
	rotated bool
}

// packPage places items, in the given order, on one page using rule r.
// It returns what was placed and the indexes left over, in order.
func packPage(items []Item, order []int, r rule) (placed []packed, left []int) {
	g := GapMM
	free := []rect{{MarginMM, MarginMM, PageW - 2*MarginMM + g, PageH - 2*MarginMM + g}}
	for _, idx := range order {
		w, h := footprint(items[idx])
		best, rot, ok := choose(free, w+g, h+g, r)
		if !ok {
			left = append(left, idx)
			continue
		}
		bw, bh := w+g, h+g
		if rot {
			bw, bh = bh, bw
		}
		used := rect{best.x, best.y, bw, bh}
		free = prune(splitAll(free, used))
		placed = append(placed, packed{idx: idx, x: best.x, y: best.y, rotated: rot})
	}
	return placed, left
}

// choose is the free rectangle (and turn) the rule likes best for a w x h box.
func choose(free []rect, w, h float64, r rule) (rect, bool, bool) {
	var best rect
	var rot, ok bool
	b1, b2 := math.Inf(1), math.Inf(1)
	for _, f := range free {
		for _, o := range [2]struct {
			w, h float64
			rot  bool
		}{{w, h, false}, {h, w, true}} {
			if o.w > f.w+1e-9 || o.h > f.h+1e-9 {
				continue
			}
			s1, s2 := r(f, o.w, o.h)
			if s1 < b1-1e-9 || (math.Abs(s1-b1) <= 1e-9 && s2 < b2) {
				best, rot, ok, b1, b2 = rect{f.x, f.y, o.w, o.h}, o.rot, true, s1, s2
			}
		}
	}
	return best, rot, ok
}

// splitAll replaces every free rectangle the used one overlaps by the up to
// four maximal pieces of it that are left.
func splitAll(free []rect, u rect) []rect {
	out := make([]rect, 0, len(free)+4)
	for _, f := range free {
		if u.x >= f.x+f.w || u.x+u.w <= f.x || u.y >= f.y+f.h || u.y+u.h <= f.y {
			out = append(out, f)
			continue
		}
		if u.x > f.x {
			out = append(out, rect{f.x, f.y, u.x - f.x, f.h})
		}
		if u.x+u.w < f.x+f.w {
			out = append(out, rect{u.x + u.w, f.y, f.x + f.w - u.x - u.w, f.h})
		}
		if u.y > f.y {
			out = append(out, rect{f.x, f.y, f.w, u.y - f.y})
		}
		if u.y+u.h < f.y+f.h {
			out = append(out, rect{f.x, u.y + u.h, f.w, f.y + f.h - u.y - u.h})
		}
	}
	return out
}

// prune drops every free rectangle that lies inside another.
func prune(free []rect) []rect {
	out := free[:0:0]
	for i, a := range free {
		inside := false
		for j, b := range free {
			if i == j || !contains(b, a) {
				continue
			}
			// Of two identical rectangles keep the first.
			if !contains(a, b) || j < i {
				inside = true
				break
			}
		}
		if !inside {
			out = append(out, a)
		}
	}
	return out
}

func contains(outer, inner rect) bool {
	return inner.x >= outer.x-1e-9 && inner.y >= outer.y-1e-9 &&
		inner.x+inner.w <= outer.x+outer.w+1e-9 && inner.y+inner.h <= outer.y+outer.h+1e-9
}
