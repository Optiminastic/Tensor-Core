package delhivery

// Delhivery's exception queue - the three tabs their dashboard shows.
//
// A DIFFERENT TRANSPORT FROM THE REST OF THIS PACKAGE. Everything else here
// is REST against track.delhivery.com with "Authorization: Token <key>". This
// is JSON-RPC against Delhivery's D1 MCP GATEWAY with an OAuth bearer minted
// from Keycloak, and it is the only place the words "Need Action",
// "Auto Re-attempt" and "Return Recommendation" exist. The tracking API has no
// concept of them: it returns a scan and a sentence, and nothing about whose
// decision a parcel is waiting on.
//
// NOT one.delhivery.com. That host serves the dashboard, and its internal XHR
// paths (/web/api/exceptions/...) sit behind a CloudFront distribution that
// allows only cacheable methods - every POST, PUT and PATCH to it comes back
// as a CloudFront 403 HTML page that never reaches Delhivery's gateway at all.
// Measured, after this client was first written against it. The supported
// programmatic surface is the gateway, whose per-client URL looks like
// https://<gateway>/delhivery-one/c/<client>/mcp and whose tool names
// ("list-exceptions", "update-exceptions") are the contract.
//
// WHY THAT MATTERS, measured on the live account. The tracking API reported
// three parcels as undelivered (EOD-3 "Delivery Rescheduled by Customer",
// EOD-86 "Not attempted", U-PMA "Package found in Audit") while Delhivery's own
// Need Action tab was empty - because Delhivery had already booked an
// appointment for the first and owned the other two itself. Classifying scan
// codes locally to guess the tab reproduces exactly that mismatch, so this
// package does not guess: the row's own "action" field IS the tab.
//
// READ PLUS ONE WRITE. ApplyCTA acts on somebody's parcel - it can send a van
// or start a return - so it is deliberately the only write, it takes the CTA
// code from the row's own cta_list rather than a code this package invents, and
// its caller guards on dispatch:manage.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// DefaultUCPAuthURL mints the bearer. The realm is per account and is appended.
const DefaultUCPAuthURL = "https://ucp-auth.delhivery.com/facelessvoid"

// toolListExceptions and toolUpdateExceptions are the gateway's own tool
// names. They are the contract, in place of REST paths.
const (
	toolListExceptions   = "list-exceptions"
	toolUpdateExceptions = "update-exceptions"
)

// Tab is one of the three tabs on Delhivery's exceptions screen, in the
// spelling their API expects as a request filter.
type Tab string

const (
	// TabNeedAction is waiting on us.
	TabNeedAction Tab = "NEED_ACTION"
	// TabAutoReattempt is one Delhivery will retry by itself.
	TabAutoReattempt Tab = "AUTO_REATTEMPT"
	// TabReturnRecommendation is one Delhivery suggests giving up on.
	TabReturnRecommendation Tab = "RETURN_RECOMMENDATION"
	// TabClosed is resolved, and is history rather than work.
	TabClosed Tab = "CLOSED"
)

// Valid reports whether t is a tab Delhivery knows.
func (t Tab) Valid() bool {
	switch t {
	case TabNeedAction, TabAutoReattempt, TabReturnRecommendation, TabClosed:
		return true
	}
	return false
}

// BucketNDR is the exception bucket for non-delivery. Delhivery REQUIRES a
// bucket on every request - an omitted one is refused with "Bucket is a
// mandatory field and should be valid !" rather than defaulting to all.
const BucketNDR = "EB15"

