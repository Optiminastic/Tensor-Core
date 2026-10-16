package whatsapp

// Getting the bytes of something a customer sent.
//
// A webhook never carries an attachment - it carries an id, and the bytes are
// two further calls away. Both are needed and the middle one is perishable:
//
//	GET /{media-id}  -> {url, mime_type, sha256, file_size}
//	GET {url}        -> the bytes, Authorization: Bearer required
//
// THE URL LAPSES FIVE MINUTES AFTER IT IS ISSUED and answers 401 to a request
// without the token - opening one in a browser fails, which is the usual way
// somebody concludes the id is broken. So the url is never stored and never
// returned: the only durable handle is the media id, which lasts seven days
// from the webhook, and every attempt redoes both hops from it. That is what
// makes a retrying job safe, and it is why this is one method rather than two.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// MediaMaxBytes is the largest attachment this will fetch.
//
// Meta's own caps are 5 MB for an image and 16 MB for video, so anything
// claiming more than this is not a customer photograph. Checked against the
// size Meta reports BEFORE the body is requested, so an absurd claim costs one
// small request rather than a download.
const MediaMaxBytes = 32 << 20

// Media is what Meta knows about an attachment, beside its bytes.
type Media struct {
	MimeType string
	// SHA256 is Meta's own digest, carried so a stored file can be checked
	// against what was sent rather than against what arrived.
	SHA256    string
	SizeBytes int64
	// Filename is set only for a document.
	Filename string
}

// DownloadMedia fetches one attachment, doing both hops.
//
// The returned reader is the live response body: the caller MUST close it, and
// should spool it to disk rather than into memory - the length is chosen by
// whoever sent the message.
func (c *Client) DownloadMedia(ctx context.Context, mediaID string) (io.ReadCloser, Media, error) {
	if !c.Configured() {
		return nil, Media{}, fmt.Errorf("WhatsApp is not configured")
	}
	if strings.TrimSpace(mediaID) == "" {
		return nil, Media{}, fmt.Errorf("no media id to fetch")
	}

	found, url, err := c.mediaURL(ctx, mediaID)
	if err != nil {
		return nil, Media{}, err
	}
	if found.SizeBytes > MediaMaxBytes {
		return nil, Media{}, fmt.Errorf(
			"that attachment is %d bytes, over the %d byte limit", found.SizeBytes, MediaMaxBytes)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, Media{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)
	// Meta's CDN refuses a request with no User-Agent. Measured: without this
	// the download answers 400 while the same url succeeds from curl.
	req.Header.Set("User-Agent", "Tensor/1.0")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, Media{}, fmt.Errorf("could not reach WhatsApp's media host: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		// A 404 here is the five-minute window having closed between the two
		// hops. Retryable, and the retry starts again from the id.
		if resp.StatusCode == http.StatusNotFound {
			return nil, Media{}, fmt.Errorf(
				"that media url has expired; fetch it again from the id (404)")
		}
		return nil, Media{}, parseError(resp.StatusCode, raw)
	}
	// Trust the declared type over Meta's metadata only when the metadata is
	// silent: the content-type on the body is what the bytes actually are.
	if ct := resp.Header.Get("Content-Type"); ct != "" && found.MimeType == "" {
		found.MimeType = ct
	}
	return resp.Body, found, nil
}

// mediaURL is the first hop. Its answer is deliberately not returned to
// callers: see the file comment.
func (c *Client) mediaURL(ctx context.Context, mediaID string) (Media, string, error) {
	endpoint := fmt.Sprintf("%s/%s/%s", c.cfg.BaseURL, c.cfg.APIVersion, mediaID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Media{}, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return Media{}, "", fmt.Errorf("could not reach WhatsApp: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Media{}, "", fmt.Errorf("could not read WhatsApp's response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Media{}, "", parseError(resp.StatusCode, raw)
	}

	var payload struct {
		URL      string `json:"url"`
		MimeType string `json:"mime_type"`
		SHA256   string `json:"sha256"`
		FileSize int64  `json:"file_size"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Media{}, "", fmt.Errorf("could not decode WhatsApp's media response: %w", err)
	}
	if strings.TrimSpace(payload.URL) == "" {
		// A 2xx with no url is not an answer. Said out loud rather than
		// returned as an empty download, which would look like an empty file.
		return Media{}, "", fmt.Errorf("WhatsApp returned no url for that media id")
	}
	return Media{
		MimeType:  payload.MimeType,
		SHA256:    payload.SHA256,
		SizeBytes: payload.FileSize,
	}, payload.URL, nil
}
