package photosheet

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"strconv"
	"strings"
)

// PreviewDPI draws the preview small enough to send, large enough to see a
// face cropped badly.
const PreviewDPI = 72.0

// PreviewPNG is every sheet side by side on grey, cut lines drawn - the
// picture an operator checks before printing.
func PreviewPNG(sheets []Sheet, dpi float64) ([]byte, error) {
	if len(sheets) == 0 {
		return nil, fmt.Errorf("no sheets")
	}
	pages := make([]*image.RGBA, len(sheets))
	for i, s := range sheets {
		pages[i] = s.Render(dpi, true)
	}
	pw, ph := pages[0].Bounds().Dx(), pages[0].Bounds().Dy()
	gutter := max(8, pw/20)
	out := image.NewRGBA(image.Rect(0, 0, gutter+len(pages)*(pw+gutter), ph+2*gutter))
	draw.Draw(out, out.Bounds(), image.NewUniform(color.RGBA{0xd9, 0xd9, 0xd9, 0xff}), image.Point{}, draw.Src)
	for i, pg := range pages {
		blit(out, pg, image.Pt(gutter+i*(pw+gutter), gutter))
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// PDF is the print file: one A4 page per sheet, each the sheet as a JPEG at
// dpi with the cut lines round every square photo drawn on top as vectors,
// so they print as hairlines. A round sticker's ring is its cut line.
func PDF(sheets []Sheet, dpi float64) ([]byte, error) {
	if len(sheets) == 0 {
		return nil, fmt.Errorf("no sheets")
	}
	pages := make([]pdfPage, len(sheets))
	for i, s := range sheets {
		pg := s.Render(dpi, false)
		var jpg bytes.Buffer
		if err := jpeg.Encode(&jpg, pg, &jpeg.Options{Quality: 92}); err != nil {
			return nil, err
		}
		pages[i] = pdfPage{jpg: jpg.Bytes(), w: pg.Bounds().Dx(), h: pg.Bounds().Dy(), content: cutLines(s)}
	}
	return writePDF(pages), nil
}

type pdfPage struct {
	jpg     []byte
	w, h    int
	content string
}

func num(v float64) string {
	s := strconv.FormatFloat(v, 'f', 3, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

func pt(mm float64) float64 { return mm / 25.4 * 72 }

// cutLines is the page's content stream: the image, then a 0.25 pt grey
// rectangle round each square photo.
func cutLines(s Sheet) string {
	W, H := pt(PageW), pt(PageH)
	var b strings.Builder
	b.WriteString("q " + num(W) + " 0 0 " + num(H) + " 0 0 cm /Im0 Do Q\n")
	b.WriteString("0.6 G 0.25 w\n")
	for _, p := range s.Photos {
		if p.Circle {
			continue
		}
		r, _ := p.rects()
		x, y := pt(r.Min[0]), H-pt(r.Max[1])
		b.WriteString(num(x) + " " + num(y) + " " + num(pt(r.Max[0]-r.Min[0])) + " " +
			num(pt(r.Max[1]-r.Min[1])) + " re S\n")
	}
	return b.String()
}

// writePDF writes a minimal PDF 1.4: catalog, pages, then per page a page
// object, its content stream and its image.
func writePDF(pages []pdfPage) []byte {
	W, H := num(pt(PageW)), num(pt(PageH))
	n := 2 + 3*len(pages) + 1
	offs := make([]int, n+1)
	var out bytes.Buffer
	w := func(s string) { out.WriteString(s) }
	w("%PDF-1.4\n")
	out.Write([]byte{0x25, 0xE2, 0xE3, 0xCF, 0xD3, 0x0A})
	offs[1] = out.Len()
	w("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = strconv.Itoa(3+3*i) + " 0 R"
	}
	offs[2] = out.Len()
	w(fmt.Sprintf("2 0 obj\n<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n", strings.Join(kids, " "), len(pages)))
	for i, p := range pages {
		pg, ct, im := 3+3*i, 4+3*i, 5+3*i
		offs[pg] = out.Len()
		w(fmt.Sprintf("%d 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %s %s] "+
			"/Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>\nendobj\n", pg, W, H, im, ct))
		offs[ct] = out.Len()
		w(fmt.Sprintf("%d 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", ct, len(p.content), p.content))
		offs[im] = out.Len()
		w(fmt.Sprintf("%d 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d "+
			"/ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n",
			im, p.w, p.h, len(p.jpg)))
		out.Write(p.jpg)
		w("\nendstream\nendobj\n")
	}
	info := n
	offs[info] = out.Len()
	w(fmt.Sprintf("%d 0 obj\n<< /Producer (Tensor) /Title (Custom photo sheets) >>\nendobj\n", info))
	xref := out.Len()
	w(fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", n+1))
	for i := 1; i <= n; i++ {
		w(fmt.Sprintf("%010d 00000 n \n", offs[i]))
	}
	w(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R /Info %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", n+1, info, xref))
	return out.Bytes()
}