// UCPConfig is one account's machine credential. Every field is required;
// there is no environment fallback, because a half-configured client that
// silently reads a different account's parcels is worse than one that says no.
type UCPConfig struct {
	ClientID     string
	ClientSecret string
	// Realm is account-specific, e.g. "ucp-5DG6Y57SLGT2". It is part of the
	// token URL, so a wrong one fails at sign-in rather than returning
	// somebody else's data.
	Realm string
	// HQClientID is the "cms::client::<uuid>" header every call carries. It
	// selects the account, so it must match the realm's.
	HQClientID string

	// GatewayURL is this client's own D1 MCP gateway endpoint, of the form
	// https://<gateway>/delhivery-one/c/<client>/mcp. It is per account and
	// has no sensible default, so it is required.
	GatewayURL string
	// UserEmail rides along as X-UCP-User-Email. Delhivery's gateway accepts
	// "--" when there is no human behind the call, which is the case here.
	UserEmail string

	// AuthURL overrides the Keycloak host, for tests. Empty means production.
	AuthURL string
}

// UCPClient reads and acts on one account's exception queue.
type UCPClient struct {
	http *http.Client
	cfg  UCPConfig

	// The bearer is cached because every page load would otherwise mint a
	// new one, and Keycloak counts that.
	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewUCP returns a client, or nil when any part of the credential is missing.
//
// Nil rather than an error, matching New: a brand with no UCP credential is the
// normal case - the tracking key alone is enough for the rest of this package -
// and Configured() is what callers check.
func NewUCP(cfg UCPConfig) *UCPClient {
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.ClientSecret = strings.TrimSpace(cfg.ClientSecret)
	cfg.Realm = strings.TrimSpace(cfg.Realm)
	cfg.HQClientID = strings.TrimSpace(cfg.HQClientID)
	cfg.GatewayURL = strings.TrimSpace(cfg.GatewayURL)
	if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.Realm == "" ||
		cfg.HQClientID == "" || cfg.GatewayURL == "" {
		return nil
	}
	if cfg.AuthURL == "" {
		cfg.AuthURL = DefaultUCPAuthURL
	}
	// The token URL is built as <auth>/realms/<realm>/..., so an auth URL
	// that already ends in /realms would produce /realms/realms/.
	cfg.AuthURL = strings.TrimSuffix(strings.TrimRight(cfg.AuthURL, "/"), "/realms")
	if strings.TrimSpace(cfg.UserEmail) == "" {
		cfg.UserEmail = "--"
	}
	return &UCPClient{http: &http.Client{Timeout: defaultTimeout}, cfg: cfg}
}

// Configured reports whether this client can call Delhivery.
func (c *UCPClient) Configured() bool { return c != nil && c.cfg.ClientSecret != "" }

// bearer returns a cached access token, minting one when it is missing or
// close to expiry.
//
// The sixty-second margin is because the token is checked here and spent on a
// request that travels: one that passes this check with two seconds left is
// refused at the far end, and the failure looks like a credential problem
// rather than a clock one.
func (c *UCPClient) bearer(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Add(time.Minute).Before(c.expires) {
		return c.token, nil
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
	}
	endpoint := strings.TrimRight(c.cfg.AuthURL, "/") +
		"/realms/" + url.PathEscape(c.cfg.Realm) + "/protocol/openid-connect/token"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("delhivery sign-in: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("delhivery sign-in returned %d", res.StatusCode)
	}
	if body.AccessToken == "" {
		// Keycloak's own words: "invalid_client" means the secret is wrong,
		// which is the operator's problem and worth saying out loud.
		reason := strings.TrimSpace(body.Description)
		if reason == "" {
			reason = strings.TrimSpace(body.Error)
		}
		if reason == "" {
			reason = "no token and no reason given"
		}
		return "", fmt.Errorf("delhivery refused the credential: %s", reason)
	}

	c.token = body.AccessToken
	// Keycloak's tokens are short (minutes). A missing expires_in is treated
	// as already-stale rather than forever-fresh.
	c.expires = time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)
	return c.token, nil
}

