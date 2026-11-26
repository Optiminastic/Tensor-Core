package httpapi

// Custom photo: the customer photos of several orders, laid out on A4 sheets
// for the office printer (internal/photosheet).
//
// An operator types order numbers separated by commas. Every line of those
// orders that is a photo product contributes its photos, each at the size
// that product takes:
//
//	DuoSnap (T3DPS-DUO-*)          65 x 105 mm
//	Photo fridge magnet (PFM-*)    56.6 x 59.7 mm
//	Family Crossword frame (FCF-*) by photo_frames: 3 -> 106 x 80,
//	                               4 -> 89 x 67, 6 -> 71 x 54 mm
//	Social sticker (TSM-*)         round, 51 mm when batch_size is Large,
//	                               otherwise 41 mm
//
// Nothing is stored; asking again builds the same sheets.

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/photosheet"
	"github.com/Optiminastic/tensor-core/internal/production"
)

const (
	// maxSheetOrders and maxSheetPhotos bound one request: a print run, not
	// a month of orders.
	maxSheetOrders = 50
	maxSheetPhotos = 150
)

// photoSpec is the printed size of one product's photo.
type photoSpec struct {
	w, h   float64
	circle bool
}

// crosswordSizes is the Family Crossword photo by how many frames it has.
var crosswordSizes = map[string]photoSpec{
	"3": {w: 106, h: 80},
	"4": {w: 89, h: 67},
	"6": {w: 71, h: 54},
}

// photoSpecFor is the size a line's photos print at. ok is false for a line
// that is not a photo product; problem says why a photo product cannot be
// laid out.
func photoSpecFor(li production.LineItem) (spec photoSpec, problem string, ok bool) {
	sku := ""
	if li.SKU != nil {
		sku = strings.ToUpper(strings.TrimSpace(*li.SKU))
	}
	switch {
	case strings.HasPrefix(sku, "T3DPS-DUO"):
		return photoSpec{w: 65, h: 105}, "", true
	case strings.HasPrefix(sku, "PFM"):
		return photoSpec{w: 56.6, h: 59.7}, "", true
	case strings.HasPrefix(sku, "FCF"):
		frames := lineProp(li, "photo frames")
		if s, known := crosswordSizes[frames]; known {
			return s, "", true
		}
		return photoSpec{}, fmt.Sprintf("%s has photo_frames %q; the photo size is known for 3, 4 or 6", sku, frames), true
	case strings.HasPrefix(sku, "TSM"):
		d := 41.0
		if strings.Contains(strings.ToLower(lineProp(li, "batch size")), "large") {
			d = 51
		}
		return photoSpec{w: d, h: d, circle: true}, "", true
	}
	return photoSpec{}, "", false
}

// lineProp is the value of the property whose normalised name is key.
func lineProp(li production.LineItem, key string) string {
	for _, p := range li.Properties {
		if normalisePropKey(p.Name) == key {
			return strings.TrimSpace(p.Value)
		}
	}
	return ""
}

// linePhotoURLs are the photo links on a line, in the order the customer gave
// them: every https value of a property the customer filled in.
func linePhotoURLs(li production.LineItem) []string {
	var out []string
	for _, p := range li.Properties {
		v := strings.TrimSpace(p.Value)
		if strings.HasPrefix(p.Name, "_") || !strings.HasPrefix(strings.ToLower(v), "https://") {
			continue
		}
		out = append(out, v)
	}
	return out
}

// sheetPhoto is one photo to print, before it is downloaded.
type sheetPhoto struct {
	url   string
	spec  photoSpec
	label string
}

// sheetSummary rides back in the X-Photo-Sheet header, so the page can say
// what was laid out and what was left out. ASCII only.
type sheetSummary struct {
	Sheets      int       `json:"sheets"`
	Photos      int       `json:"photos"`
	Utilisation []float64 `json:"utilisation"`
	Skipped     []string  `json:"skipped"`
}

var orderSplit = regexp.MustCompile(`[\s,;]+`)

