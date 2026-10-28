package httpapi

// What somebody flagged at a station, for the queues to show.
//
// THIS IS NOT issue_reason. The two are different taxonomies on purpose and
// the distinction is easy to get wrong - it was got wrong once already while
// building this screen:
//
//   - issue_reason is PRE-PRINT validation: missing catalogue data. Setting it
//     excludes a job from batching, which is useless and actively harmful for
//     a part somebody is already holding.
//   - A station issue is a PHYSICAL DEFECT found after printing. It is
//     recorded as an event and leaves the job in its queue, where the next
//     person to pick it up needs to see it.
//
// So a screen asking "what did somebody flag" has to read the events, and a
// screen reading issue_reason instead shows nothing at all for every issue the
// floor actually raised.

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
)

func (s *Server) registerStationIssues(r *gin.Engine) {
	g := r.Group("/brands/:slug/production/station-issues")
	g.Use(s.guards.RequireUser(), s.guards.RequirePermission(auth.ProductionRead.Key()))
	g.GET("", s.listStationIssues)
}

type openStationIssueResponse struct {
	JobID string `json:"job_id"`
	// Stage is where it was raised: assembly, finishing or qc. Carried
	// because a defect found at QC still matters when the job reaches
	// packaging, and the row should say where it came from.
	Stage    string `json:"stage"`
	Reason   string `json:"reason"`
	Comment  string `json:"comment"`
	ActorID  string `json:"actor_id"`
	RaisedAt string `json:"raised_at"`
}

func (s *Server) listStationIssues(c *gin.Context) {
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	ctx := c.Request.Context()

	// SCOPED TO WHAT IS ON THE BOARD. Every issue ever raised would be a
	// growing list nobody asked for; the queues only need the jobs they are
	// about to render.
	waiting, err := s.store.Q.ListJobsAwaitingStations(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the station queues.")
		return
	}
	if len(waiting) == 0 {
		c.JSON(http.StatusOK, gin.H{"items": []openStationIssueResponse{}})
		return
	}

	ids := make([]uuid.UUID, 0, len(waiting))
	for _, row := range waiting {
		ids = append(ids, row.ID)
	}

	rows, err := s.store.Q.ListLatestStationIssues(ctx, ids)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the station issues.")
		return
	}

	out := make([]openStationIssueResponse, 0, len(rows))
	for _, r := range rows {
		item := openStationIssueResponse{
			JobID:   r.JobID.String(),
			ActorID: r.ActorID,
		}
		if r.Stage != nil {
			item.Stage = *r.Stage
		}
		if r.Reason != nil {
			item.Reason = *r.Reason
		}
		if r.Comment != nil {
			item.Comment = *r.Comment
		}
		if r.CreatedAt.Valid {
			item.RaisedAt = r.CreatedAt.Time.Format(time.RFC3339)
		}
		out = append(out, item)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}