// Exception is one row of Delhivery's queue, in their own vocabulary.
//
// NOT TRANSLATED INTO OURS. The whole point of this screen is to show what
// Delhivery shows; a local re-wording is a second source of truth, and the
// mismatch that prompted this file came from exactly that.
type Exception struct {
	Waybill string `json:"awb_number"`
	ID      string `json:"exception_id"`
	Bucket  string `json:"exception_bucket"`
	// BucketName is Delhivery's label, e.g. "Pending NDR Verification".
	BucketName string `json:"exception_bucket_name"`
	// Action is the TAB this row sits on, as Delhivery files it, spelled for
	// a human: "Need Action", "Auto Re-attempt". This is the field that makes
	// the page match their dashboard.
	Action string `json:"action"`
	// CTAList maps the code to press to the words on the button. The codes
	// differ per bucket, so the buttons are built from this rather than from
	// a list in our code that would go stale.
	CTAList map[string]string `json:"cta_list"`

	Reason     string `json:"exception_nsl_remark"`
	Status     string `json:"pkg_status"`
	StatusType string `json:"pkg_status_type"`
	Payment    string `json:"payment_mode"`
	ResolvedBy string `json:"resolved_by"`
	Actionable string `json:"actionable"`

	// Epoch seconds, all of them.
	ExceptionTime int64 `json:"exception_time"`
	SLA           int64 `json:"sla"`
	PromiseDate   int64 `json:"promise_date"`
	LastUpdate    int64 `json:"last_update_time"`

	// The NDR proofs, which are what decide whether a re-attempt is worth
	// paying for: a "consignee unavailable" two kilometres from the address,
	// or with a three-second call, is a different decision from one at the
	// door after a real conversation.
	Geofence         bool    `json:"geofence"`
	GeofenceDistance float64 `json:"geofence_distance"`
	CallAttempted    bool    `json:"call_attempted"`
	CallDuration     int     `json:"call_duration"`
	WhatsAppVerified bool    `json:"wtsp_verified"`

	// DispatchCount arrives as a STRING ("1"), not a number. Decoded as one
	// and converted by Attempts rather than fought with a custom unmarshaller.
	DispatchCount string `json:"dispatch_count"`
}