// parseOrderNumbers splits "116324, 115443 117584" into its numbers, each
// once, in the order typed.
func parseOrderNumbers(raw string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range orderSplit.Split(strings.TrimSpace(raw), -1) {
		if n == "" || seen[strings.ToLower(n)] {
			continue
		}
		seen[strings.ToLower(n)] = true
		out = append(out, n)
	}
	return out
}

// shortOrder is the order's number without the shop prefix, for a label.
func shortOrder(number string) string {
	if i := strings.LastIndex(number, "-"); i >= 0 && i < len(number)-1 {
		return number[i+1:]
	}
	return number
}

// orderPhotos are the photos one order contributes, and why any of its photo
// lines contributes none.
func orderPhotos(order gen.Order) (photos []sheetPhoto, skipped []string) {
	var lines []production.LineItem
	if err := jsonUnmarshalLines(order.LineItems, &lines); err != nil {
		return nil, []string{order.OrderNumber + ": the line items could not be read"}
	}
	for _, li := range lines {
		spec, problem, ok := photoSpecFor(li)
		if !ok {
			continue
		}
		sku := ""
		if li.SKU != nil {
			sku = *li.SKU
		}
		if problem != "" {
			skipped = append(skipped, order.OrderNumber+": "+problem)
			continue
		}
		urls := linePhotoURLs(li)
		if len(urls) == 0 {
			skipped = append(skipped, order.OrderNumber+": "+sku+" carries no photo")
			continue
		}
		for copyN := 0; copyN < max(1, li.Quantity); copyN++ {
			for i, u := range urls {
				label := shortOrder(order.OrderNumber) + " " + sku
				if len(urls) > 1 {
					label += " #" + strconv.Itoa(i+1)
				}
				photos = append(photos, sheetPhoto{url: u, spec: spec, label: label})
			}
		}
	}
	return photos, skipped
}

func (s *Server) customPhotoSheet(c *gin.Context) {
	var req struct {
		OrderNumber  string `json:"order_number"`
		OrderNumbers string `json:"order_numbers"`
		Format       string `json:"format"`
	}
	_ = c.ShouldBindJSON(&req)
	numbers := parseOrderNumbers(req.OrderNumbers + "," + req.OrderNumber)
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "png"
	}
	switch {
	case len(numbers) == 0:
		detail(c, http.StatusUnprocessableEntity, "Enter the order numbers, separated by commas.")
		return
	case len(numbers) > maxSheetOrders:
		detail(c, http.StatusUnprocessableEntity, fmt.Sprintf("At most %d orders at a time.", maxSheetOrders))
		return
	case format != "png" && format != "pdf":
		detail(c, http.StatusUnprocessableEntity, "Format must be one of: png, pdf.")
		return
	}
	slug, ok := brandSlugParam(c)
	if !ok || !s.brandMustExist(c, slug) {
		return
	}
	photos, skipped, err := s.sheetPhotos(c.Request.Context(), slug, numbers)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the orders.")
		return
	}
	s.answerPhotoSheet(c, photoSheetJob{photos: photos, skipped: skipped, format: format, numbers: numbers})
}

