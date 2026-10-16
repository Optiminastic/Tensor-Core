package httpapi

// Fetching the attachments customers send, and handing them back to the
// dashboard.
//
// A webhook stores the message and a media id and stops there. This is the
// other half: a job pulls the bytes out of Meta and into object storage, and a
// route serves them to somebody looking at the conversation.
//
// WHY A JOB. The webhook handler is deliberately fast and dumb - Meta retries
// anything it is not told about quickly, and a retry re-ingests. A download is
// unbounded: six photographs and a sixteen-megabyte video is a perfectly
// ordinary damage report. So the handler records the id and this clears up
// afterwards, on the shared queue rather than the single-worker inbound one.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/storage"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/phone"
)

// How a message's attachment stands. The empty string is the fourth state and
// the commonest one: this message has no attachment at all.
const (
	mediaPending = "pending"
	mediaStored  = "stored"
	mediaFailed  = "failed"
)

// whatsappMediaUploader is who an attachment is recorded as having been
// uploaded by. file_assets.uploaded_by is a Better Auth user id elsewhere, with
// no foreign key, so a word reads correctly in its index.
const whatsappMediaUploader = "whatsapp"

// WhatsAppMediaWorker pulls one attachment into object storage.
type WhatsAppMediaWorker struct {
	river.WorkerDefaults[production.WhatsAppMediaArgs]
	server *Server
	logger *slog.Logger
}

func NewWhatsAppMediaWorker(s *Server, logger *slog.Logger) *WhatsAppMediaWorker {
	return &WhatsAppMediaWorker{server: s, logger: logger}
}

func (w *WhatsAppMediaWorker) Work(ctx context.Context, job *river.Job[production.WhatsAppMediaArgs]) error {
	// Cancelled, not retried. A process with no object storage will not grow
	// any, and a download it cannot store is work it must not keep attempting
	// - the row stays 'pending' so a process that does have storage can.
	if w.server.storage == nil {
		w.logger.Error("whatsapp media: object storage is not configured here, so nothing was fetched",
			"brand", job.Args.BrandSlug, "message", job.Args.MessageID)
		return river.JobCancel(errors.New("object storage is not configured"))
	}

	msg, err := w.server.store.Q.GetWhatsAppMessageByWAID(ctx, job.Args.MessageID)
	if err != nil {
		w.logger.Info("whatsapp media: that message is gone", "message", job.Args.MessageID)
		return river.JobCancel(err)
	}
	// Already settled, by an earlier attempt or a duplicate job.
	if msg.MediaStatus != mediaPending || msg.MediaID == "" {
		return nil
	}

	messenger := w.server.whatsappFor(ctx, msg.BrandSlug)
	if messenger == nil || !messenger.Configured() {
		w.logger.Warn("whatsapp media: WhatsApp is not connected for this brand",
			"brand", msg.BrandSlug, "message", job.Args.MessageID)
		return river.JobCancel(errors.New("WhatsApp is not connected for this brand"))
	}

	body, found, err := messenger.DownloadMedia(ctx, msg.MediaID)
	if err != nil {
		// LAST ATTEMPT ONLY. Meta's url lapses in five minutes and a 404 on
		// the second hop is ordinary; every retry starts again from the id, so
		// a failure is only final once River has stopped trying.
		if job.Attempt >= job.MaxAttempts {
			w.failMedia(ctx, job.Args.MessageID, err)
			return river.JobCancel(err)
		}
		return fmt.Errorf("fetch the attachment: %w", err)
	}
	defer func() { _ = body.Close() }()

	fileID := uuid.New()
	name := attachmentName(fileID, found.MimeType, msg.Kind)
	key := fmt.Sprintf("whatsapp/%s/%s", msg.BrandSlug, name)

	size, err := w.spoolAndStore(ctx, body, key, found.MimeType)
	if err != nil {
		if job.Attempt >= job.MaxAttempts {
			w.failMedia(ctx, job.Args.MessageID, err)
			return river.JobCancel(err)
		}
		return err
	}

	if _, err := w.server.store.Q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
		ID: fileID, Filename: name, ContentType: found.MimeType,
		SizeBytes: size, StorageKey: key, UploadedBy: whatsappMediaUploader,
	}); err != nil {
		return fmt.Errorf("record the attachment: %w", err)
	}
	if err := w.server.store.Q.SetWhatsAppMessageMedia(ctx, gen.SetWhatsAppMessageMediaParams{
		FileAssetID: &fileID, MediaMimeType: found.MimeType, WaMessageID: job.Args.MessageID,
	}); err != nil {
		return fmt.Errorf("attach the file to the message: %w", err)
	}

	// The customer's number is masked: this line is about a file, and the
	// message id finds the conversation.
	w.logger.Info("whatsapp attachment stored",
		"brand", msg.BrandSlug, "wa", phone.Mask(msg.WaID),
		"kind", msg.Kind, "bytes", size)
	return nil
}

