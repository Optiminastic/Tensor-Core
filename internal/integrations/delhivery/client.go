// Package delhivery reads shipment tracking from Delhivery's carrier API.
//
// READ-ONLY, DELIBERATELY. Delhivery also exposes /api/p/update, which changes
// a shipment's delivery instructions (re-attempt, reschedule, return to
// origin). That is a real-world action on somebody's parcel, so nothing in this
// package can reach it: the only request it knows how to make is a track.
//
// THERE IS NO LIST-BY-STATUS. Measured against the live account: the tracking
// endpoint answers
//
//	{"Error": "parameter ref_ids/ref_nos or waybill is required"}
//
// with or without a status filter, and /api/cmu/get/ndr, /api/ndr/ and
// /api/v1/ndr/ all return Delhivery's own login page rather than JSON. So
// "which of our shipments are stuck" cannot be asked of Delhivery - the
// waybills have to come from somewhere else (Shopify's fulfilments) and the
// filtering happens here, on the tracked result.
package delhivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Optiminastic/tensor-core/internal/retry"
)

// trackBatchSize is how many waybills go in one request.
//
// Delhivery documents 50 as the cap for a comma-separated waybill list. A
// larger list is not rejected with an error that says so - it simply comes back
// short - so the chunking is here rather than left to the caller.
const trackBatchSize = 50

// trackConcurrency is how many of those requests are in flight at once.
//
// MEASURED, and the reason this is not 1: thirty days on the live store is 703
// waybills, which is fifteen sequential requests and about ten seconds of
// waiting - on a page somebody opens to answer "which parcels are stuck".
//
// Three rather than fifteen because these are somebody else's servers. Probed
// against the live API: twelve sequential single-waybill requests pass, and so
// do bursts of four - so the limit Delhivery enforces is on SUSTAINED volume,
// not on a burst. It was tripped in practice (HTTP 429) by re-tracking all 703
// parcels on every page load, which is what the caller's cache now prevents;
// the retry below is the second line of defence, not the first.
const trackConcurrency = 3

// DefaultBaseURL is the production tracking host. The staging host
// (staging-express.delhivery.com) has its own keys and its own data; pointing a
// production key at it returns nothing rather than failing, which looks exactly
// like "no shipments".
//
// Exported because the Settings form pre-fills it. A blank "Carrier host" box
// beside a connected integration reads as unconfigured, when in fact this is
// the host every request is already going to.
const DefaultBaseURL = "https://track.delhivery.com"

const defaultTimeout = 30 * time.Second

// Config is what one account needs. APIKey is the only secret.
type Config struct {
	APIKey string
	// BaseURL overrides the host, for tests and for staging. Empty means
	// production.
	BaseURL string
}

// Client tracks shipments for one Delhivery account.
type Client struct {
	http *http.Client
	cfg  Config
}

// New returns a client, or nil when there is no key.
//
// Nil rather than an error: an install with no Delhivery account is the normal
// case, and Configured() is what every caller checks.
func New(cfg Config) *Client {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	return &Client{http: &http.Client{Timeout: defaultTimeout}, cfg: cfg}
}

// Configured reports whether this client can call Delhivery.
func (c *Client) Configured() bool { return c != nil && strings.TrimSpace(c.cfg.APIKey) != "" }

