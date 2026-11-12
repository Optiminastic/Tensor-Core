package httpapi

// Product creation: print artwork made from one order, on demand.
//
// The DNP photo frame (DNPF) prints two customer photos, cut into strips and
// interleaved across an A4 page (internal/photoframe). The shop made it with a
// Windows tool and two photos saved by hand from the order; here an operator
// types the order number, and the photos the customer uploaded - LEFT PHOTO
// and RIGHT PHOTO on the order line - are fetched and laid out.
//
// Nothing is stored. The artwork is a function of the order, so asking again
// gives the same file, and a re-uploaded photo is picked up by asking again.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/photoframe"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// maxPhotoBytes bounds one customer photo. A phone JPEG is 2-8 MB.
const maxPhotoBytes = 25 << 20

func (s *Server) registerProductCreation(r *gin.Engine) {
	g := r.Group("/brands/:slug/product-creation")
	g.Use(s.guards.RequireUser())
	g.POST("/dnp-photo-frame", s.guards.RequirePermission(auth.ProductionRead.Key()), s.dnpPhotoFrame)
	g.POST("/night-lamp", s.guards.RequirePermission(auth.ProductionRead.Key()), s.nightLamp)
}

type productCreationRequest struct {
	OrderNumber string `json:"order_number"`
	// Format is the file wanted; the first of the product's formats when empty.
	Format string `json:"format"`
}

// productCreationOrder reads the request and finds its order, answering the
// caller itself and returning false when it cannot. formats lists what the
// product makes, the default first.
func (s *Server) productCreationOrder(c *gin.Context, formats []string) (gen.Order, string, bool) {
	var req productCreationRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.OrderNumber) == "" {
		detail(c, http.StatusUnprocessableEntity, "Enter the order number.")
		return gen.Order{}, "", false
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = formats[0]
	}
	if !slices.Contains(formats, format) {
		detail(c, http.StatusUnprocessableEntity, "Format must be one of: "+strings.Join(formats, ", ")+".")
		return gen.Order{}, "", false
	}
	slug, ok := brandSlugParam(c)
	if !ok || !s.brandMustExist(c, slug) {
		return gen.Order{}, "", false
	}
	number := strings.TrimSpace(req.OrderNumber)
	order, err := s.store.Q.FindOrderByNumberForBrand(c.Request.Context(), gen.FindOrderByNumberForBrandParams{
		BrandSlug: &slug, Number: number,
	})
	if isNoRows(err) {
		detail(c, http.StatusNotFound, fmt.Sprintf("No order %s for this brand.", number))
		return gen.Order{}, "", false
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the order.")
		return gen.Order{}, "", false
	}
	return order, format, true
}

// jsonUnmarshalLines reads an order's line items.
func jsonUnmarshalLines(raw []byte, lines *[]production.LineItem) error {
	return json.Unmarshal(raw, lines)
}

func (s *Server) dnpPhotoFrame(c *gin.Context) {
	order, format, ok := s.productCreationOrder(c, []string{"png", "pdf"})
	if !ok {
		return
	}
	ctx := c.Request.Context()
	left, right, problem := photoFrameURLs(order)
	if problem != "" {
		detail(c, http.StatusUnprocessableEntity, problem)
		return
	}

	frame := photoframe.Frame{Decor: true}
	for side, u := range []string{left, right} {
		img, err := fetchPhoto(ctx, u)
		if err != nil {
			which := []string{"left", "right"}[side]
			detail(c, http.StatusBadGateway, fmt.Sprintf("Could not load the %s photo: %v", which, err))
			return
		}
		if side == 0 {
			frame.A.Image = img
		} else {
			frame.B.Image = img
		}
	}

	var out []byte
	var err error
	contentType := "image/png"
	if format == "pdf" {
		out, err = frame.PDF(photoframe.DefaultDPI)
		contentType = "application/pdf"
	} else {
		out, err = frame.PNG(photoframe.DefaultDPI)
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not build the photo frame.")
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`,
		fmt.Sprintf("%s_DNP.%s", order.OrderNumber, format)))
	c.Data(http.StatusOK, contentType, out)
}

// photoFrameURLs finds the order line carrying a LEFT PHOTO and a RIGHT PHOTO,
// or says what is missing.
func photoFrameURLs(order gen.Order) (left, right, problem string) {
	var lines []production.LineItem
	if err := json.Unmarshal(order.LineItems, &lines); err != nil {
		return "", "", "This order's line items could not be read."
	}
	for _, li := range lines {
		var l, r string
		for _, p := range li.Properties {
			switch normalisePropKey(p.Name) {
			case "left photo":
				l = strings.TrimSpace(p.Value)
			case "right photo":
				r = strings.TrimSpace(p.Value)
			}
		}
		if l != "" || r != "" {
			if l == "" || r == "" {
				return "", "", fmt.Sprintf("Order %s has only one photo; the frame needs a LEFT PHOTO and a RIGHT PHOTO.", order.OrderNumber)
			}
			return l, r, ""
		}
	}
	return "", "", fmt.Sprintf("Order %s has no LEFT PHOTO / RIGHT PHOTO - is it a DNP photo frame order?", order.OrderNumber)
}

// photoClient fetches customer photos. HTTPS only, bounded in time and size,
// and refuses any address inside a private network: the URL comes from an
// order, which a customer typed, so it must not be able to reach this host's
// neighbours.
var photoClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{Timeout: 10 * time.Second, Control: refusePrivateAddress}).DialContext,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || req.URL.Scheme != "https" {
			return errors.New("refusing that redirect")
		}
		return nil
	},
}

func refusePrivateAddress(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("refusing to fetch from %s", host)
	}
	return nil
}

func fetchPhoto(ctx context.Context, raw string) (image.Image, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("the photo link is not an https URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := photoClient.Do(req)
	if err != nil {
		return nil, errors.New("the photo could not be downloaded")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the photo link answered %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPhotoBytes+1))
	if err != nil {
		return nil, errors.New("the photo could not be downloaded")
	}
	if len(data) > maxPhotoBytes {
		return nil, errors.New("the photo is larger than 25 MB")
	}
	return photoframe.DecodePhoto(data)
}
