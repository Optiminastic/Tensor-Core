package httpapi

// The damage form - the second part of Tensor a stranger can reach, and the
// first that accepts a file from one.
//
// Everything said in personalisation_public.go about assuming a hostile
// caller applies here unchanged, and one thing more: this group WRITES, and
// what it writes is bytes of the caller's choosing into object storage. The
// defence is entirely in the caps and the uniform refusals below, because
// there is nobody to authenticate.
//
//   - The token is the only authority. Which brand, which customer, and which
//     files already exist all come off the link row, never off the request.
//   - Every refusal is the same refusal, so probing learns nothing.
//   - One file per request, bounded, and a bounded number of them per link.

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

const (
	// maxDamageFileBytes bounds ONE upload.
	//
	// A phone video of a broken parcel is a few tens of megabytes; 25 MB
	// covers a short clip and every photograph, and keeps a single request
	// from being a denial-of-service on an unauthenticated route.
	maxDamageFileBytes = 25 << 20

	// maxDamageFiles bounds one link. Eight is more than anybody needs to
	// show a damaged plank from every angle, and it is what stops a token
	// becoming free file hosting.
	maxDamageFiles = 8

	// damageUploadDeadline is how long ONE upload may take.
	//
	// THIS IS THE WHOLE REASON FILES GO UP ONE AT A TIME. The server's
	// WriteTimeout is 60 seconds and Go starts that clock when the request
	// headers arrive, so it covers the body: a 25 MB video from a phone on a
	// slow connection is killed mid-upload, with nothing on either side
	// saying why. Extended per request rather than globally, so the rest of
	// the API keeps its short deadline.
	damageUploadDeadline = 10 * time.Minute

	// damageUploader is what file_assets records as the uploader. A word,
	// where that column otherwise holds a Better Auth user id - the same
	// convention the WhatsApp media worker uses.
	damageUploader = "customer"
)

// damageFileExt is what the form accepts, and what each one actually IS.
//
// IMAGES AND VIDEO ONLY: a customer photographing a parcel sends one of
// these, and anything else on an unauthenticated upload route is somebody
// else's idea.
//
// THE EXTENSION IS THE GATE, AND THE TYPE HERE IS THE ANSWER. The browser's
// own Content-Type is not trustworthy enough to refuse on. Chrome on Windows
// builds it from the registry, and a machine with no registry entry for .heic
// - which is most of them - sends "application/octet-stream" for an ordinary
// iPhone photograph. Refusing that told a customer "please send a photo or a
// video" about a photo, which is the least actionable sentence this form can
// produce. Verified on 2026-10-26: the same .heic is refused as
// octet-stream and accepted as image/heic.
//
// So the declared type is only ever used when it is already an image or a
// video; otherwise the extension's own type is stored. That is also the safer
// direction - a .jpg declaring itself text/html gets stored as image/jpeg
// rather than served back as markup.
var damageFileExt = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
	".webp": "image/webp", ".heic": "image/heic", ".heif": "image/heif",
	".gif": "image/gif",
	".mp4": "video/mp4", ".mov": "video/quicktime", ".m4v": "video/x-m4v",
	".3gp": "video/3gpp", ".webm": "video/webm", ".avi": "video/x-msvideo",
	".mkv": "video/x-matroska",
}

func (s *Server) registerDamagePublic(r *gin.Engine) {
	// NO RequireUser, like the personalisation group - see the file comment.
	g := r.Group("/public/damage/:token")
	g.Use(s.publicRateLimit())

	g.GET("", s.publicDamageForm)
	g.POST("/files", s.publicDamageUpload)
	g.POST("", s.publicDamageSubmit)
}

type publicDamageFileJSON struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes"`
}

type publicDamageResponse struct {
	// OrderNumber is what they typed last time, so reopening the link shows
	// the report they already filed rather than an empty form.
	OrderNumber string                 `json:"order_number"`
	Submitted   bool                   `json:"submitted"`
	ExpiresAt   string                 `json:"expires_at"`
	Files       []publicDamageFileJSON `json:"files"`
	MaxFiles    int                    `json:"max_files"`
	MaxFileMB   int                    `json:"max_file_mb"`
}

