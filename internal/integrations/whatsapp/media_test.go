package whatsapp

// Fetching an attachment, and the two ways it is usually got wrong.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mediaServer answers both hops. Each records what it was asked, so a test can
// assert on the headers rather than only on the bytes.
type mediaServer struct {
	srv      *httptest.Server
	bytes    []byte
	size     int64
	bodyCode int

	metaAuth string
	bodyAuth string
	bodyUA   string
	hops     int
}

func newMediaServer(t *testing.T, payload []byte) *mediaServer {
	t.Helper()
	m := &mediaServer{bytes: payload, size: int64(len(payload)), bodyCode: http.StatusOK}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.hops++
		if strings.HasSuffix(r.URL.Path, "/blob") {
			m.bodyAuth = r.Header.Get("Authorization")
			m.bodyUA = r.Header.Get("User-Agent")
			if m.bodyCode != http.StatusOK {
				w.WriteHeader(m.bodyCode)
				_, _ = w.Write([]byte(`{"error":{"message":"gone","code":404}}`))
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(m.bytes)
			return
		}
		m.metaAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"url":       m.srv.URL + "/blob",
			"mime_type": "image/png",
			"sha256":    "deadbeef",
			"file_size": m.size,
		})
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mediaServer) client() *Client {
	return New(Config{
		AccessToken: "tok", PhoneNumberID: "1", WABAID: "9",
		TemplateName: "t", TemplateLanguage: "en", BaseURL: m.srv.URL,
	})
}

// Both hops need the token, and the second needs a User-Agent as well. Meta's
// CDN answers 400 to a request without one, which reads like a bad media id.
func TestBothHopsAreAuthenticatedAndTheBodyIsNamed(t *testing.T) {
	srv := newMediaServer(t, []byte("PNGPNGPNG"))

	body, found, err := srv.client().DownloadMedia(context.Background(), "MEDIA1")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = body.Close() }()

	got, _ := io.ReadAll(body)
	if string(got) != "PNGPNGPNG" {
		t.Errorf("bytes = %q", got)
	}
	if found.MimeType != "image/png" || found.SHA256 != "deadbeef" || found.SizeBytes != 9 {
		t.Errorf("media = %+v", found)
	}
	if srv.metaAuth != "Bearer tok" {
		t.Errorf("first hop sent %q", srv.metaAuth)
	}
	if srv.bodyAuth != "Bearer tok" {
		t.Errorf("second hop sent %q; the url 401s without the token", srv.bodyAuth)
	}
	if srv.bodyUA == "" {
		t.Error("second hop sent no User-Agent; Meta's CDN refuses that")
	}
}

// The size is checked against what Meta reports, BEFORE the body is asked for.
// Otherwise the guard costs exactly the download it exists to prevent.
func TestAnOversizedAttachmentIsRefusedBeforeItIsFetched(t *testing.T) {
	srv := newMediaServer(t, []byte("x"))
	srv.size = MediaMaxBytes + 1

	_, _, err := srv.client().DownloadMedia(context.Background(), "MEDIA1")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "over the") {
		t.Errorf("error = %v", err)
	}
	if srv.hops != 1 {
		t.Errorf("made %d hops; the body must not be requested at all", srv.hops)
	}
}

// The five-minute window closing between the hops is a 404, and it has to read
// as "ask again", not as a missing attachment.
func TestAnExpiredURLSaysToFetchItAgain(t *testing.T) {
	srv := newMediaServer(t, []byte("x"))
	srv.bodyCode = http.StatusNotFound

	_, _, err := srv.client().DownloadMedia(context.Background(), "MEDIA1")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error = %v, want it to name the expiry", err)
	}
}

// A 2xx with no url is not an answer; returning it would store an empty file.
func TestNoURLIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"mime_type":"image/png"}`)
	}))
	defer srv.Close()

	c := New(Config{AccessToken: "t", PhoneNumberID: "1", WABAID: "9",
		TemplateName: "t", TemplateLanguage: "en", BaseURL: srv.URL})
	if _, _, err := c.DownloadMedia(context.Background(), "MEDIA1"); err == nil {
		t.Fatal("expected an error for a response carrying no url")
	}
}
