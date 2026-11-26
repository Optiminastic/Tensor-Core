// Package photosheet lays customer photos out on A4 paper: the inserts that
// go into DuoSnaps, photo fridge magnets, Family Crossword frames and social
// stickers, printed on the office printer and cut out by hand.
//
// Each photo has the size its product takes (internal/httpapi picks it from
// the order). The sheets are filled one at a time, each with as much photo
// as fits (pack.go), and what does not fit starts the next sheet.
//
// PURE: images in, pages out. No network, no storage.
package photosheet

import (
	"errors"
	"image"
	"math/rand/v2"
	"sort"
)

// Paper and clearances, in millimetres.
const (
	PageW, PageH = 210.0, 297.0 // A4 portrait
	// MarginMM is kept clear on all four sides: office printers do not print
	// to the edge.
	MarginMM = 6.0
	// GapMM is the clearance between two photos, room for the scissors.
	GapMM = 3.0
	// LabelMM is the band under each photo for its order number.
	LabelMM = 3.2
	// RingMM is the dark ring a round sticker photo is printed inside.
	RingMM = 0.8
	// DefaultDPI is the print resolution.
	DefaultDPI = 300.0
)

// Item is one photo to print.
type Item struct {
	Image image.Image
	// WMM x HMM is the printed size, as the photo is seen (not as placed).
	WMM, HMM float64
	// Circle prints the photo round, WMM across, inside a RingMM ring.
	Circle bool
	// Label is printed small under the photo: whose it is.
	Label string
}

// Placed is an item where it lands on its sheet. X, Y are the top-left of
// the photo in millimetres from the top-left of the page. Rotated means it
// was turned 90 degrees clockwise to fit, label and all.
type Placed struct {
	Item
	X, Y    float64
	Rotated bool
}

// Sheet is one A4 page.
type Sheet struct {
	Photos []Placed
}

// Utilisation is the share of the page the photos cover, 0..1.
func (s Sheet) Utilisation() float64 {
	var a float64
	for _, p := range s.Photos {
		a += p.WMM * p.HMM
	}
	return a / (PageW * PageH)
}

// ErrTooLarge is returned for a photo bigger than the printable page.
var ErrTooLarge = errors.New("a photo is larger than an A4 page")

// footprint is the room an item needs: the photo and its label band.
func footprint(it Item) (w, h float64) { return it.WMM, it.HMM + LabelMM }

// Layout fills the sheets one at a time, each as full as it can be: every
// ordering of the photos left is packed by every rule, and the packing that
// covers the most paper is the sheet. What did not fit starts the next one.
func Layout(items []Item) ([]Sheet, error) {
	usableW, usableH := PageW-2*MarginMM, PageH-2*MarginMM
	for _, it := range items {
		w, h := footprint(it)
		fits := (w <= usableW && h <= usableH) || (h <= usableW && w <= usableH)
		if !fits {
			return nil, ErrTooLarge
		}
	}
	var sheets []Sheet
	pending := make([]int, len(items))
	for i := range pending {
		pending[i] = i
	}
	for len(pending) > 0 {
		placed, left := fullestPage(items, pending)
		sheet := Sheet{}
		for _, p := range placed {
			sheet.Photos = append(sheet.Photos, Placed{Item: items[p.idx], X: p.x, Y: p.y, Rotated: p.rotated})
		}
		sheets = append(sheets, sheet)
		pending = left
	}
	return sheets, nil
}

// fullestPage is the packing of these items that covers the most paper.
func fullestPage(items []Item, pending []int) ([]packed, []int) {
	var bestPlaced []packed
	var bestLeft []int
	bestArea := -1.0
	for _, order := range orderings(items, pending) {
		for _, r := range rules {
			placed, left := packPage(items, order, r)
			var area float64
			for _, p := range placed {
				area += items[p.idx].WMM * items[p.idx].HMM
			}
			if area > bestArea+1e-9 {
				bestPlaced, bestLeft, bestArea = placed, left, area
			}
		}
	}
	return bestPlaced, bestLeft
}

// orderings are the orders the photos left are tried in: as given, then
// largest first by area, by height, by width, by longest and by shortest
// side - each a stable sort, so equal photos keep the order of their orders.
func orderings(items []Item, pending []int) [][]int {
	keys := []func(Item) float64{
		func(it Item) float64 { w, h := footprint(it); return w * h },
		func(it Item) float64 { _, h := footprint(it); return h },
		func(it Item) float64 { w, _ := footprint(it); return w },
		func(it Item) float64 { w, h := footprint(it); return max(w, h) },
		func(it Item) float64 { w, h := footprint(it); return min(w, h) },
	}
	out := make([][]int, 0, len(keys)+1+shuffles)
	out = append(out, pending)
	for _, key := range keys {
		order := append([]int(nil), pending...)
		sort.SliceStable(order, func(a, b int) bool { return key(items[order[a]]) > key(items[order[b]]) })
		out = append(out, order)
	}
	// And a few shuffles of the area order, seeded so the same orders always
	// give the same sheets: a mix of shapes sometimes packs tighter when a
	// small photo goes in before a large one.
	rng := rand.New(rand.NewPCG(uint64(len(pending)), 2026))
	for range shuffles {
		order := append([]int(nil), out[1]...)
		rng.Shuffle(len(order), func(a, b int) { order[a], order[b] = order[b], order[a] })
		out = append(out, order)
	}
	return out
}

// shuffles is how many shuffled orders are tried per sheet.
const shuffles = 40
