package photoframe

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"strconv"
	"strings"
)

// PNG renders the page with its cut lines, at dpi.
func (f Frame) PNG(dpi float64) ([]byte, error) {
	page, err := f.Render(dpi, true)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, page); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// PDF is the print file: the page as one JPEG with the cut and fold lines
// drawn as vectors on top, so they print as hairlines at any printer
// resolution - DnpFrame.SavePdf / WritePdf.
func (f Frame) PDF(dpi float64) ([]byte, error) {
	page, err := f.Render(dpi, false)
	if err != nil {
		return nil, err
	}
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, page, &jpeg.Options{Quality: 95}); err != nil {
		return nil, err
	}
	return writePDF(jpg.Bytes(), page.Bounds().Dx(), page.Bounds().Dy()), nil
}

func num(v float64) string {
	s := strconv.FormatFloat(v, 'f', 4, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

func pt(mm float64) float64 { return mm / 25.4 * 72 }

func rectOp(x, y, w, h, pageHpt float64) string {
	return fmt.Sprintf("%s %s %s %s re S\n", num(pt(x)), num(pageHpt-pt(y+h)), num(pt(w)), num(pt(h)))
}

func writePDF(jpg []byte, pw, ph int) []byte {
	W, H := pt(PageW), pt(PageH)
	var cs strings.Builder
	cs.WriteString("q " + num(W) + " 0 0 " + num(H) + " 0 0 cm /Im0 Do Q\n")
	cs.WriteString("0 0 0 RG 0 J 0 j\n")
	cs.WriteString(num(stripLinePt) + " w\n")
	for i := 0; i < 2*StripsPerPhoto; i++ {
		cs.WriteString(rectOp(stripX0+float64(i)*stripW, stripY, stripW, stripH, H))
	}
	cs.WriteString(num(borderLinePt) + " w\n")
	cs.WriteString(rectOp(borderX, borderY, borderW, borderH, H))
	content := cs.String()

	var out bytes.Buffer
	offs := make([]int, 7)
	w := func(s string) { out.WriteString(s) }
	w("%PDF-1.4\n")
	out.Write([]byte{0x25, 0xE2, 0xE3, 0xCF, 0xD3, 0x0A})
	offs[1] = out.Len()
	w("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	offs[2] = out.Len()
	w("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	offs[3] = out.Len()
	w("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 " + num(W) + " " + num(H) +
		"] /TrimBox [0 0 " + num(W) + " " + num(H) +
		"] /Resources << /XObject << /Im0 5 0 R >> >> /Contents 4 0 R >>\nendobj\n")
	offs[4] = out.Len()
	w(fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content))
	offs[5] = out.Len()
	w(fmt.Sprintf("5 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d "+
		"/ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n", pw, ph, len(jpg)))
	out.Write(jpg)
	w("\nendstream\nendobj\n")
	offs[6] = out.Len()
	w("6 0 obj\n<< /Producer (Tensor DNP Photo Frame) /Title (DNP photo frame) >>\nendobj\n")
	xref := out.Len()
	w("xref\n0 7\n0000000000 65535 f \n")
	for i := 1; i < 7; i++ {
		w(fmt.Sprintf("%010d 00000 n \n", offs[i]))
	}
	w(fmt.Sprintf("trailer\n<< /Size 7 /Root 1 0 R /Info 6 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref))
	return out.Bytes()
}

// DecodePhoto reads a JPEG or PNG and turns it upright. A phone stores a
// portrait photo sideways with an EXIF orientation tag; ignoring it would
// put the customer on their side across nine strips.
func DecodePhoto(data []byte) (image.Image, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("not a readable JPEG or PNG: %w", err)
	}
	if format == "jpeg" {
		img = orient(img, jpegOrientation(data))
	}
	return img, nil
}

// jpegOrientation reads the EXIF orientation tag (0x0112), 1 when absent.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		size := int(b[i+2])<<8 | int(b[i+3])
		if marker == 0xDA || size < 2 || i+2+size > len(b) {
			return 1
		}
		seg := b[i+4 : i+2+size]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return exifOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

func exifOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var u16 func([]byte) int
	var u32 func([]byte) int
	switch string(t[:2]) {
	case "II":
		u16 = func(p []byte) int { return int(p[0]) | int(p[1])<<8 }
		u32 = func(p []byte) int { return int(p[0]) | int(p[1])<<8 | int(p[2])<<16 | int(p[3])<<24 }
	case "MM":
		u16 = func(p []byte) int { return int(p[0])<<8 | int(p[1]) }
		u32 = func(p []byte) int { return int(p[0])<<24 | int(p[1])<<16 | int(p[2])<<8 | int(p[3]) }
	default:
		return 1
	}
	ifd := u32(t[4:8])
	if ifd+2 > len(t) {
		return 1
	}
	n := u16(t[ifd : ifd+2])
	for e := 0; e < n; e++ {
		off := ifd + 2 + e*12
		if off+12 > len(t) {
			return 1
		}
		if u16(t[off:off+2]) == 0x0112 {
			if v := u16(t[off+8 : off+10]); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orient applies an EXIF orientation (1-8) so the image reads upright.
func orient(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	src := toRGBA(img)
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	swap := o >= 5
	dw, dh := w, h
	if swap {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			switch o {
			case 2:
				nx, ny = w-1-x, y
			case 3:
				nx, ny = w-1-x, h-1-y
			case 4:
				nx, ny = x, h-1-y
			case 5:
				nx, ny = y, x
			case 6:
				nx, ny = h-1-y, x
			case 7:
				nx, ny = h-1-y, w-1-x
			case 8:
				nx, ny = y, w-1-x
			}
			dst.SetRGBA(nx, ny, src.RGBAAt(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