// Attempts is DispatchCount as a number, and 0 when it is absent or unparseable.
func (e Exception) Attempts() int {
	n, err := strconv.Atoi(strings.TrimSpace(e.DispatchCount))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// ExceptionQuery narrows the queue. Tab and Status are both required by
// Delhivery; Bucket defaults to NDR.
type ExceptionQuery struct {
	Tab Tab
	// Status is OPEN, OVERDUE or CLOSED. Empty means open work, which is
	// what every tab but Closed is asking about.
	Status []string
	// Buckets defaults to NDR. Delhivery refuses an empty one.
	Buckets  []string
	Waybill  string
	Page     int
	PageSize int
}

type ucpListResponse struct {
	ResultCount int         `json:"result_count"`
	TotalCount  int         `json:"total_count"`
	Results     []Exception `json:"results"`
}

// dispatchSlices is every attempt count, expressed in the only two forms
// Delhivery's validator accepts.
//
// THERE IS NO "ANY NUMBER OF ATTEMPTS". Two rules meet here and leave no gap
// to slip through, both measured against the live account:
//
//   - The gateway's tool SCHEMA marks range_dispatch_count_op required, so
//     omitting it fails before Delhivery sees the call at all:
//     "Input validation error: 'range_dispatch_count_op' is a required property".
//   - Delhivery's VALIDATOR then rejects an empty operator and accepts only
//     "EQ <n>" (exactly that many attempts) or "GT 3" (more than three):
//     "Invalid operator for dispatch count. Only EQ and GT are supported."
//
// So a single call can only ever return ONE slice of attempt counts. Sending
// one - which is what Delhivery's own examples show - makes the page load
// cleanly while hiding every parcel with a different count, which is the exact
// quiet wrongness this screen exists to end. These five cover 0, 1, 2, 3 and
// everything above, and are merged.
var dispatchSlices = []struct {
	Op    string
	Value int
}{
	{"EQ", 0}, {"EQ", 1}, {"EQ", 2}, {"EQ", 3}, {"GT", 3},
}

// allExceptionStatuses is every status, and narrowing it is a bug.
//
// THE TAB IS THE FILTER, NOT THE STATUS. Delhivery CLOSES an exception the
// moment its own automation takes over - resolved_by "Internal triggers" -
// while still listing that parcel under Auto Reattempt, because the parcel is
// very much still out. Measured: AWB 69187910000604 is live (pkg_status
// "Pending", 2 attempts) and sits on their Auto Reattempt tab, yet asking for
// OPEN+OVERDUE returns nothing at all. Asking for all three and letting the
// "action" parameter pick the tab returns exactly the rows their dashboard
// shows, and asking for NEED_ACTION still correctly returns none of them.
//
// Delhivery requires a status, so this cannot simply be omitted.
var allExceptionStatuses = []string{"OPEN", "OVERDUE", "CLOSED"}

// sweepConcurrency bounds how much of one tab's sweep is in flight.
//
// Three rather than five, because the handler already runs the three tabs in
// parallel: five here would mean fifteen simultaneous requests to somebody
// else's gateway every time the page is opened. Three keeps the worst case at
// nine and still finishes the sweep in two rounds instead of five.
const sweepConcurrency = 3

// sweepPageSize is how many rows one request asks for, and maxSweepPages caps
// how far a slice is followed.
//
// The cap is a guard, not a limit anybody should hit: it exists so a gateway
// that ignores paging cannot spin this loop forever. A slice that reaches it
// is reported as truncated rather than silently cut.
const (
	sweepPageSize = 50
	maxSweepPages = 20
)

// ListExceptions returns one tab of the queue, across every attempt count.
//
// ALWAYS A SWEEP, never a single call - see dispatchSlices for why there is no
// cheaper option. A slice that fails is reported by name alongside the rows
// that did come back: a tab that is short and says so is worth far more than
// one that is short and does not.
func (c *UCPClient) ListExceptions(ctx context.Context, q ExceptionQuery) ([]Exception, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("delhivery one is not configured")
	}
	base := c.listArgs(q)

	rows := make([][]Exception, len(dispatchSlices))
	errs := make([]error, len(dispatchSlices))

	var group errgroup.Group
	group.SetLimit(sweepConcurrency)
	for i, slice := range dispatchSlices {
		i, slice := i, slice
		group.Go(func() error {
			rows[i], errs[i] = c.listSlice(ctx, base, slice.Op, slice.Value)
			return nil
		})
	}
	_ = group.Wait()

	merged := make([]Exception, 0, 16)
	seen := map[string]bool{}
	var missing []string
	var firstErr error

	for i, slice := range dispatchSlices {
		if errs[i] != nil {
			if firstErr == nil {
				firstErr = errs[i]
			}
			missing = append(missing, fmt.Sprintf("%s %d", slice.Op, slice.Value))
			continue
		}
		for _, row := range rows[i] {
			// A row can answer to more than one slice if Delhivery's count
			// moves mid-sweep. Showing it twice would have somebody
			// re-attempt the same parcel twice.
			key := row.ID
			if key == "" {
				key = row.Waybill
			}
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, row)
		}
	}

	if len(missing) == len(dispatchSlices) {
		// Nothing came back at all, so the refusal itself is the answer and
		// is passed through in Delhivery's own words.
		return nil, firstErr
	}
	if len(missing) > 0 {
		return merged, fmt.Errorf(
			"delhivery answered for some attempt counts but not %s, so this tab may be short (%v)",
			strings.Join(missing, ", "), firstErr)
	}
	return merged, nil
}

