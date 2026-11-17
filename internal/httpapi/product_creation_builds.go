package httpapi

// Background builds for Product Creation: print files cut by OpenSCAD, which
// only the production worker has. Asking for one records a build and queues
// it; the page polls the build and downloads the file when it lands.
//
// Each product supplies its own builder (see productBuilders); the recording,
// queueing, polling, storing and downloading are shared here.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// builtFile is what a product's builder hands back: the print file, and a
// preview PNG when the product's picture can only be drawn from the model.
type builtFile struct {
	name    string
	data    []byte
	preview []byte
	// parts are the separate files of a product that prints as several -
	// the Soulmate Combo's plank, rose and keychain - each downloadable on
	// its own beside data, which then holds all of them.
	parts []builtPart
}

// builtPart is one of a build's separate files, stored as parts/<name>.3mf.
type builtPart struct {
	name string
	data []byte
}

// productBuilder builds one product's print file for an order, on the worker.
type productBuilder func(ctx context.Context, s *Server, order gen.Order) (builtFile, error)

// productBuilders maps a build's product to its builder.
var productBuilders = map[string]productBuilder{
	moonLampProduct:   buildMoonLamp,
	pdnpProduct:       buildPDNPFront,
	soulmateProduct:   buildSoulmateCombo,
	duoSnapProduct:    buildDuoSnap,
	registryProduct:   registryBuilder(""),
	coupleDateProduct: registryBuilder(coupleDateCode),
}

// previewFileName is the preview stored beside a build's print file.
const previewFileName = "preview.png"

type productBuildResponse struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Filename string `json:"filename,omitempty"`
	Error    string `json:"error,omitempty"`
}

func buildDTO(b gen.ProductCreationBuild) productBuildResponse {
	return productBuildResponse{ID: b.ID.String(), Status: b.Status, Filename: deref(b.Filename), Error: deref(b.Error)}
}

// startProductBuild records and queues a build of product for order, or
// answers with a finished or running build of the same file instead.
func (s *Server) startProductBuild(c *gin.Context, order gen.Order, product, variant string) {
	if s.productBuilds == nil || s.storage == nil {
		detail(c, http.StatusConflict, "Background builds are not configured on this service.")
		return
	}
	ctx := c.Request.Context()
	brand := deref(order.BrandSlug)
	if b, err := s.store.Q.FindReusableProductCreationBuild(ctx, gen.FindReusableProductCreationBuildParams{
		BrandSlug: brand, Product: product, Variant: variant, OrderNumber: order.OrderNumber,
	}); err == nil {
		c.JSON(http.StatusOK, buildDTO(b))
		return
	}
	b, err := s.store.Q.InsertProductCreationBuild(ctx, gen.InsertProductCreationBuildParams{
		ID: uuid.New(), BrandSlug: brand, OrderNumber: order.OrderNumber,
		Product: product, Variant: variant, RequestedBy: currentUserID(c),
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not record the build.")
		return
	}
	if err := s.productBuilds.Enqueue(ctx, b.ID); err != nil {
		msg := "Could not queue the build."
		_ = s.store.Q.MarkProductCreationBuild(ctx, gen.MarkProductCreationBuildParams{ID: b.ID, Status: "failed", Error: &msg})
		detail(c, http.StatusInternalServerError, msg)
		return
	}
	c.JSON(http.StatusAccepted, buildDTO(b))
}

// productBuild answers GET .../builds/:id with the build's status.
func (s *Server) productBuild(c *gin.Context) {
	b, ok := s.loadBuild(c)
	if ok {
		c.JSON(http.StatusOK, buildDTO(b))
	}
}

// productBuildFile answers GET .../builds/:id/file with the finished file.
func (s *Server) productBuildFile(c *gin.Context) {
	b, ok := s.loadBuild(c)
	if !ok {
		return
	}
	if b.Status != "done" || b.StorageKey == nil {
		detail(c, http.StatusConflict, "That file is not ready yet.")
		return
	}
	s.streamStored(c, *b.StorageKey, deref(b.Filename), contentTypeOf(deref(b.Filename)))
}

// partName is what a build part may be called: lower-case words, nothing that
// could climb out of the build's folder.
var partName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// productBuildPart answers GET .../builds/:id/parts/:part with one of a
// multi-file build's separate files.
func (s *Server) productBuildPart(c *gin.Context) {
	b, ok := s.loadBuild(c)
	if !ok {
		return
	}
	part := c.Param("part")
	if !partName.MatchString(part) {
		detail(c, http.StatusBadRequest, "That is not a part of this build.")
		return
	}
	if b.Status != "done" || b.StorageKey == nil {
		detail(c, http.StatusConflict, "That file is not ready yet.")
		return
	}
	name := strings.TrimSuffix(deref(b.Filename), path.Ext(deref(b.Filename))) + "_" + part + ".3mf"
	s.streamStored(c, path.Dir(*b.StorageKey)+"/parts/"+part+".3mf", name, "model/3mf")
}

// contentTypeOf names a stored print file's type from its name.
func contentTypeOf(filename string) string {
	if strings.EqualFold(path.Ext(filename), ".zip") {
		return "application/zip"
	}
	return "model/3mf"
}

// productBuildPreview answers GET .../builds/:id/preview with the PNG drawn
// from the finished model, for products that have one.
func (s *Server) productBuildPreview(c *gin.Context) {
	b, ok := s.loadBuild(c)
	if !ok {
		return
	}
	if b.Status != "done" || b.StorageKey == nil {
		detail(c, http.StatusConflict, "That preview is not ready yet.")
		return
	}
	name := strings.TrimSuffix(deref(b.Filename), path.Ext(deref(b.Filename))) + "_preview.png"
	s.streamStored(c, path.Dir(*b.StorageKey)+"/"+previewFileName, name, "image/png")
}

func (s *Server) streamStored(c *gin.Context, key, filename, contentType string) {
	if s.storage == nil {
		detail(c, http.StatusConflict, "Object storage is not configured on this service.")
		return
	}
	obj, err := s.storage.Get(c.Request.Context(), key)
	if err != nil {
		detail(c, http.StatusBadGateway, "Could not read the file from storage.")
		return
	}
	defer func() { _ = obj.Body.Close() }()
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	c.Header("Content-Type", contentType)
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, obj.Body)
}

