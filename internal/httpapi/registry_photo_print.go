package httpapi

// A product's Photo print size: the size its customer photo prints at on the
// Custom Photo A4 sheets, and a sample PDF showing it.
//
// OPTIONAL. Most products carry no photo and leave this empty. A product that
// does carry one and has no sizes here falls back to the sizes Tensor ships
// (photo_print_rules.go), which this page shows so nobody has to guess what
// will print.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/photosheet"
)

const (
	maxPhotoSampleBytes = 20 << 20
	maxPhotoSizeRows    = 12
)

func (s *Server) registerRegistryPhotoPrint(g *gin.RouterGroup, read, manage gin.HandlerFunc) {
	g.GET("/products/:code/photo-print", read, s.getPhotoPrint)
	g.PUT("/products/:code/photo-print/sizes", manage, s.putPhotoPrintSizes)
	g.POST("/products/:code/photo-print/sample", manage, s.uploadPhotoPrintSample)
	g.GET("/products/:code/photo-print/sample", read, s.downloadPhotoPrintSample)
	g.DELETE("/products/:code/photo-print/sample", manage, s.deletePhotoPrintSample)
}

type photoSampleResponse struct {
	Filename   string    `json:"filename"`
	SizeBytes  int64     `json:"size_bytes"`
	UploadedBy string    `json:"uploaded_by"`
	UploadedAt time.Time `json:"uploaded_at"`
}

type photoPrintResponse struct {
	// Sizes are this product's own. Empty means none are set.
	Sizes []photoRule `json:"sizes"`
	// BuiltIn are the sizes Tensor prints this product at when Sizes is
	// empty - by its SKUs - or empty when it has none either.
	BuiltIn []photoRule          `json:"built_in"`
	Sample  *photoSampleResponse `json:"sample"`
}