// listSlice reads one attempt-count slice, following its pages.
func (c *UCPClient) listSlice(
	ctx context.Context, base map[string]any, op string, value int,
) ([]Exception, error) {
	var all []Exception

	for page := 1; page <= maxSweepPages; page++ {
		args := make(map[string]any, len(base)+4)
		for k, v := range base {
			args[k] = v
		}
		args["range_dispatch_count_op"] = op
		args["range_dispatch_count_value"] = value
		args["page"] = page
		args["page_size"] = sweepPageSize

		var out ucpListResponse
		if err := c.callTool(ctx, toolListExceptions, args, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Results...)

		// A short page is the last page. result_count is not trusted for
		// this: it has come back as 3 beside a total_count of 0 on the live
		// account, so the rows themselves are counted instead.
		if len(out.Results) < sweepPageSize {
			break
		}
	}
	return all, nil
}

// listArgs is everything that does not vary across the sweep.
//
// The sort and exception-time parameters go out EMPTY rather than omitted: the
// gateway's schema marks them required too, and unlike the dispatch-count pair
// an empty value clears Delhivery's validator instead of tripping it. That
// asymmetry is theirs, not a mistake here.
func (c *UCPClient) listArgs(q ExceptionQuery) map[string]any {
	buckets := q.Buckets
	if len(buckets) == 0 {
		buckets = []string{BucketNDR}
	}
	status := q.Status
	if len(status) == 0 {
		status = allExceptionStatuses
	}

	args := map[string]any{
		"bucket": buckets,
		"status": status,

		"field":     "",
		"direction": "",
		"op":        "",
		"value":     "",
	}
	if q.Tab.Valid() {
		args["action"] = string(q.Tab)
	}
	if w := strings.TrimSpace(q.Waybill); w != "" {
		args["awb"] = w
	}
	return args
}

// CTAAction presses one button on one row.
type CTAAction struct {
	// ExceptionID and Bucket come from the row, not from us.
	ExceptionID string
	Bucket      string
	// Code is a key of the row's own CTAList, e.g. CTA023 (re-attempt) or
	// CTA002 (return). Taken from the row so a bucket whose buttons differ
	// still works without a code table here to go stale.
	Code string
	// Data is the action's payload where one applies. Most take none.
	Data map[string]any
}

// ApplyCTA sends one instruction to Delhivery.
//
// ONE AT A TIME, although the tool takes a list. A partial failure inside a
// batch is reported per item and there is nothing sensible to do with "three
// of your five vans were booked" on a screen - so the caller loops and each
// row gets its own answer.
func (c *UCPClient) ApplyCTA(ctx context.Context, a CTAAction) error {
	if !c.Configured() {
		return fmt.Errorf("delhivery one is not configured")
	}
	if strings.TrimSpace(a.ExceptionID) == "" || strings.TrimSpace(a.Code) == "" {
		return fmt.Errorf("an exception id and an action code are both required")
	}
	bucket := strings.TrimSpace(a.Bucket)
	if bucket == "" {
		bucket = BucketNDR
	}

	args := map[string]any{
		"exception_id":  strings.TrimSpace(a.ExceptionID),
		"actionable_id": strings.TrimSpace(a.Code),
		"bucket_id":     bucket,
	}
	if len(a.Data) > 0 {
		args["action_info"] = map[string]any{"action_data": a.Data}
	}

	var out struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := c.callTool(ctx, toolUpdateExceptions, args, &out); err != nil {
		return err
	}
	if !out.Success {
		// Delhivery's refusals name the parcel's state, which is the one
		// thing an operator can act on, so it is passed through.
		reason := strings.TrimSpace(out.Message)
		if reason == "" {
			reason = "no reason given"
		}
		return fmt.Errorf("delhivery would not take that: %s", reason)
	}
	return nil
}

