package photosheet

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"strings"
	"testing"
)

func solid(w, h int, c color.RGBA) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
	return img
}

var red = color.RGBA{220, 20, 20, 255}

func many(n int, w, h float64, circle bool) []Item {
	out := make([]Item, n)
	for i := range out {
		out[i] = Item{Image: solid(400, 600, red), WMM: w, HMM: h, Circle: circle, Label: "T3DPS-1"}
	}
	return out
}

// Every photo is on the page, inside the margins, and clear of every other
// photo by the cutting gap.
func checkSheet(t *testing.T, s Sheet) {
	t.Helper()
	type box struct{ x0, y0, x1, y1 float64 }
	var boxes []box
	for _, p := range s.Photos {
		w, h := footprint(p.Item)
		if p.Rotated {
			w, h = h, w
		}
		b := box{p.X, p.Y, p.X + w, p.Y + h}
		if b.x0 < MarginMM-1e-6 || b.y0 < MarginMM-1e-6 || b.x1 > PageW-MarginMM+1e-6 || b.y1 > PageH-MarginMM+1e-6 {
			t.Fatalf("photo outside the margins: %+v", b)
		}
		for _, o := range boxes {
			if b.x0 < o.x1+GapMM-1e-6 && o.x0 < b.x1+GapMM-1e-6 && b.y0 < o.y1+GapMM-1e-6 && o.y0 < b.y1+GapMM-1e-6 {
				t.Fatalf("photos closer than the gap: %+v %+v", b, o)
			}
		}
		boxes = append(boxes, b)
	}
}

// The PDF sample put four DuoSnap photos on a page; the packer turns a
// fifth on its side into the space left at the bottom.
func TestDuoSnapsFillAPageThenStartTheNext(t *testing.T) {
	sheets, err := Layout(many(7, 65, 105, false))
	if err != nil {
		t.Fatal(err)
	}
	if len(sheets) != 2 || len(sheets[0].Photos) < 4 {
		t.Fatalf("sheets = %d, first holds %d", len(sheets), len(sheets[0].Photos))
	}
	total := 0
	for _, s := range sheets {
		checkSheet(t, s)
		total += len(s.Photos)
	}
	if total != 7 {
		t.Fatalf("placed %d of 7", total)
	}
	if sheets[0].Utilisation() <= sheets[1].Utilisation() {
		t.Fatal("the first sheet should be the full one")
	}
}

func TestMixedProductsShareASheet(t *testing.T) {
	items := append(many(2, 65, 105, false), many(5, 56.6, 59.7, false)...)
	items = append(items, many(2, 51, 51, true)...)
	items = append(items, many(1, 106, 80, false)...)
	sheets, err := Layout(items)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range sheets {
		checkSheet(t, s)
		n += len(s.Photos)
	}
	if n != len(items) || len(sheets) > 2 {
		t.Fatalf("placed %d of %d on %d sheets", n, len(items), len(sheets))
	}
}

func TestFridgeMagnetsTwelveToAPage(t *testing.T) {
	sheets, _ := Layout(many(12, 56.6, 59.7, false))
	if len(sheets) != 1 {
		t.Fatalf("12 magnets took %d sheets", len(sheets))
	}
}

func TestTooLargeIsRefused(t *testing.T) {
	if _, err := Layout(many(1, 250, 250, false)); err != ErrTooLarge {
		t.Fatalf("err = %v", err)
	}
}

// A round sticker is white outside its circle and ringed just inside it,
// with the photo in the middle.
func TestStickerIsRoundAndRinged(t *testing.T) {
	tile := photoTile(Item{Image: solid(300, 300, red), WMM: 51, HMM: 51, Circle: true}, 600, 600)
	if c := tile.RGBAAt(2, 2); c != (color.RGBA{255, 255, 255, 255}) {
		t.Fatalf("corner = %v, want white", c)
	}
	if c := tile.RGBAAt(300, 300); c != red {
		t.Fatalf("centre = %v, want the photo", c)
	}
	if c := tile.RGBAAt(300, 3); c != ringColour {
		t.Fatalf("edge = %v, want the ring", c)
	}
}

// A photo is cropped to its frame, never stretched: a wide photo in a tall
// frame keeps its middle.
func TestPhotoIsCoverFitted(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 300, 100))
	draw.Draw(src, src.Bounds(), image.NewUniform(color.RGBA{0, 0, 255, 255}), image.Point{}, draw.Src)
	draw.Draw(src, image.Rect(100, 0, 200, 100), image.NewUniform(red), image.Point{}, draw.Src)
	tile := photoTile(Item{Image: src, WMM: 10, HMM: 10}, 50, 50)
	if tile.RGBAAt(1, 25) != red || tile.RGBAAt(48, 25) != red {
		t.Fatal("the square crop should be the red middle third")
	}
}

func TestPDFHasOnePagePerSheet(t *testing.T) {
	sheets, _ := Layout(many(7, 65, 105, false))
	pdf, err := PDF(sheets, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) || !strings.Contains(string(pdf), "/Count 2") {
		t.Fatal("expected a two-page PDF")
	}
	if _, err := PreviewPNG(sheets, 20); err != nil {
		t.Fatal(err)
	}
}

func TestRotatedPhotoLandsInItsFootprint(t *testing.T) {
	p := Placed{Item: Item{WMM: 65, HMM: 105}, X: 10, Y: 20, Rotated: true}
	photo, label := p.rects()
	if label.Min != [2]float64{10, 20} || photo.Max != [2]float64{10 + LabelMM + 105, 20 + 65} {
		t.Fatalf("photo %v label %v", photo, label)
	}
}
