package httpapi

// Font files for design templates.
//
// OpenSCAD renders a missing font in a substitute and reports success, so a
// template naming a face the worker does not have prints the wrong lettering
// (JOB-116894-23, the Duo Snap). Faces used to arrive only by commit and
// redeploy. Now one is uploaded next to the .scad that needs it.
//
// THE FONT IS OPTIONAL. Only a NEW .scad naming a family that is neither
// built in nor uploaded is refused, and the refusal names the family and the
// file to upload. Templates already stored are never re-checked or changed,
// and a render stages an uploaded face only for a family its own file names
// and no worker ships - so everything that renders today renders the same.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"bytes"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// maxFontBytes bounds an upload. Text faces are tens to hundreds of KB; a
// large CJK face is a few MB.
const maxFontBytes = 20 << 20

// fontFiles caches uploaded faces by storage key. A stored face never changes
// (the key is its content hash), so a cached copy can never be stale.
var fontFiles = &templateSourceCache{entries: map[string][]byte{}}

type fontResponse struct {
	ID         string    `json:"id"`
	Families   []string  `json:"families"`
	Style      string    `json:"style"`
	Filename   string    `json:"filename"`
	SizeBytes  int64     `json:"size_bytes"`
	UploadedBy string    `json:"uploaded_by"`
	CreatedAt  time.Time `json:"created_at"`
}

type fontListResponse struct {
	// BuiltIn are the families every worker already has: a template naming
	// one needs no upload.
	BuiltIn  []string       `json:"built_in"`
	Uploaded []fontResponse `json:"uploaded"`
}

func (s *Server) registerDesignFonts(r *gin.Engine) {
	g := r.Group("/registry/fonts")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.ConfigRead.Key()), s.listDesignFonts)
	g.POST("", s.guards.RequirePermission(auth.RegistryManage.Key()), s.uploadDesignFont)
}

func toFontResponse(f gen.DesignFont) fontResponse {
	return fontResponse{
		ID: f.ID.String(), Families: f.Families, Style: f.Style, Filename: f.Filename,
		SizeBytes: f.SizeBytes, UploadedBy: f.UploadedBy, CreatedAt: db.Time(f.CreatedAt),
	}
}

func (s *Server) listDesignFonts(c *gin.Context) {
	rows, err := s.store.Q.ListDesignFonts(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list the fonts.")
		return
	}
	out := fontListResponse{BuiltIn: personalise.BuiltinFontFamilies(), Uploaded: make([]fontResponse, 0, len(rows))}
	for _, r := range rows {
		out.Uploaded = append(out.Uploaded, toFontResponse(r))
	}
	c.JSON(http.StatusOK, out)
}

// uploadDesignFont stores one .ttf/.otf. 201 for a new face, 200 when the
// same file is already here.
func (s *Server) uploadDesignFont(c *gin.Context) {
	ctx := c.Request.Context()
	if !s.filesReady(c) {
		return
	}
	data, filename, ok := readFontUpload(c)
	if !ok {
		return
	}
	face, err := personalise.ReadFontFace(data)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "That file is not a font Tensor can use. "+
			"Upload the desktop font file - a .ttf (TrueType) or .otf (OpenType) - not a web font "+
			"(.woff, .woff2), a collection (.ttc) or a .zip.")
		return
	}
	// Never shadow a face the workers ship: an upload that changed how an
	// existing design renders is exactly what this must not do.
	for _, fam := range face.Families {
		if personalise.BuiltinFontFamily(fam) {
			detail(c, http.StatusConflict, fmt.Sprintf(
				"Tensor already has the %q font family, so there is nothing to upload. "+
					"Templates naming it render with it already.", fam))
			return
		}
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	if existing, err := s.store.Q.GetDesignFontBySHA(ctx, hash); err == nil {
		c.JSON(http.StatusOK, toFontResponse(existing))
		return
	} else if !isNoRows(err) {
		detail(c, http.StatusInternalServerError, "Could not check the font.")
		return
	}

	ext := strings.ToLower(filepath.Ext(filename))
	key := fmt.Sprintf("fonts/%s%s", hash, ext)
	if err := s.storage.Put(ctx, key, bytes.NewReader(data), int64(len(data)), fontContentType(ext)); err != nil {
		obs.FromContext(ctx).Error("could not store a font", "file", filename, "error", err)
		detail(c, http.StatusBadGateway, "Could not store the font file.")
		return
	}
	families := make([]string, len(face.Families))
	for i, f := range face.Families {
		families[i] = strings.ToLower(strings.Join(strings.Fields(f), " "))
	}
	row, err := s.store.Q.InsertDesignFont(ctx, gen.InsertDesignFontParams{
		ID: uuid.New(), Families: families, Style: face.Style, Filename: filename,
		StorageKey: key, Sha256: hash, SizeBytes: int64(len(data)), UploadedBy: currentUserID(c),
	})
	if err != nil {
		obs.FromContext(ctx).Error("could not record a font", "file", filename, "error", err)
		detail(c, http.StatusInternalServerError, "Could not record the font.")
		return
	}
	obs.FromContext(ctx).Info("design font uploaded", "families", face.Families, "style", face.Style, "bytes", len(data))
	c.JSON(http.StatusCreated, toFontResponse(row))
}

