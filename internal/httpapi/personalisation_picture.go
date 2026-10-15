package httpapi

// The customer's preview, as a picture.
//
// The page used to show the model in a WebGL viewer. The mesh was 129KB and
// three.js, which drew it, is about 700KB before @react-three/fiber and drei -
// five times the weight of what it displayed, downloaded, parsed and executed
// on a phone before anything appeared. On a real order on a real phone it
// never appeared at all: the page sat on "Loading preview…" indefinitely while
// the bundle compiled.
//
// So the picture is drawn here. Measured on the same plank: 99ms and 11.7KB,
// no JavaScript, no GPU, and it paints with the rest of the page.
//
// Drawn from the SAME BYTES the 3MF route serves - previewModelFor, hearts
// composed in and all - so the picture and the model can never disagree about
// what the customer is approving.

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// publicPersonalisationPicture draws one item as a PNG.
//
// ?view=right takes it from the other side. A Dual Name Plank carries one
// name read from each end, so a single view shows a customer half of what
// they bought - the page asks for both.
func (s *Server) publicPersonalisationPicture(c *gin.Context) {
	model, job, _, ok := s.previewModelFor(c)
	if !ok {
		return
	}

	parts, err := meshio.Read3MF(model)
	if err != nil || len(parts) == 0 {
		// The model is there and cannot be drawn. Said out loud because the
		// customer will see an empty frame and nothing else explains it.
		obs.FromContext(c.Request.Context()).Warn(
			"personalisation: the preview could not be drawn",
			"job", job.ID, "error", err, "parts", len(parts))
		detail(c, http.StatusNotFound, "This preview is still being built.")
		return
	}

	view := meshio.ViewFromLeft
	if c.Query("view") == "right" {
		view = meshio.ViewFromRight
	}
	png, err := meshio.RenderPreview(parts, meshio.PreviewOptions{View: view})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not draw the preview.")
		return
	}

	// Private and short-lived, like the model route. The page busts it with
	// the model version on every edit, so this only ever serves repeat views
	// of a picture that has not changed - a scroll back up the page, or the
	// second view being opened.
	c.Header("Cache-Control", "private, max-age=60")
	c.Data(http.StatusOK, "image/png", png)
}