// sheetPhotos finds the orders and gathers their photos.
func (s *Server) sheetPhotos(ctx context.Context, slug string, numbers []string) ([]sheetPhoto, []string, error) {
	var photos []sheetPhoto
	var skipped []string
	for _, n := range numbers {
		order, err := s.store.Q.FindOrderByNumberForBrand(ctx, gen.FindOrderByNumberForBrandParams{
			BrandSlug: &slug, Number: n,
		})
		if isNoRows(err) {
			skipped = append(skipped, n+": no such order for this brand")
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		p, sk := orderPhotos(order)
		if len(p) == 0 && len(sk) == 0 {
			sk = []string{order.OrderNumber + ": no DuoSnap, fridge magnet, crossword or social sticker in it"}
		}
		photos = append(photos, p...)
		skipped = append(skipped, sk...)
	}
	return photos, skipped, nil
}

type photoSheetJob struct {
	photos  []sheetPhoto
	skipped []string
	format  string
	numbers []string
}

// answerPhotoSheet downloads the photos, lays them out and sends the file.
func (s *Server) answerPhotoSheet(c *gin.Context, job photoSheetJob) {
	if len(job.photos) > maxSheetPhotos {
		detail(c, http.StatusUnprocessableEntity, fmt.Sprintf(
			"These orders hold %d photos; at most %d at a time.", len(job.photos), maxSheetPhotos))
		return
	}
	items, failed := downloadSheetPhotos(c.Request.Context(), job.photos)
	skipped := append(job.skipped, failed...)
	if len(items) == 0 {
		msg := "No photos to print."
		if len(skipped) > 0 {
			msg += " " + strings.Join(skipped, "; ") + "."
		}
		detail(c, http.StatusUnprocessableEntity, msg)
		return
	}
	sheets, err := photosheet.Layout(items)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, contentType, err := renderPhotoSheets(sheets, job.format)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not build the photo sheets.")
		return
	}
	sum := sheetSummary{Sheets: len(sheets), Photos: len(items), Skipped: skipped}
	for _, sh := range sheets {
		sum.Utilisation = append(sum.Utilisation, float64(int(sh.Utilisation()*1000))/1000)
	}
	if b, err := json.Marshal(sum); err == nil {
		c.Header("X-Photo-Sheet", asciiOnly(string(b)))
	}
	name := "photos_" + shortOrder(job.numbers[0])
	if len(job.numbers) > 1 {
		name += fmt.Sprintf("_and_%d_more", len(job.numbers)-1)
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name+"."+job.format))
	c.Data(http.StatusOK, contentType, out)
}

func renderPhotoSheets(sheets []photosheet.Sheet, format string) ([]byte, string, error) {
	if format == "pdf" {
		b, err := photosheet.PDF(sheets, photosheet.DefaultDPI)
		return b, "application/pdf", err
	}
	b, err := photosheet.PreviewPNG(sheets, photosheet.PreviewDPI)
	return b, "image/png", err
}

// sheetPhotoClient is photoClient for one byte range of a photo
// (photo_fetch_parallel.go): the same refusals, a minute per range, and
// enough idle connections kept open for the ranges running side by side.
var sheetPhotoClient = &http.Client{
	Timeout: 60 * time.Second,
	Transport: &http.Transport{
		DialContext:         photoClient.Transport.(*http.Transport).DialContext,
		MaxIdleConnsPerHost: photoConnections,
	},
	CheckRedirect: photoClient.CheckRedirect,
}

// downloadSheetPhotos fetches each distinct photo once, all of them at once
// under the connection cap (photo_fetch_parallel.go). A photo that cannot be
// fetched is left out and named, rather than failing the run.
func downloadSheetPhotos(ctx context.Context, photos []sheetPhoto) ([]photosheet.Item, []string) {
	images := map[string]image.Image{}
	problems := map[string]string{}
	var mu sync.Mutex
	var g errgroup.Group
	g.SetLimit(photoConnections)
	seen := map[string]bool{}
	for _, p := range photos {
		if seen[p.url] {
			continue
		}
		seen[p.url] = true
		u := p.url
		g.Go(func() error {
			img, err := fetchPhotoFast(ctx, u)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				problems[u] = err.Error()
			} else {
				images[u] = img
			}
			return nil
		})
	}
	_ = g.Wait()
	var items []photosheet.Item
	var failed []string
	for _, p := range photos {
		img, ok := images[p.url]
		if !ok {
			failed = append(failed, p.label+": "+problems[p.url])
			continue
		}
		items = append(items, photosheet.Item{Image: img, WMM: p.spec.w, HMM: p.spec.h, Circle: p.spec.circle, Label: p.label})
	}
	return items, failed
}

// asciiOnly replaces anything outside printable ASCII, so the summary is a
// valid header value whatever an order's SKU holds.
func asciiOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return '?'
		}
		return r
	}, s)
}