// readFontUpload reads the multipart "file", refusing a wrong type or size.
func readFontUpload(c *gin.Context) ([]byte, string, bool) {
	header, err := c.FormFile("file")
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "Attach the .ttf or .otf font file to upload.")
		return nil, "", false
	}
	if ext := strings.ToLower(filepath.Ext(header.Filename)); ext != ".ttf" && ext != ".otf" {
		detail(c, http.StatusUnprocessableEntity,
			"A font is uploaded as its .ttf (TrueType) or .otf (OpenType) file.")
		return nil, "", false
	}
	if header.Size > maxFontBytes {
		detail(c, http.StatusUnprocessableEntity, "That font file is larger than 20 MB.")
		return nil, "", false
	}
	src, err := header.Open()
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the uploaded file.")
		return nil, "", false
	}
	defer func() { _ = src.Close() }()
	data, err := io.ReadAll(io.LimitReader(src, maxFontBytes+1))
	if err != nil || len(data) > maxFontBytes {
		detail(c, http.StatusInternalServerError, "Could not read the uploaded file.")
		return nil, "", false
	}
	return data, filepath.Base(header.Filename), true
}

func fontContentType(ext string) string {
	if ext == ".otf" {
		return "font/otf"
	}
	return "font/ttf"
}

// missingTemplateFonts is the families a template names that no worker ships
// and nobody has uploaded - the ones its render would substitute.
func (s *Server) missingTemplateFonts(ctx context.Context, source []byte) ([]string, error) {
	var need []string
	for _, f := range personalise.TemplateFontFamilies(source) {
		if !personalise.BuiltinFontFamily(f) {
			need = append(need, f)
		}
	}
	if len(need) == 0 {
		return nil, nil
	}
	rows, err := s.store.Q.DesignFontsForFamilies(ctx, lowerAll(need))
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, r := range rows {
		for _, f := range r.Families {
			have[f] = true
		}
	}
	var missing []string
	for _, f := range need {
		if !have[strings.ToLower(strings.Join(strings.Fields(f), " "))] {
			missing = append(missing, f)
		}
	}
	return missing, nil
}

// missingFontsDetail is the refusal for a .scad whose font is not here.
func missingFontsDetail(missing []string) string {
	quoted := make([]string, len(missing))
	for i, f := range missing {
		quoted[i] = fmt.Sprintf("%q", f)
	}
	sort.Strings(quoted)
	noun := "font family"
	if len(missing) > 1 {
		noun = "font families"
	}
	return fmt.Sprintf("This file uses the %s %s, which Tensor does not have, so it would render in a "+
		"different font. Add the font file first - the .ttf or .otf whose family name is %s, "+
		"one file per style the design uses - then add this .scad again.",
		noun, strings.Join(quoted, ", "), strings.Join(quoted, " / "))
}

func lowerAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(strings.Join(strings.Fields(s), " "))
	}
	return out
}

// fontLoader reads the uploaded faces for the renderer
// (personalise.FontLoader). The file names are the stored keys' base names,
// which are content hashes and so never collide.
func (s *Server) fontLoader() personalise.FontLoader {
	return func(ctx context.Context, families []string) (map[string][]byte, error) {
		rows, err := s.store.Q.DesignFontsForFamilies(ctx, lowerAll(families))
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, nil
		}
		if s.storage == nil {
			return nil, fmt.Errorf("object storage is not configured")
		}
		out := make(map[string][]byte, len(rows))
		for _, r := range rows {
			data, ok := fontFiles.get(r.StorageKey)
			if !ok {
				obj, err := s.storage.Get(ctx, r.StorageKey)
				if err != nil {
					return nil, fmt.Errorf("fetch font %s: %w", r.Filename, err)
				}
				data, err = io.ReadAll(io.LimitReader(obj.Body, maxFontBytes+1))
				_ = obj.Body.Close()
				if err != nil {
					return nil, fmt.Errorf("read font %s: %w", r.Filename, err)
				}
				fontFiles.put(r.StorageKey, data)
			}
			out[filepath.Base(r.StorageKey)] = data
		}
		return out, nil
	}
}
