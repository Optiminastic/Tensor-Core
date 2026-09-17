package httpapi

// Rebuilding one job's model, from the job page.
//
// Re-rendering existed only as cmd/rerender - a binary in the container - so
// from the floor there was no way to rebuild a model at all. That was tolerable
// while the only reason to re-render was a template fix, which is a deploy-time
// event somebody with a shell was doing anyway. It stopped being tolerable when
// a job could be individually wrong: five planks printed with the first line's
// names, and the operator looking at JOB-115059-2 - reading NAVYA & KRISHNA
// against an order for APRAJITA & AJAY - had no button to press.
//
// Status is deliberately not checked. A queued job's plank has not printed, so
// a better model reaches the floor; a completed job's plank HAS printed and the
// model is instead the record a reprint is built from and the thing that job
// page shows. Both are worth correcting, and refusing the second would leave
// exactly the case this exists for unreachable.
//
// The bed's merged plate is not touched: cmd/replate owns that, and it skips
// completed beds because their plate is the record of what actually printed.

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// rerenderProductionJob queues a fresh render of one job's model.
//
// Returns 202 rather than 200: OpenSCAD is 20-45 seconds of CPU and the render
// runs on the production worker, so the model is not new by the time this
// answers. The response carries the job as it stands, which is what the caller
// re-renders its page from.
func (s *Server) rerenderProductionJob(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	job, err := s.store.Q.GetProductionJobByID(ctx, id)
	if err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	// An uploaded design has no template to rebuild from, and saying so is more
	// use than queueing work that would fail with errNotPersonalisable.
	if !IsGeneratedProduct(deref(job.Sku), deref(job.ProductName)) {
		detail(c, http.StatusUnprocessableEntity,
			"This product's model is uploaded rather than rendered, so there is nothing to rebuild. Upload a new file instead.")
		return
	}
	// Nil on a service started without OpenSCAD - the API degrades to holding
	// personalised jobs rather than refusing to start, so the button has to
	// degrade the same way instead of returning a 500 nobody can act on.
	if s.modelEnqueuer == nil {
		detail(c, http.StatusConflict,
			"Model generation is not configured on this service.")
		return
	}
	if err := s.modelEnqueuer.Enqueue(ctx, id); err != nil {
		detail(c, http.StatusInternalServerError, "Could not queue the render.")
		return
	}
	c.JSON(http.StatusAccepted, s.singleJobDTO(ctx, job))
}