// callTool runs one tool on the gateway and decodes what it returned.
//
// THE RESULT IS JSON INSIDE JSON. The gateway speaks JSON-RPC, and the tool's
// actual payload arrives as TEXT inside result.content[0].text - so there are
// two decodes, and a refusal can surface at either level: as a JSON-RPC error
// object, or as Delhivery's own error envelope inside that text.
//
// THE RESPONSE MAY BE SSE. The gateway negotiates text/event-stream and can
// answer with a single "data: {...}" line rather than a bare body, so that
// framing is unwrapped before the first decode instead of failing it.
func (c *UCPClient) callTool(ctx context.Context, tool string, args map[string]any, out any) error {
	token, err := c.bearer(ctx)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.GatewayURL,
		bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-hq-client-id", c.cfg.HQClientID)
	req.Header.Set("X-UCP-Realm", c.cfg.Realm)
	req.Header.Set("X-UCP-User-Email", c.cfg.UserEmail)
	req.Header.Set("X-Request-ID", requestID())

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("delhivery one: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	raw, err := readLimited(res.Body)
	if err != nil {
		return fmt.Errorf("delhivery one: %w", err)
	}
	body := unwrapSSE(raw)

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		said := summarise(body)
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			// Worth distinguishing from a bad secret: the bearer WAS minted,
			// so the credential is real and this client is simply not
			// entitled to this gateway.
			return fmt.Errorf("delhivery signed the credential in but refused the gateway (%d): %s",
				res.StatusCode, said)
		}
		return fmt.Errorf("delhivery one returned %d: %s", res.StatusCode, said)
	}

	var envelope struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("delhivery one: %s", summarise(body))
	}
	if envelope.Error != nil {
		return fmt.Errorf("%s",
			strings.TrimPrefix(strings.TrimSpace(envelope.Error.Message), "internal:"))
	}
	if len(envelope.Result.Content) == 0 {
		return fmt.Errorf("delhivery one answered with nothing")
	}

	text := []byte(envelope.Result.Content[0].Text)

	// Delhivery's own refusal envelope travels INSIDE the text, with the
	// JSON-RPC call reported as a success. Looked for before decoding, or a
	// refusal silently becomes an empty result set - which on this screen
	// reads as "nothing is stuck", the most expensive wrong answer it can
	// give.
	var fail struct {
		Code    string `json:"error_code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(text, &fail) == nil && fail.Code != "" {
		reason := strings.TrimSpace(fail.Message)
		if reason == "" {
			reason = fail.Code
		}
		return fmt.Errorf("%s", strings.TrimPrefix(reason, "internal:"))
	}
	if envelope.Result.IsError {
		return fmt.Errorf("delhivery one: %s", summarise(text))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(text, out)
}

// unwrapSSE pulls the payload out of a one-event text/event-stream body and
// leaves a plain JSON body untouched.
func unwrapSSE(raw []byte) []byte {
	text := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(text, "data:") && !strings.Contains(text, "\ndata:") {
		return raw
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if after, found := strings.CutPrefix(line, "data:"); found {
			return []byte(strings.TrimSpace(after))
		}
	}
	return raw
}

// requestID is echoed in Delhivery's own logs, and is the only handle for
// asking them about one specific call that went wrong.
func requestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "tensor"
	}
	return hex.EncodeToString(b[:])
}

// maxUCPBody caps what is read from Delhivery. Their error pages are HTML and
// an unauthenticated call has returned a full login page before now; a cap
// keeps a misconfiguration from pulling a megabyte into memory per request.
const maxUCPBody = 4 << 20

func readLimited(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, maxUCPBody))
}

// maxReason is how much of a refusal body is worth putting on screen. Long
// enough for Delhivery's sentence, short enough that an HTML error page does
// not become the error message.
const maxReason = 300

// summarise turns a refusal body into one line an operator can act on.
//
// Delhivery answers a refusal with JSON sometimes and with an HTML error page
// other times, and the HTML is worthless on screen - so it is named as HTML
// rather than pasted in as angle brackets.
func summarise(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "no reason given"
	}
	if strings.HasPrefix(text, "<") {
		return "an HTML error page rather than an API response, which usually means the " +
			"request never reached the API"
	}
	text = strings.Join(strings.Fields(text), " ")
	text = strings.TrimPrefix(text, "internal:")
	if len(text) > maxReason {
		text = text[:maxReason] + "…"
	}
	return text
}