// loadBuild reads :id, scoped to the brand in the path.
func (s *Server) loadBuild(c *gin.Context) (gen.ProductCreationBuild, bool) {
	slug, ok := brandSlugParam(c)
	if !ok {
		return gen.ProductCreationBuild{}, false
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		detail(c, http.StatusBadRequest, "That is not a valid build id.")
		return gen.ProductCreationBuild{}, false
	}
	b, err := s.store.Q.GetProductCreationBuild(c.Request.Context(), id)
	if err != nil || b.BrandSlug != slug {
		detail(c, http.StatusNotFound, "That build does not exist.")
		return gen.ProductCreationBuild{}, false
	}
	return b, true
}

// ProductBuildWorker builds a recorded Product Creation file with the
// product's builder and stores it for the page to download.
type ProductBuildWorker struct {
	river.WorkerDefaults[production.ProductBuildArgs]
	server *Server
	logger *slog.Logger
}

// NewProductBuildWorker builds the worker.
func NewProductBuildWorker(server *Server, logger *slog.Logger) *ProductBuildWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &ProductBuildWorker{server: server, logger: logger}
}

// Timeout bounds one build; a 6 inch moon lamp takes about twelve seconds.
func (w *ProductBuildWorker) Timeout(*river.Job[production.ProductBuildArgs]) time.Duration {
	return 10 * time.Minute
}

// Work builds one file. A failure is written onto the build for the page to
// show; it is returned only on the first attempt, so River retries once.
func (w *ProductBuildWorker) Work(ctx context.Context, job *river.Job[production.ProductBuildArgs]) error {
	s := w.server
	b, err := s.store.Q.GetProductCreationBuild(ctx, job.Args.BuildID)
	if err != nil {
		return nil // the record is gone; nothing to build
	}
	_ = s.store.Q.MarkProductCreationBuild(ctx, gen.MarkProductCreationBuildParams{ID: b.ID, Status: "building"})
	start := time.Now()
	key, name, err := w.build(ctx, b)
	if err != nil {
		msg := err.Error()
		_ = s.store.Q.MarkProductCreationBuild(ctx, gen.MarkProductCreationBuildParams{ID: b.ID, Status: "failed", Error: &msg})
		w.logger.Warn("product build failed", "build", b.ID, "order", b.OrderNumber, "error", err)
		if job.Attempt < job.MaxAttempts {
			return err
		}
		return nil
	}
	_ = s.store.Q.MarkProductCreationBuild(ctx, gen.MarkProductCreationBuildParams{
		ID: b.ID, Status: "done", StorageKey: &key, Filename: &name,
	})
	w.logger.Info("product build done", "build", b.ID, "order", b.OrderNumber,
		"file", name, "took", time.Since(start).Round(time.Millisecond))
	return nil
}

func (w *ProductBuildWorker) build(ctx context.Context, b gen.ProductCreationBuild) (string, string, error) {
	s := w.server
	builder, ok := productBuilders[b.Product]
	if !ok {
		return "", "", fmt.Errorf("Tensor does not know how to build %q", b.Product)
	}
	if s.renderer == nil || !s.renderer.Available() {
		return "", "", fmt.Errorf("OpenSCAD is not available on this worker")
	}
	if s.storage == nil {
		return "", "", fmt.Errorf("object storage is not configured on this worker")
	}
	brand := b.BrandSlug
	order, err := s.store.Q.FindOrderByNumberForBrand(ctx, gen.FindOrderByNumberForBrandParams{
		BrandSlug: &brand, Number: b.OrderNumber,
	})
	if err != nil {
		return "", "", fmt.Errorf("order %s could not be read", b.OrderNumber)
	}
	f, err := builder(ctx, s, order)
	if err != nil {
		return "", "", err
	}
	dir := fmt.Sprintf("product-creation/%s", b.ID)
	key := dir + "/" + f.name
	if err := s.storage.Put(ctx, key, bytes.NewReader(f.data), int64(len(f.data)), contentTypeOf(f.name)); err != nil {
		return "", "", fmt.Errorf("could not store the file")
	}
	for _, p := range f.parts {
		if err := s.storage.Put(ctx, dir+"/parts/"+p.name+".3mf", bytes.NewReader(p.data),
			int64(len(p.data)), "model/3mf"); err != nil {
			return "", "", fmt.Errorf("could not store the %s file", p.name)
		}
	}
	if f.preview != nil {
		if err := s.storage.Put(ctx, dir+"/"+previewFileName, bytes.NewReader(f.preview),
			int64(len(f.preview)), "image/png"); err != nil {
			return "", "", fmt.Errorf("could not store the preview")
		}
	}
	return key, f.name, nil
}
