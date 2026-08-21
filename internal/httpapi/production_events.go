package httpapi

// The job history: writing events from every mutating path, and reading one
// job's stream back.
//
// Reads are guarded by auth.AuditRead, which has existed in the catalog since
// the RBAC seed but guarded no route until now.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

type jobEventResponse struct {
	ID           string          `json:"id"`
	Seq          int64           `json:"seq"`
	EventType    string          `json:"event_type"`
	Stage        *string         `json:"stage"`
	Reason       *string         `json:"reason"`
	Comment      *string         `json:"comment"`
	ActorID      string          `json:"actor_id"`
	BatchID      *string         `json:"batch_id"`
	RelatedJobID *string         `json:"related_job_id"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    time.Time       `json:"created_at"`
}

func jobEventDTO(e gen.ProductionJobEvent) jobEventResponse {
	meta := json.RawMessage(e.Metadata)
	if len(meta) == 0 {
		meta = json.RawMessage("{}")
	}
	return jobEventResponse{
		ID: e.ID.String(), Seq: e.Seq, EventType: e.EventType, Stage: e.Stage,
		Reason: e.Reason, Comment: e.Comment, ActorID: e.ActorID,
		BatchID: uuidPtrStr(e.BatchID), RelatedJobID: uuidPtrStr(e.RelatedJobID),
		Metadata: meta, CreatedAt: db.Time(e.CreatedAt),
	}
}

func (s *Server) registerJobEvents(r *gin.Engine) {
	g := r.Group("/production-jobs")
	g.Use(s.guards.RequireUser())
	g.GET("/:id/events", s.guards.RequirePermission(auth.AuditRead.Key()), s.listJobEvents)
}

func (s *Server) listJobEvents(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := s.store.Q.GetProductionJobByID(ctx, id); err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	rows, err := s.store.Q.ListJobEvents(ctx, id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the job history.")
		return
	}
	out := make([]jobEventResponse, 0, len(rows))
	for _, e := range rows {
		out = append(out, jobEventDTO(e))
	}
	c.JSON(http.StatusOK, out)
}

// jobEvent describes one entry to append. Only JobID, EventType and ActorID are
// required; the rest are absent for events they do not apply to.
type jobEvent struct {
	JobID        uuid.UUID
	EventType    string
	Stage        string
	Reason       string
	Comment      *string
	ActorID      string
	BatchID      *uuid.UUID
	RelatedJobID *uuid.UUID
	Metadata     map[string]any
}

// recordJobEvent appends one history entry using the caller's queries handle,
// so it commits with the state change it describes - an event and the thing it
// records can never disagree.
//
// Errors are returned, not swallowed: an event that silently failed to write
// leaves a gap in a history the whole point of which is that it has none.
func recordJobEvent(ctx context.Context, q *gen.Queries, e jobEvent) error {
	metadata := []byte("{}")
	if len(e.Metadata) > 0 {
		encoded, err := json.Marshal(e.Metadata)
		if err != nil {
			return err
		}
		metadata = encoded
	}
	actor := e.ActorID
	if actor == "" {
		actor = systemActor
	}
	_, err := q.InsertJobEvent(ctx, gen.InsertJobEventParams{
		ID: uuid.New(), JobID: e.JobID, EventType: e.EventType,
		Stage: nilIfEmpty(e.Stage), Reason: nilIfEmpty(e.Reason), Comment: e.Comment,
		ActorID: actor, BatchID: e.BatchID, RelatedJobID: e.RelatedJobID,
		Metadata: metadata,
	})
	return err
}

// systemActor is the actor for an event no human triggered - a batch completing,
// a worker creating jobs from an order.
const systemActor = "system"

// RecordJobEvent appends one history entry for a caller outside the HTTP layer
// - the print simulator today, a Bambu bridge reporting real printer telemetry
// later.
//
// print.started and print.completed are declared in internal/production/
// events.go and, until this existed, had no writer at all: nothing in the
// system ever recorded when a job actually went on or came off a bed, so a
// job's history jumped from "created" straight to whatever an operator did at
// assembly. This is that missing writer.
func (s *Server) RecordJobEvent(ctx context.Context, jobID uuid.UUID, eventType string, batchID uuid.UUID, actor string) error {
	return recordJobEvent(ctx, s.store.Q, jobEvent{
		JobID: jobID, EventType: eventType, ActorID: actor, BatchID: &batchID,
	})
}