func (s *Server) getPhotoPrint(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	resp, err := s.photoPrintFor(c, product)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the photo print size.")
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (s *Server) photoPrintFor(c *gin.Context, product gen.Product) (photoPrintResponse, error) {
	ctx := c.Request.Context()
	rows, err := s.store.Q.ListProductPhotoSizes(ctx, product.ID)
	if err != nil {
		return photoPrintResponse{}, err
	}
	resp := photoPrintResponse{Sizes: rulesFromRows(rows), BuiltIn: []photoRule{}}
	if variants, err := s.store.Q.ListVariantsForProduct(ctx, product.ID); err == nil {
		for _, v := range variants {
			if b := builtinRulesForSKU(deref(v.Sku)); len(b) > 0 {
				resp.BuiltIn = b
				break
			}
		}
	}
	sample, err := s.store.Q.GetProductPhotoSample(ctx, product.ID)
	switch {
	case err == nil:
		resp.Sample = &photoSampleResponse{Filename: sample.Filename, SizeBytes: sample.SizeBytes,
			UploadedBy: sample.UploadedBy, UploadedAt: db.Time(sample.CreatedAt)}
	case !isNoRows(err):
		return photoPrintResponse{}, err
	}
	return resp, nil
}

// putPhotoPrintSizes replaces the product's sizes. An empty list clears them,
// so the product prints at its built-in size again, or not at all.
func (s *Server) putPhotoPrintSizes(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	var req struct {
		Sizes []photoRule `json:"sizes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "Send the photo sizes as a list.")
		return
	}
	rules, refusal := cleanPhotoRules(req.Sizes)
	if refusal != "" {
		detail(c, http.StatusUnprocessableEntity, refusal)
		return
	}
	ctx := c.Request.Context()
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.DeleteProductPhotoSizes(ctx, product.ID); err != nil {
			return err
		}
		for i, r := range rules {
			if err := q.InsertProductPhotoSize(ctx, gen.InsertProductPhotoSizeParams{
				ID: uuid.New(), ProductID: product.ID, WidthMm: r.WidthMM, HeightMm: r.HeightMM,
				Shape: r.Shape, WhenField: r.WhenField, WhenValue: r.WhenValue,
				Position: int32(i), UpdatedBy: currentUserID(c),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the photo sizes.")
		return
	}
	s.getPhotoPrint(c)
}

// cleanPhotoRules checks every size fits the sheet and says exactly which row
// does not. Millimetres, rounded to a tenth.
func cleanPhotoRules(in []photoRule) ([]photoRule, string) {
	if len(in) > maxPhotoSizeRows {
		return nil, fmt.Sprintf("At most %d photo sizes per product.", maxPhotoSizeRows)
	}
	usableW := photosheet.PageW - 2*photosheet.MarginMM
	usableH := photosheet.PageH - 2*photosheet.MarginMM - photosheet.LabelMM
	defaults := 0
	out := make([]photoRule, 0, len(in))
	for i, r := range in {
		row := fmt.Sprintf("Size %d", i+1)
		r.Shape = strings.ToLower(strings.TrimSpace(r.Shape))
		if r.Shape == "" {
			r.Shape = shapeRect
		}
		if r.Shape != shapeRect && r.Shape != shapeCircle {
			return nil, row + ": the shape must be rect or circle."
		}
		if r.Shape == shapeCircle {
			r.HeightMM = r.WidthMM
		}
		r.WidthMM, r.HeightMM = math.Round(r.WidthMM*10)/10, math.Round(r.HeightMM*10)/10
		if r.WidthMM <= 0 || r.HeightMM <= 0 {
			return nil, row + ": give a width and a height above zero."
		}
		short, long := math.Min(r.WidthMM, r.HeightMM), math.Max(r.WidthMM, r.HeightMM)
		if short > usableW || long > usableH {
			return nil, fmt.Sprintf("%s: %.1f x %.1f mm does not fit on an A4 sheet (at most %.0f x %.0f mm).",
				row, r.WidthMM, r.HeightMM, usableW, usableH)
		}
		r.WhenField, r.WhenValue = strings.TrimSpace(r.WhenField), strings.TrimSpace(r.WhenValue)
		if (r.WhenField == "") != (r.WhenValue == "") {
			return nil, row + ": give both the order field and its value, or neither."
		}
		if r.WhenField == "" {
			defaults++
		}
		out = append(out, r)
	}
	if defaults > 1 {
		return nil, "Only one size can be the default (no order field); give the others a field and value."
	}
	return out, ""
}

func (s *Server) uploadPhotoPrintSample(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	if s.storage == nil {
		detail(c, http.StatusServiceUnavailable, "File storage is not configured, so the sample cannot be kept.")
		return
	}
	data, filename, ok := readPDFUpload(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	sum := sha256.Sum256(data)
	key := fmt.Sprintf("photo-samples/%s/%s.pdf", strings.ToLower(product.Code), hex.EncodeToString(sum[:]))
	if err := s.storage.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "application/pdf"); err != nil {
		obs.FromContext(ctx).Error("could not store a photo sample", "product", product.Code, "error", err)
		detail(c, http.StatusBadGateway, "Could not store the sample PDF.")
		return
	}
	if _, err := s.store.Q.UpsertProductPhotoSample(ctx, gen.UpsertProductPhotoSampleParams{
		ProductID: product.ID, Filename: filename, StorageKey: key,
		SizeBytes: int64(len(data)), UploadedBy: currentUserID(c),
	}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the sample PDF.")
		return
	}
	s.getPhotoPrint(c)
}

// readPDFUpload reads the "file" field and checks it really is a PDF.
func readPDFUpload(c *gin.Context) ([]byte, string, bool) {
	header, err := c.FormFile("file")
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "Attach the sample PDF.")
		return nil, "", false
	}
	if header.Size > maxPhotoSampleBytes {
		detail(c, http.StatusUnprocessableEntity, "That PDF is larger than 20 MB.")
		return nil, "", false
	}
	src, err := header.Open()
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the uploaded file.")
		return nil, "", false
	}
	defer func() { _ = src.Close() }()
	data, err := io.ReadAll(io.LimitReader(src, maxPhotoSampleBytes+1))
	if err != nil || len(data) > maxPhotoSampleBytes {
		detail(c, http.StatusInternalServerError, "Could not read the uploaded file.")
		return nil, "", false
	}
	if !bytes.HasPrefix(bytes.TrimLeft(data[:min(len(data), 1024)], "\x00\t\r\n "), []byte("%PDF-")) {
		detail(c, http.StatusUnprocessableEntity, "That file is not a PDF. Upload the sample as a .pdf.")
		return nil, "", false
	}
	return data, filepath.Base(header.Filename), true
}

func (s *Server) downloadPhotoPrintSample(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	sample, err := s.store.Q.GetProductPhotoSample(ctx, product.ID)
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "This product has no sample PDF.")
		return
	}
	if err != nil || s.storage == nil {
		detail(c, http.StatusInternalServerError, "Could not read the sample PDF.")
		return
	}
	obj, err := s.storage.Get(ctx, sample.StorageKey)
	if err != nil {
		detail(c, http.StatusBadGateway, "Could not fetch the sample PDF from storage.")
		return
	}
	defer func() { _ = obj.Body.Close() }()
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, sample.Filename))
	c.DataFromReader(http.StatusOK, obj.Size, "application/pdf", obj.Body, nil)
}

func (s *Server) deletePhotoPrintSample(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	if err := s.store.Q.DeleteProductPhotoSample(c.Request.Context(), product.ID); err != nil {
		detail(c, http.StatusInternalServerError, "Could not remove the sample PDF.")
		return
	}
	s.getPhotoPrint(c)
}