// Track returns what Delhivery knows about each waybill, in no guaranteed
// order and with no guarantee that every waybill comes back - a waybill that
// belongs to another account is silently absent rather than refused.
func (c *Client) Track(ctx context.Context, waybills []string) ([]Shipment, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("delhivery is not configured")
	}

	seen := make(map[string]bool, len(waybills))
	unique := make([]string, 0, len(waybills))
	for _, w := range waybills {
		w = strings.TrimSpace(w)
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		unique = append(unique, w)
	}

	var chunks [][]string
	for start := 0; start < len(unique); start += trackBatchSize {
		end := start + trackBatchSize
		if end > len(unique) {
			end = len(unique)
		}
		chunks = append(chunks, unique[start:end])
	}

	// Results are collected PER CHUNK and flattened in order afterwards, so a
	// concurrent run returns the same list as a sequential one. A shared
	// append under a mutex would reorder the page between refreshes for no
	// reason.
	results := make([][]Shipment, len(chunks))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(trackConcurrency)
	for i, chunk := range chunks {
		i, chunk := i, chunk
		group.Go(func() error {
			batch, err := c.trackBatch(groupCtx, chunk)
			if err != nil {
				return err
			}
			results[i] = batch
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		// Not returned beside a partial list. A short list is
		// indistinguishable from a complete one to the caller, and this one
		// renders a page that claims to be every delivery.
		return nil, err
	}

	out := make([]Shipment, 0, len(unique))
	for _, batch := range results {
		out = append(out, batch...)
	}
	return out, nil
}

// ErrRateLimited is what Delhivery's 429 becomes, after the retries are spent.
var ErrRateLimited = errors.New("delhivery rate limited")

// rateLimited carries the carrier's own Retry-After so the loop waits as long
// as it was asked to rather than guessing.
type rateLimited struct{ after time.Duration }

func (e *rateLimited) Error() string {
	return "Delhivery is rate limiting Tensor, so the carrier could not be read. " +
		"It clears on its own in a minute or two - reload then."
}
func (e *rateLimited) Unwrap() error             { return ErrRateLimited }
func (e *rateLimited) Retryable() bool           { return true }
func (e *rateLimited) RetryAfter() time.Duration { return e.after }

// parseRetryAfter reads the header's seconds form. Delhivery sends no
// Retry-After at all today, which is why the policy below has a backoff of its
// own rather than relying on one.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	return 0
}

// retryPolicy backs off on a 429 and on nothing else. A rejected request was
// never executed, so retrying it cannot duplicate anything - and this client
// only ever reads.
var retryPolicy = retry.Policy{
	MaxAttempts: 3, BaseDelay: time.Second, MaxDelay: 8 * time.Second,
}

func (c *Client) trackBatch(ctx context.Context, waybills []string) ([]Shipment, error) {
	var out []Shipment
	err := retry.Do(ctx, retryPolicy, func(ctx context.Context) error {
		batch, err := c.trackBatchOnce(ctx, waybills)
		if err != nil {
			return err
		}
		out = batch
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) trackBatchOnce(ctx context.Context, waybills []string) ([]Shipment, error) {
	endpoint := c.cfg.BaseURL + "/api/v1/packages/json/?" +
		url.Values{"waybill": {strings.Join(waybills, ",")}}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	// Delhivery's scheme is "Token <key>", not "Bearer". Bearer answers with
	// the HTML login page, which decodes as a JSON failure rather than a 401.
	req.Header.Set("Authorization", "Token "+c.cfg.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach Delhivery: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("Delhivery rejected the API key")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		// Retryable, and the sentence says what to do rather than printing a
		// status code at somebody. "Delhivery tracking failed (429)" was what
		// this page showed in practice, and it tells the reader nothing.
		return nil, &rateLimited{after: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Delhivery tracking failed (%d)", resp.StatusCode)
	}

	// Delhivery answers a bad request with HTTP 200 and {"Error": "..."}, so
	// the status code alone is not enough to know the call worked.
	var body struct {
		Error        string `json:"Error"`
		ShipmentData []struct {
			Shipment Shipment `json:"Shipment"`
		} `json:"ShipmentData"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("could not decode Delhivery's response: %w", err)
	}
	if body.Error != "" {
		return nil, fmt.Errorf("Delhivery: %s", body.Error)
	}

	out := make([]Shipment, 0, len(body.ShipmentData))
	for _, entry := range body.ShipmentData {
		out = append(out, entry.Shipment)
	}
	return out, nil
}
