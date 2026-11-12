package photoframe

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"testing"
)

func solid(w, h int, c color.RGBA) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
	return img
}

var (
	red  = color.RGBA{220, 20, 20, 255}
	blue = color.RGBA{20, 20, 220, 255}
)

// The page is A4 landscape at 300 dpi, and the strips alternate A B A B
// across it starting 22.5 mm in - strip 1 is photo A, strip 2 photo B.
func TestRenderInterleavesTheTwoPhotos(t *testing.T) {
	f := Frame{A: Photo{Image: solid(1200, 1600, red)}, B: Photo{Image: solid(900, 900, blue)}}
	page, err := f.Render(DefaultDPI, false)
	if err != nil {
		t.Fatal(err)
	}
	if b := page.Bounds(); b.Dx() != 3508 || b.Dy() != 2480 {
		t.Fatalf("page = %v, want 3508x2480", b)
	}
	k := DefaultDPI / 25.4
	mid := func(strip int) color.RGBA {
		x := int((stripX0 + (float64(strip)+0.5)*stripW) * k)
		return page.RGBAAt(x, int(105*k))
	}
	for strip := 0; strip < 2*StripsPerPhoto; strip++ {
		want := red
		if strip%2 == 1 {
			want = blue
		}
		if got := mid(strip); got != want {
			t.Fatalf("strip %d = %v, want %v", strip+1, got, want)
		}
	}
	// Outside the strips, the page is white.
	if got := page.RGBAAt(int(3*k), int(105*k)); got != (color.RGBA{255, 255, 255, 255}) {
		t.Fatalf("margin = %v", got)
	}
}

// Each photo is cut into nine columns in order, left to right, not mirrored:
// the left half of photo A lands in the first strips of A.
func TestStripsKeepThePhotosLeftToRightOrder(t *testing.T) {
	half := image.NewRGBA(image.Rect(0, 0, 1260, 1900))
	draw.Draw(half, image.Rect(0, 0, 630, 1900), image.NewUniform(red), image.Point{}, draw.Src)
	draw.Draw(half, image.Rect(630, 0, 1260, 1900), image.NewUniform(blue), image.Point{}, draw.Src)
	page, err := Frame{A: Photo{Image: half}}.Render(DefaultDPI, false)
	if err != nil {
		t.Fatal(err)
	}
	k := DefaultDPI / 25.4
	at := func(i int) color.RGBA { // A's i-th strip (0-based) is page strip 2i
		return page.RGBAAt(int((stripX0+(float64(2*i)+0.5)*stripW)*k), int(105*k))
	}
	if at(0) != red || at(8) != blue {
		t.Fatalf("first A strip %v, last A strip %v", at(0), at(8))
	}
}

func TestPDFAndPNGAreWellFormed(t *testing.T) {
	f := Frame{A: Photo{Image: solid(800, 1200, red)}, B: Photo{Image: solid(800, 1200, blue)}, Decor: true}
	pdf, err := f.PDF(100)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) ||
		!bytes.Contains(pdf, []byte("re S")) {
		t.Fatal("PDF missing its header, trailer or vector lines")
	}
	pngBytes, err := f.PNG(100)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := image.Decode(bytes.NewReader(pngBytes)); err != nil {
		t.Fatalf("PNG does not decode: %v", err)
	}
}

// A portrait phone photo stored sideways with orientation 6 comes out upright.
func TestDecodePhotoAppliesEXIFOrientation(t *testing.T) {
	img := solid(40, 20, red) // stored landscape
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	// Splice an APP1 Exif segment with orientation 6 after SOI.
	exif := []byte("Exif\x00\x00II*\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00\x06\x00\x00\x00\x00\x00\x00\x00")
	seg := append([]byte{0xFF, 0xE1, byte((len(exif) + 2) >> 8), byte(len(exif) + 2)}, exif...)
	data := append(append([]byte{0xFF, 0xD8}, seg...), buf.Bytes()[2:]...)
	out, err := DecodePhoto(data)
	if err != nil {
		t.Fatal(err)
	}
	if b := out.Bounds(); b.Dx() != 20 || b.Dy() != 40 {
		t.Fatalf("decoded %v, want 20x40 (rotated upright)", b)
	}
}
