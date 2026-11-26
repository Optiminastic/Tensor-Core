package httpapi

// Custom photo: the customer photos of several orders, laid out on A4 sheets
// for the office printer (internal/photosheet).
//
// An operator types order numbers separated by commas. Every line of those
// orders that is a photo product contributes its photos, each at the size
// that product takes - the registry's Photo print size for the product, or
// the sizes Tensor ships for DuoSnap, fridge magnets, Family Crossword and
// social stickers (photo_print_rules.go).
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

// photoRulesFor resolves the photo sizes of an order line's SKU: the
// registry's when the product has any, otherwise the built-in ones. nil means
// the line is not a photo product.
type photoRulesFor func(sku string) []photoRule

// photoSpecFor is the size a line's photos print at. ok is false for a line
// that is not a photo product; problem says why a photo product cannot be
// laid out.
func photoSpecFor(li production.LineItem, rulesFor photoRulesFor) (spec photoSpec, problem string, ok bool) {
	sku := ""
	if li.SKU != nil {
		sku = strings.TrimSpace(*li.SKU)
	}
	rules := rulesFor(sku)
	if len(rules) == 0 {
		return photoSpec{}, "", false
	}
	spec, problem = matchPhotoRule(rules, li)
	if problem != "" {
		problem = sku + " " + problem
	}
	return spec, problem, true
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
func orderPhotos(order gen.Order, rulesFor photoRulesFor) (photos []sheetPhoto, skipped []string) {
	var lines []production.LineItem
	if err := jsonUnmarshalLines(order.LineItems, &lines); err != nil {
		return nil, []string{order.OrderNumber + ": the line items could not be read"}
	}
	for _, li := range lines {
		spec, problem, ok := photoSpecFor(li, rulesFor)
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
	// The server's write timeout (HTTP_WRITE_TIMEOUT_SECONDS, 60 s) is shorter
	// than downloading a big run of photos from the slow photo host. Hitting
	// it closed the connection AS the finished sheets went out: Tensor logged
	// a 200 after 81 s and the page said it could not reach Tensor-Core. This
	// route alone gets four minutes, matching the frontend proxy's limit.
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(4 * time.Minute))
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

// photoRulesResolver looks each SKU up once per request: the registry's
// sizes for its product when it has any, else the built-in ones.
func (s *Server) photoRulesResolver(ctx context.Context) photoRulesFor {
	seen := map[string][]photoRule{}
	return func(sku string) []photoRule {
		key := strings.ToLower(sku)
		if r, ok := seen[key]; ok {
			return r
		}
		var rules []photoRule
		if sku != "" {
			if rows, err := s.store.Q.PhotoSizesForSKU(ctx, sku); err == nil && len(rows) > 0 {
				rules = rulesFromRows(rows)
			}
		}
		if rules == nil {
			rules = builtinRulesForSKU(sku)
		}
		seen[key] = rules
		return rules
	}
}

// sheetPhotos finds the orders and gathers their photos.
func (s *Server) sheetPhotos(ctx context.Context, slug string, numbers []string) ([]sheetPhoto, []string, error) {
	var photos []sheetPhoto
	var skipped []string
	rulesFor := s.photoRulesResolver(ctx)
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
		p, sk := orderPhotos(order, rulesFor)
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
