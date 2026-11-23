package httpapi

// Uploading a font, and the .scad check it exists for.
//
// The upload routes need real object storage; the other integration tests run
// without any. This one uses a MinIO named by TENSOR_TEST_S3 (host:port,
// access key, secret key, comma separated) and a bucket of its own, and skips
// when it is not set - never the live bucket.

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

func testObjectStore(t *testing.T) *storage.Client {
	t.Helper()
	spec := os.Getenv("TENSOR_TEST_S3")
	if spec == "" {
		t.Skip("TENSOR_TEST_S3 not set (host:port,access,secret of a scratch MinIO)")
	}
	parts := strings.Split(spec, ",")
	if len(parts) != 3 {
		t.Fatalf("TENSOR_TEST_S3 = %q, want host:port,access,secret", spec)
	}
	c, err := storage.New(context.Background(), storage.Options{
		Endpoint: parts[0], AccessKey: parts[1], SecretKey: parts[2], Bucket: "tensor-test",
	})
	if err != nil {
		t.Fatalf("test object store: %v", err)
	}
	return c
}

// renamedFont is Lobster with its family renamed in place to a same-length
// name no worker ships, so the test has a face that genuinely needs an upload.
func renamedFont(t *testing.T, to string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../scadassets/fonts/Lobster-Regular.ttf")
	if err != nil {
		t.Fatal(err)
	}
	if len(to) != len("Lobster") {
		t.Fatal("the new name must be as long as the old")
	}
	utf16be := func(s string) []byte {
		var b []byte
		for _, r := range utf16.Encode([]rune(s)) {
			b = append(b, byte(r>>8), byte(r))
		}
		return b
	}
	data = bytes.ReplaceAll(data, utf16be("Lobster"), utf16be(to))
	return bytes.ReplaceAll(data, []byte("Lobster"), []byte(to))
}

func multipartFile(t *testing.T, path, token, filename string, data []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(data)
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestIntegrationAFontIsOnlyAskedForWhenTheScadNeedsOne(t *testing.T) {
	objects := testObjectStore(t)
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE design_fonts; TRUNCATE design_templates CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	s := NewServer(config.Settings{Environment: "development", AuthAudience: "tensor-core"},
		store, auth.NewGuards(minter.verifier, ""), nil)
	s.EnablePipeline(objects, nil)
	router := s.Router()
	token := minter.mint(t, []string{auth.ConfigRead.Key(), auth.RegistryManage.Key()})
	send := func(path, filename string, data []byte) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, multipartFile(t, path, token, filename, data))
		return rr
	}

	// A .scad on built-in fonts needs nothing uploaded: the font is optional.
	if rr := send("/registry/templates/plain", "plain.scad",
		[]byte("PART = \"\";\ntext(\"A\", font = \"Segoe UI Black\");\n")); rr.Code != http.StatusCreated {
		t.Fatalf("built-in font = %d %s, want 201", rr.Code, rr.Body.String())
	}

	// One naming a face nobody has is refused, naming the family and the file.
	scad := []byte("PART = \"\";\nf = \"Gothamx\";\ntext(\"A\", font = f);\n")
	rr := send("/registry/templates/gotham", "gotham.scad", scad)
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `\"Gothamx\"`) ||
		!strings.Contains(rr.Body.String(), ".ttf or .otf") {
		t.Fatalf("missing font = %d %s, want 422 naming Gothamx", rr.Code, rr.Body.String())
	}

	// A face every worker ships cannot be uploaded over: it would change
	// designs that render with it today.
	ond, _ := os.ReadFile("../../scadassets/fonts/Ondfuturs-Demo.ttf")
	if rr := send("/registry/fonts", "Ondfuturs-Demo.ttf", ond); rr.Code != http.StatusConflict {
		t.Fatalf("built-in face = %d %s, want 409", rr.Code, rr.Body.String())
	}
	if rr := send("/registry/fonts", "x.woff2", []byte("wOF2")); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("web font = %d, want 422", rr.Code)
	}
	if rr := send("/registry/fonts", "x.ttf", []byte("not a font")); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("not a font = %d, want 422", rr.Code)
	}

	face := renamedFont(t, "Gothamx")
	rr = send("/registry/fonts", "Gothamx-Regular.ttf", face)
	var first fontResponse
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &first) != nil ||
		len(first.Families) == 0 || first.Families[0] != "gothamx" {
		t.Fatalf("upload = %d %s", rr.Code, rr.Body.String())
	}
	// The same file again is the same font, not a second row.
	rr = send("/registry/fonts", "copy.ttf", face)
	var again fontResponse
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &again) != nil || again.ID != first.ID {
		t.Fatalf("re-upload = %d %s, want 200 with id %s", rr.Code, rr.Body.String(), first.ID)
	}

	// Now the .scad goes in.
	if rr := send("/registry/templates/gotham", "gotham.scad", scad); rr.Code != http.StatusCreated {
		t.Fatalf("after the font = %d %s, want 201", rr.Code, rr.Body.String())
	}

	// The renderer is handed exactly that face, and nothing for built-ins.
	files, err := s.fontLoader()(ctx, []string{"Gothamx"})
	if err != nil || len(files) != 1 {
		t.Fatalf("loader = %d files, %v", len(files), err)
	}
	for _, b := range files {
		if !bytes.Equal(b, face) {
			t.Fatal("the staged face is not the uploaded file")
		}
	}
	if files, _ := s.fontLoader()(ctx, []string{"Segoe UI Black"}); len(files) != 0 {
		t.Fatalf("a built-in family loaded %d uploads", len(files))
	}

	var list fontListResponse
	rr = doJSON(router, http.MethodGet, "/registry/fonts", token, nil)
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &list) != nil ||
		len(list.Uploaded) != 1 || !strings.Contains(strings.Join(list.BuiltIn, ","), "ondfuturs") {
		t.Fatalf("list = %d %s", rr.Code, rr.Body.String())
	}
}