// publicDamageForm says whether the link is live and what it already holds.
func (s *Server) publicDamageForm(c *gin.Context) {
	ctx := c.Request.Context()
	link, ok := s.openDamageLink(c)
	if !ok {
		return
	}
	if err := s.store.Q.MarkDamageLinkOpened(ctx, link.ID); err != nil {
		obs.FromContext(ctx).Warn("could not mark a damage link opened", "error", err)
	}

	out := publicDamageResponse{
		Submitted: link.SubmittedAt.Valid,
		ExpiresAt: link.ExpiresAt.Time.UTC().Format(time.RFC3339),
		Files:     s.damageLinkFiles(c, link.ID),
		MaxFiles:  maxDamageFiles, MaxFileMB: maxDamageFileBytes >> 20,
	}
	// The order number comes from the report, not the link: the link is
	// minted before anybody knows which order it is about.
	if report, err := s.store.Q.GetDamageReportByLink(ctx, &link.ID); err == nil {
		out.OrderNumber = report.OrderNumber
	}
	c.JSON(http.StatusOK, out)
}

// publicDamageUpload takes ONE file.
//
// Not a multipart of eight. Each file gets its own deadline, its own retry
// and its own line of progress in the page - and a connection that drops
// after the fourth photograph has still delivered four.
func (s *Server) publicDamageUpload(c *gin.Context) {
	ctx := c.Request.Context()
	link, ok := s.openDamageLink(c)
	if !ok {
		return
	}

	// Pushed out before a byte is read. See damageUploadDeadline: without
	// this the server's own 60-second write deadline cuts off any upload
	// slower than about 3 Mbit, which is most phones.
	if err := http.NewResponseController(c.Writer).
		SetWriteDeadline(time.Now().Add(damageUploadDeadline)); err != nil {
		obs.FromContext(ctx).Warn("could not extend the upload deadline", "error", err)
	}

	count, err := s.store.Q.CountDamageFiles(ctx, link.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read what you have already sent.")
		return
	}
	if count >= maxDamageFiles {
		detail(c, http.StatusUnprocessableEntity, fmt.Sprintf(
			"You can send up to %d files. Remove one before adding another, "+
				"or send the rest in a reply on WhatsApp.", maxDamageFiles))
		return
	}

	// MaxBytesReader BEFORE FormFile, so an oversized body is cut off as it
	// arrives rather than after it has all been spooled to disk.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxDamageFileBytes+(1<<20))
	header, err := c.FormFile("file")
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			detail(c, http.StatusRequestEntityTooLarge, damageTooLargeMessage())
			return
		}
		detail(c, http.StatusUnprocessableEntity, "Choose a photo or a video to send.")
		return
	}
	if header.Size <= 0 {
		detail(c, http.StatusUnprocessableEntity, "That file is empty.")
		return
	}
	if header.Size > maxDamageFileBytes {
		detail(c, http.StatusRequestEntityTooLarge, damageTooLargeMessage())
		return
	}
	ext := strings.ToLower(path.Ext(header.Filename))
	known, ok := damageFileExt[ext]
	if !ok {
		detail(c, http.StatusUnprocessableEntity,
			"Please send a photo or a video of the damage.")
		return
	}
	// The browser's word is taken only when it says image or video. See
	// damageFileExt: an octet-stream here is an ordinary iPhone photograph on
	// a Windows machine, not an attack.
	contentType := known
	if declared := strings.ToLower(strings.TrimSpace(
		header.Header.Get("Content-Type"))); isDamageMedia(declared) {
		contentType = declared
	}

	// ASKED AFTER THE FILE IS CHECKED, not before. A misconfigured process
	// is not the customer's mistake to be told about first: when they have
	// sent a PDF, "please send a photo or a video" is the useful sentence,
	// and it is the one the checks above can give.
	if s.storage == nil {
		detail(c, http.StatusServiceUnavailable,
			"Uploads are not available just now. Please try again shortly.")
		return
	}

	fileID := uuid.New()
	// The customer's own filename is never used: it is attacker-chosen text
	// that would end up in a storage key and a Content-Disposition header.
	name := fileID.String() + ext
	key := fmt.Sprintf("damage/%s/%s", link.BrandSlug, name)

	size, err := s.spoolDamageFile(c, header, key, contentType)
	if err != nil {
		obs.FromContext(ctx).Error("damage upload failed", "brand", link.BrandSlug, "error", err)
		detail(c, http.StatusInternalServerError, "That file did not reach us. Please try again.")
		return
	}

	if _, err := s.store.Q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
		ID: fileID, Filename: name, ContentType: contentType,
		SizeBytes: size, StorageKey: key, UploadedBy: damageUploader,
	}); err != nil {
		detail(c, http.StatusInternalServerError, "That file did not reach us. Please try again.")
		return
	}
	if err := s.store.Q.AttachDamageFile(ctx, gen.AttachDamageFileParams{
		LinkID: link.ID, FileID: fileID,
	}); err != nil {
		detail(c, http.StatusInternalServerError, "That file did not reach us. Please try again.")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"file":  publicDamageFileJSON{ID: fileID.String(), Kind: damageKind(contentType), SizeBytes: size},
		"files": s.damageLinkFiles(c, link.ID),
	})
}

