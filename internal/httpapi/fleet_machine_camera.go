package httpapi

// GET /machine-fleet/:id/camera/stream - proxy one printer's live MJPEG feed.
//
// Tensor stands in the middle rather than letting the browser fetch from
// BambuBuddy directly, for two reasons that both matter:
//
//   - Reachability. BambuBuddy sits on the Tailscale network with the printers.
//     An operator's browser does not, so an <img> pointing at 100.x.y.z:8000
//     would work only on the machine running BambuBuddy.
//   - Credentials. Reaching it needs the BambuBuddy API key, which must never
//     be in a URL a browser holds. The proxy keeps the key server-side and mints
//     a short-lived stream token per viewing session.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// Frame-rate bounds for the proxied stream.
//
// These come from measuring the real feed, not from taste. An H2C frame is
// ~311 KB, so 5 fps sustains ~1.4 MB/s - about 12 Mbps of continuous UPLOAD
// from a laptop on a domestic connection, which is more than many have and
// enough to disrupt the printer control traffic sharing that link.
//
// 2 fps costs ~0.6 MB/s and still shows a part coming loose or a spaghetti
// failure long before the print is wasted, because a print is a slow subject.
// The ceiling exists because fps arrives in a query string: without it, one
// hand-edited URL could saturate the link for everyone.
const (
	defaultCameraFPS = 2
	maxCameraFPS     = 10
)

func (s *Server) streamMachineCamera(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if !s.bambu.Configured() {
		detail(c, http.StatusConflict, "BambuBuddy is not configured on this service.")
		return
	}
	ctx := c.Request.Context()
	log := obs.FromContext(ctx)

	machine, err := s.store.Q.GetFleetMachine(ctx, id)
	if err != nil {
		dbError(c, err, "That machine does not exist.", "Could not load the machine.")
		return
	}

	printerID, err := s.bambuPrinterIDFor(ctx, machine.MachineID)
	if err != nil || printerID < 0 {
		detail(c, http.StatusConflict, "This machine could not be matched to a printer in BambuBuddy.")
		return
	}

	token, err := s.bambu.CreateStreamToken(ctx)
	if err != nil {
		log.Error("could not mint a camera stream token", "machine", id, "error", err)
		var reason bambubuddy.ReasonError
		if errors.As(err, &reason) {
			detail(c, http.StatusBadGateway, reason.Reason)
			return
		}
		detail(c, http.StatusBadGateway, "Could not start the camera stream.")
		return
	}

	stream, err := s.bambu.OpenStream(ctx, printerID, cameraFPS(c.Query("fps")), token)
	if err != nil {
		log.Warn("could not open the camera stream", "machine", id, "error", err)
		var reason bambubuddy.ReasonError
		if errors.As(err, &reason) {
			detail(c, http.StatusBadGateway, reason.Reason)
			return
		}
		detail(c, http.StatusBadGateway, "Could not open the camera stream.")
		return
	}
	defer func() { _ = stream.Close() }()

	// Ask BambuBuddy to shut the camera down once nobody is watching. It would
	// stop on its own, but this closes the window - the far end is a printer on
	// someone's home network, and leaving its camera running because a browser
	// tab closed is not a cost this service should impose.
	//
	// A fresh context on purpose: the request context is already cancelled by
	// the time this runs, which is exactly when the stop needs to be sent.
	defer func() {
		stopCtx, cancel := detachedContext(ctx)
		defer cancel()
		if err := s.bambu.StopStream(stopCtx, printerID); err != nil {
			log.Debug("could not stop the camera stream", "machine", id, "error", err)
		}
	}()

	// The upstream content type carries the multipart boundary the browser needs
	// to split frames, so it is passed through rather than reconstructed.
	contentType := stream.ContentType
	if contentType == "" {
		contentType = "multipart/x-mixed-replace"
	}
	c.Header("Content-Type", contentType)
	// A live feed must never be cached, by the browser or anything in between:
	// a cached MJPEG response is a still image that claims to be live.
	c.Header("Cache-Control", "no-store, no-transform")
	c.Header("X-Accel-Buffering", "no") // tells nginx-style proxies not to buffer
	c.Status(http.StatusOK)

	// Copy frames through until the viewer goes away. c.Stream handles the
	// flushing that MJPEG needs - without it frames pile up in a buffer and the
	// picture arrives in bursts - and returns when the client disconnects.
	c.Stream(func(w io.Writer) bool {
		if _, err := io.CopyN(w, stream.Body, streamChunkBytes); err != nil {
			return false // upstream ended, or the viewer left
		}
		return true
	})
}

// detachedContext carries a request's values (request id, logger) into work
// that must outlive the request itself, with its own short deadline.
//
// context.WithoutCancel keeps the observability context - so the stop attempt
// still logs under the same request id - while shedding the cancellation that
// has already fired. Without it, the stop call is born cancelled and never
// reaches BambuBuddy.
func detachedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), stopStreamTimeout)
}

// stopStreamTimeout bounds the courtesy stop. It is a best-effort call made
// after the viewer has gone, so it must not linger.
const stopStreamTimeout = 5 * time.Second

// streamChunkBytes is how much is forwarded per flush. Small enough that a
// frame is not held back waiting for the buffer to fill, large enough not to
// syscall per byte.
const streamChunkBytes = 32 << 10 // 32 KiB

// cameraFPS parses the caller's requested frame rate, clamped to something the
// link can carry. An unparseable or absent value takes the default rather than
// failing the request: a bad query string should not cost an operator the
// picture.
func cameraFPS(raw string) int {
	fps, err := strconv.Atoi(raw)
	if err != nil || fps <= 0 {
		return defaultCameraFPS
	}
	if fps > maxCameraFPS {
		return maxCameraFPS
	}
	return fps
}

func (s *Server) registerFleetMachineCamera(g *gin.RouterGroup) {
	// machine:read, not machine:manage: watching a printer observes it, it does
	// not change anything about the machine or the queue.
	g.GET("/:id/camera/stream", s.guards.RequirePermission(auth.MachineRead.Key()), s.streamMachineCamera)
}
