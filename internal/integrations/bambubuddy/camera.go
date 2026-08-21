package bambubuddy

// The printer camera: a live MJPEG stream and single-frame snapshots.
//
// Tensor proxies these rather than pointing a browser at BambuBuddy directly,
// and that is not a stylistic choice. BambuBuddy lives on the Tailscale network
// with the printers; an operator's browser does not. A page that embedded
// http://100.x.y.z:8000/... would work on the machine running BambuBuddy and
// nowhere else - which is the confusing kind of broken, since it works for
// whoever builds it.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// StreamToken is a short-lived credential for the camera endpoints.
//
// It exists because an <img> tag cannot send an X-API-Key header, so BambuBuddy
// accepts ?token=... on the stream and snapshot routes instead. Tensor never
// hands this to a browser - the proxy uses it server-side - but it is why the
// API key itself does not have to travel in a URL, where it would land in logs
// and history.
type StreamToken struct {
	Token string `json:"token"`
}

// CreateStreamToken mints a token for the camera stream and snapshot routes.
func (c *Client) CreateStreamToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/printers/camera/stream-token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("bambubuddy stream token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", ReasonError{Reason: rejectionReason(resp.Body, resp.StatusCode)}
	}
	var out StreamToken
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("bambubuddy stream token: decode response: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("bambubuddy stream token: empty token")
	}
	return out.Token, nil
}

// CameraStatus is what BambuBuddy knows about an active stream.
//
// Stalled is the interesting one: a stream can be "active" while no frame has
// arrived for a long time, which looks to a viewer like a frozen picture rather
// than an error.
type CameraStatus struct {
	Active            bool     `json:"active"`
	HasFrames         bool     `json:"has_frames"`
	SecondsSinceFrame *float64 `json:"seconds_since_frame"`
	StreamUptime      *float64 `json:"stream_uptime"`
	Stalled           bool     `json:"stalled"`
}

// GetCameraStatus reports whether a stream is running and receiving frames.
func (c *Client) GetCameraStatus(ctx context.Context, printerID int) (CameraStatus, error) {
	var out CameraStatus
	err := c.get(ctx, fmt.Sprintf("/api/v1/printers/%d/camera/status", printerID), &out)
	return out, err
}

// Stream is an open MJPEG stream. The caller must Close it.
type Stream struct {
	Body io.ReadCloser
	// ContentType carries the multipart boundary the client needs to split
	// frames, so it must be passed through to the browser unchanged rather
	// than replaced with a guess at "multipart/x-mixed-replace".
	ContentType string
}

// Close releases the upstream connection.
func (s *Stream) Close() error {
	if s == nil || s.Body == nil {
		return nil
	}
	return s.Body.Close()
}

// OpenStream starts an MJPEG stream from one printer's camera.
//
// fps caps the frame rate. It is a real cost control, not a nicety: every frame
// crosses the Tailscale link from a laptop on a home connection before it
// reaches the VPS, and an uncapped stream will happily saturate that.
//
// The request deliberately uses a client with NO timeout. A live stream is a
// response that never ends, so the package's normal request timeout would cut
// the picture off mid-view at exactly the wrong moment. Cancellation comes from
// ctx instead - when the viewer navigates away, the handler's context is done
// and the connection unwinds.
func (c *Client) OpenStream(ctx context.Context, printerID, fps int, token string) (*Stream, error) {
	url := fmt.Sprintf("%s/api/v1/printers/%d/camera/stream?fps=%d&token=%s",
		c.baseURL, printerID, fps, token)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.apiKey)

	stream := &http.Client{} // no Timeout: see the doc comment
	resp, err := stream.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bambubuddy camera stream: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		reason := rejectionReason(resp.Body, resp.StatusCode)
		_ = resp.Body.Close()
		return nil, ReasonError{Reason: reason}
	}
	return &Stream{Body: resp.Body, ContentType: resp.Header.Get("Content-Type")}, nil
}

// StopStream asks BambuBuddy to tear down a printer's camera stream.
//
// Best-effort by design, and the caller should treat a failure as unimportant:
// BambuBuddy stops on its own once nothing is reading, so this only shortens
// the window. Worth asking anyway - the far end is a printer on a home network,
// and leaving its camera running because a browser tab closed is rude.
func (c *Client) StopStream(ctx context.Context, printerID int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v1/printers/%d/camera/stop", c.baseURL, printerID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("bambubuddy camera stop: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReasonBytes))
	return nil
}