// publicDamageSubmit files the report.
//
// A SECOND SUBMIT UPDATES THE FIRST. The link stays open for its full 24
// hours, so somebody who finds another photograph an hour later adds it to
// the report they already filed - which is what the shop asked for, and what
// stops the queue filling with two halves of one problem.
func (s *Server) publicDamageSubmit(c *gin.Context) {
	ctx := c.Request.Context()
	link, ok := s.openDamageLink(c)
	if !ok {
		return
	}

	var body struct {
		OrderID string `json:"order_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		detail(c, http.StatusUnprocessableEntity, "Please enter your order ID.")
		return
	}
	number := strings.TrimSpace(body.OrderID)
	if number == "" {
		detail(c, http.StatusUnprocessableEntity, "Please enter your order ID.")
		return
	}
	if len(number) > 64 {
		detail(c, http.StatusUnprocessableEntity, "That does not look like an order ID.")
		return
	}

	count, err := s.store.Q.CountDamageFiles(ctx, link.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not send that just now.")
		return
	}
	if count == 0 {
		// The photographs are the report. One without them is a line in a
		// queue nobody can act on.
		detail(c, http.StatusUnprocessableEntity,
			"Please add at least one photo or video of the damage.")
		return
	}

	params := gen.UpsertDamageReportForLinkParams{
		BrandSlug: link.BrandSlug, WaID: link.WaID, OrderNumber: number,
		PhotoCount: count, LinkID: &link.ID,
	}
	// Best effort: the customer types the number, so a report can name an
	// order Tensor has never imported - or one they mistyped. It is filed
	// either way; order_id simply stays null and the page shows the number
	// alone.
	if found, err := s.store.Q.FindOrderByNumber(ctx, gen.FindOrderByNumberParams{
		Typed: number, Digits: typedOrderNumber(number),
	}); err == nil {
		id := found.ID
		params.OrderID = &id
		params.CustomerName = deref(found.CustomerName)
	}
	if _, err := s.store.Q.UpsertDamageReportForLink(ctx, params); err != nil {
		obs.FromContext(ctx).Error("could not file a damage report",
			"brand", link.BrandSlug, "error", err)
		detail(c, http.StatusInternalServerError, "Could not send that just now.")
		return
	}
	if err := s.store.Q.SubmitDamageLink(ctx, link.ID); err != nil {
		obs.FromContext(ctx).Warn("could not mark a damage link submitted", "error", err)
	}

	c.JSON(http.StatusOK, gin.H{"submitted": true})
}

// --- helpers -----------------------------------------------------------------

// openDamageLink resolves the token, or refuses.
//
// ONE REFUSAL FOR EVERY CAUSE, as openLink does: expired, closed, never
// existed, malformed - the same status and the same sentence, so probing
// learns nothing.
func (s *Server) openDamageLink(c *gin.Context) (gen.DamageLink, bool) {
	token := strings.TrimSpace(c.Param("token"))
	if token == "" {
		refusePublicLink(c)
		return gen.DamageLink{}, false
	}
	link, err := s.store.Q.GetDamageLinkByTokenHash(
		c.Request.Context(), hashPersonalisationToken(token))
	if err != nil {
		refusePublicLink(c)
		return gen.DamageLink{}, false
	}
	// Judged here rather than in SQL, so that an expired link and an unknown
	// one come back through exactly the same branch.
	if !link.ExpiresAt.Valid || !time.Now().UTC().Before(link.ExpiresAt.Time) {
		refusePublicLink(c)
		return gen.DamageLink{}, false
	}
	if link.Status == "expired" || link.Status == "failed" {
		refusePublicLink(c)
		return gen.DamageLink{}, false
	}
	return link, true
}

func (s *Server) damageLinkFiles(c *gin.Context, linkID uuid.UUID) []publicDamageFileJSON {
	rows, err := s.store.Q.ListDamageLinkFiles(c.Request.Context(), linkID)
	if err != nil {
		return []publicDamageFileJSON{}
	}
	out := make([]publicDamageFileJSON, 0, len(rows))
	for _, r := range rows {
		out = append(out, publicDamageFileJSON{
			ID: r.ID.String(), Kind: damageKind(r.ContentType), SizeBytes: r.SizeBytes,
		})
	}
	return out
}

// spoolDamageFile writes the part to a temp file and uploads it.
//
// A FILE, NOT MEMORY, for the reason the WhatsApp media worker spools too:
// the length is chosen by whoever is uploading, and holding several at once
// is how a process with a modest heap dies on a busy afternoon.
func (s *Server) spoolDamageFile(
	c *gin.Context, header *multipart.FileHeader, key, contentType string,
) (int64, error) {
	src, err := header.Open()
	if err != nil {
		return 0, fmt.Errorf("read the upload: %w", err)
	}
	defer func() { _ = src.Close() }()

	tmp, err := os.CreateTemp("", "tensor-damage-*")
	if err != nil {
		return 0, fmt.Errorf("buffer the upload: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	size, err := io.Copy(tmp, io.LimitReader(src, maxDamageFileBytes))
	if err != nil {
		_ = tmp.Close()
		return 0, fmt.Errorf("buffer the upload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("buffer the upload: %w", err)
	}
	if size == 0 {
		return 0, errors.New("that file arrived empty")
	}
	if err := s.storage.Upload(c.Request.Context(), key, tmpPath, contentType); err != nil {
		return 0, fmt.Errorf("store the upload: %w", err)
	}
	return size, nil
}

func isDamageMedia(contentType string) bool {
	base, _, _ := strings.Cut(contentType, ";")
	base = strings.TrimSpace(base)
	return strings.HasPrefix(base, "image/") || strings.HasPrefix(base, "video/")
}

func damageKind(contentType string) string {
	if strings.HasPrefix(contentType, "video/") {
		return "video"
	}
	return "image"
}

func damageTooLargeMessage() string {
	return fmt.Sprintf(
		"That file is over %d MB. Send a shorter video, or a photo instead.",
		maxDamageFileBytes>>20)
}

// typedOrderNumber is the digits out of whatever the customer typed.
//
// The LONGEST run of them, which is the same rule the bot used on a typed
// order number and for the same reason: the "3" in T3DPS must not win. Empty
// when they typed no digits at all, which the query reads as "match on the
// exact text only".
var digitRunPattern = regexp.MustCompile(`\d+`)

func typedOrderNumber(typed string) string {
	best := ""
	for _, run := range digitRunPattern.FindAllString(typed, -1) {
		if len(run) > len(best) {
			best = run
		}
	}
	return best
}