// spoolAndStore writes the body to a temp file and uploads it.
//
// A FILE, NOT MEMORY. The length is chosen by whoever sent the message, and
// holding several of them at once is how a worker with a modest heap dies on a
// busy afternoon. The client has already refused anything over its own limit.
func (w *WhatsAppMediaWorker) spoolAndStore(
	ctx context.Context, body io.Reader, key, contentType string,
) (int64, error) {
	tmp, err := os.CreateTemp("", "tensor-whatsapp-*")
	if err != nil {
		return 0, fmt.Errorf("buffer the attachment: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	size, err := io.Copy(tmp, body)
	if err != nil {
		_ = tmp.Close()
		return 0, fmt.Errorf("buffer the attachment: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("buffer the attachment: %w", err)
	}
	if size == 0 {
		return 0, errors.New("that attachment arrived empty")
	}
	if err := w.server.storage.Upload(ctx, key, tmpPath, contentType); err != nil {
		return 0, fmt.Errorf("store the attachment: %w", err)
	}
	return size, nil
}

// failMedia records that an attachment will never arrive.
//
// Written down rather than left pending, because the two are different answers
// to "did we get their photographs?" and a screen that cannot tell them apart
// says "still downloading" for ever.
func (w *WhatsAppMediaWorker) failMedia(ctx context.Context, messageID string, cause error) {
	w.logger.Error("whatsapp media: giving up on an attachment",
		"message", messageID, "error", cause)
	if err := w.server.store.Q.FailWhatsAppMessageMedia(ctx, gen.FailWhatsAppMessageMediaParams{
		MediaError: cause.Error(), WaMessageID: messageID,
	}); err != nil {
		w.logger.Error("whatsapp media: could not record the failure",
			"message", messageID, "error", err)
	}
}

// attachmentName is the stored filename. The customer's own is deliberately
// not used: it is attacker-chosen text that would end up in a storage key and
// a Content-Disposition header.
func attachmentName(id uuid.UUID, contentType, kind string) string {
	ext := ""
	if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
		ext = exts[0]
	}
	if ext == "" {
		ext = "." + strings.ToLower(kind)
	}
	return id.String() + ext
}

// --- serving ----------------------------------------------------------------

// whatsappMessageMedia hands one attachment to the dashboard.
//
// NOT /files/:id, which already serves file_assets. That route is a flat,
// global id space with no brand in it; this one is scoped by the conversation
// the attachment arrived on, so a file id from one brand's chat cannot be
// fetched under another brand's slug. It is also guarded on whatsapp:read
// rather than production:read, because a customer's photographs are not
// production data - PACKAGING_QC holds the latter and has no business with the
// former.
func (s *Server) whatsappMessageMedia(c *gin.Context) {
	ctx := c.Request.Context()
	brand := c.Param("slug")
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if s.storage == nil {
		detail(c, http.StatusServiceUnavailable, "File storage is not configured.")
		return
	}

	row, err := s.store.Q.GetWhatsAppMessageMedia(ctx, gen.GetWhatsAppMessageMediaParams{
		ID: id, BrandSlug: brand,
	})
	if err != nil {
		dbError(c, err, "That message does not exist.", "Could not load the message.")
		return
	}
	// Said precisely, because "not yet" and "never" are different answers and
	// the page draws them differently.
	switch row.MediaStatus {
	case mediaPending:
		detail(c, http.StatusNotFound, "That attachment has not been downloaded yet.")
		return
	case mediaFailed:
		detail(c, http.StatusNotFound, "That attachment could not be downloaded from WhatsApp.")
		return
	case mediaStored:
	default:
		detail(c, http.StatusNotFound, "That message has no attachment.")
		return
	}
	if row.StorageKey == nil || row.ContentType == nil {
		detail(c, http.StatusNotFound, "That attachment is no longer in storage.")
		return
	}

	obj, err := s.storage.Get(ctx, *row.StorageKey)
	if err != nil {
		if storage.IsNotFound(err) {
			detail(c, http.StatusNotFound, "That attachment is no longer in storage.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not read the attachment.")
		return
	}
	defer func() { _ = obj.Body.Close() }()

	// Inline, because the point is to look at it in the page rather than to
	// save it. Cached hard and privately: the bytes never change once stored,
	// and they are one customer's photographs.
	c.Header("Content-Disposition", "inline")
	c.Header("Cache-Control", "private, max-age=86400")
	c.DataFromReader(http.StatusOK, obj.Size, *row.ContentType, obj.Body, nil)
}

func (s *Server) registerWhatsAppMedia(r *gin.Engine) {
	g := r.Group("/brands/:slug/whatsapp/messages")
	g.Use(s.guards.RequireUser())
	g.GET("/:id/media", s.guards.RequirePermission(auth.WhatsAppRead.Key()), s.whatsappMessageMedia)
}
