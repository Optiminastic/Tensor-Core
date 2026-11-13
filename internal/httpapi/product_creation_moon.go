package httpapi

// The 3D Moon Lamp in 4, 5 and 6 inch (SKU 3DMS01-4P / -5P / -6P): the
// customer's photo and words on a moon ball cut into a top and a glue-on cap
// - see internal/moonlamp/sized.go.
//
// The preview is built here, in a second. The print file is not: its top and
// cap are cut by OpenSCAD with the shop's joint, and only the production
// worker has OpenSCAD - so asking for it records a build and queues it, and
// the page polls the build (product_creation_builds.go).

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/moonlamp"
	"github.com/Optiminastic/tensor-core/internal/production"
)

const moonLampProduct = "moon-lamp"

// moonLampFields finds the sized moon line - the one whose SKU ends in 4P, 5P
// or 6P - and its photo and words. The photo is optional here: the shop's
// tool makes a plain moon when there is none.
func moonLampFields(order gen.Order) (moonlamp.Size, string, string, string) {
	var lines []production.LineItem
	if err := jsonUnmarshalLines(order.LineItems, &lines); err != nil {
		return moonlamp.Size{}, "", "", "This order's line items could not be read."
	}
	for _, li := range lines {
		size, ok := moonlamp.SizeForSKU(deref(li.SKU))
		if !ok {
			continue
		}
		photo, text, _ := lampLine(li)
		if len([]rune(text)) > maxLampText {
			return size, "", "", fmt.Sprintf("The lamp's text is %d characters; at most %d fit on the moon.", len([]rune(text)), maxLampText)
		}
		return size, photo, text, ""
	}
	return moonlamp.Size{}, "", "", fmt.Sprintf(
		"Order %s has no 3D Moon Lamp - its SKU would end in 4P, 5P or 6P (e.g. 3DMS01-6P).", order.OrderNumber)
}

// buildMoonMap fetches the photo, when there is one, and composes the moon.
func buildMoonMap(ctx context.Context, photoURL, text string) (*moonlamp.Map, error) {
	if photoURL == "" {
		return moonlamp.BuildWith(moonlamp.Sized(), nil, text, "")
	}
	photo, err := fetchPhoto(ctx, photoURL)
	if err != nil {
		return nil, fmt.Errorf("could not load the photo: %w", err)
	}
	return moonlamp.BuildWith(moonlamp.Sized(), photo, text, "")
}

// moonLampPreview answers POST .../moon-lamp with the lit, unrolled moon.
func (s *Server) moonLampPreview(c *gin.Context) {
	order, _, ok := s.productCreationOrder(c, []string{"png"})
	if !ok {
		return
	}
	size, photo, text, problem := moonLampFields(order)
	if problem != "" {
		detail(c, http.StatusUnprocessableEntity, problem)
		return
	}
	m, err := buildMoonMap(c.Request.Context(), photo, text)
	if err != nil {
		detail(c, http.StatusBadGateway, err.Error())
		return
	}
	png, err := m.Preview()
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not draw the moon.")
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`,
		size.Filename(order.OrderNumber, "preview.png")))
	c.Data(http.StatusOK, "image/png", png)
}

// startMoonLampBuild answers POST .../moon-lamp/builds: the 3MF is built in
// the background, one build per size.
func (s *Server) startMoonLampBuild(c *gin.Context) {
	order, _, ok := s.productCreationOrder(c, []string{"3mf"})
	if !ok {
		return
	}
	size, _, _, problem := moonLampFields(order)
	if problem != "" {
		detail(c, http.StatusUnprocessableEntity, problem)
		return
	}
	s.startProductBuild(c, order, moonLampProduct, strconv.Itoa(size.Inches))
}

// buildMoonLamp builds a recorded moon lamp: the skin here, the top and cap
// cut by OpenSCAD side by side, then the Bambu project with the stands.
func buildMoonLamp(ctx context.Context, s *Server, order gen.Order) (builtFile, error) {
	size, photo, text, problem := moonLampFields(order)
	if problem != "" {
		return builtFile{}, fmt.Errorf("%s", problem)
	}
	m, err := buildMoonMap(ctx, photo, text)
	if err != nil {
		return builtFile{}, err
	}
	top, cap, err := cutMoon(ctx, s, size, m.SkinSTL(size))
	if err != nil {
		return builtFile{}, err
	}
	kit, err := moonlamp.WriteKit(moonlamp.Plate(size, order.OrderNumber, top, cap), moonlamp.MoonPrintSettings)
	if err != nil {
		return builtFile{}, fmt.Errorf("could not write the 3MF: %w", err)
	}
	return builtFile{name: size.Filename(order.OrderNumber, "3mf"), data: kit}, nil
}

// cut renders the top and the cap at the same time.
func cutMoon(ctx context.Context, s *Server, size moonlamp.Size, skin []byte) ([]byte, []byte, error) {
	type result struct {
		part string
		stl  []byte
		err  error
	}
	results := make(chan result, 2)
	for _, part := range []string{"top", "cap"} {
		go func(part string) {
			stl, err := s.renderer.RunSource(ctx, moonlamp.SizedSCAD,
				map[string][]byte{"skin.stl": skin},
				map[string]string{"size_in": strconv.Itoa(size.Inches), "part": `"` + part + `"`},
				8*time.Minute)
			results <- result{part, stl, err}
		}(part)
	}
	var top, cap []byte
	var errs []string
	for range 2 {
		r := <-results
		switch {
		case r.err != nil:
			errs = append(errs, r.part+": "+r.err.Error())
		case r.part == "top":
			top = r.stl
		default:
			cap = r.stl
		}
	}
	if len(errs) > 0 {
		return nil, nil, fmt.Errorf("OpenSCAD could not cut the moon: %s", strings.Join(errs, "; "))
	}
	return top, cap, nil
}
