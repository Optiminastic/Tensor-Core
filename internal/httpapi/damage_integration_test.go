package httpapi

// The damage form, end to end: the link the bot sends, the public page behind
// it, and the queue the shop reads.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/bot"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
)

// Tapping "Damage concern" hands the customer a form.
//
// What it pins, in order of how badly each would hurt:
//   - the message is a cta_url carrying a /d/<token> address, not a wall of
//     text with a link buried in it;
//   - a SECOND tap does not mint a second link. The raw token is never
//     stored, so a second link orphans every file uploaded against the first
//     and the customer would submit an empty report;
//   - the conversation stays IDLE. The flow this replaced ended in a handoff,
//     which silences the bot for twelve hours;
//   - an unknown token and an expired one are refused with the SAME sentence,
//     so probing learns nothing.
func TestIntegrationDamageLink(t *testing.T) {
	h := newWAHarness(t, nil)
	orderID := paidOrder(t, h.store, "T3DPS-115766", 7001)

	h.tap(t, content.OptionDamage)
	sent := h.meta.last(t)
	if sent.Type != "interactive" || sent.ButtonLabel != content.DamageLinkButton {
		t.Fatalf("damage message = %+v", sent)
	}
	if !strings.HasPrefix(sent.URL, waFrontendURL+"/d/") {
		t.Fatalf("damage link = %q", sent.URL)
	}
	token := strings.TrimPrefix(sent.URL, waFrontendURL+"/d/")

	var state string
	if err := h.store.Pool.QueryRow(h.ctx,
		`SELECT state FROM whatsapp_conversations WHERE wa_id = $1`, waCustomer).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(bot.StateIdle) {
		t.Fatalf("conversation state = %q, want idle so the menu still works", state)
	}

	// A second tap points them back at the link they already hold.
	h.tap(t, content.OptionDamage)
	if again := h.meta.last(t); again.Text != content.DamageLinkAlready {
		t.Fatalf("second tap = %+v", again)
	}
	if n := countRows(t, h, "SELECT count(*) FROM damage_links"); n != 1 {
		t.Fatalf("a second tap minted %d links", n)
	}

	public := "/public/damage/" + token
	rr := serve(h.router, httptest.NewRequest(http.MethodGet, public, nil))
	var form publicDamageResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &form)
	if rr.Code != http.StatusOK || form.Submitted || len(form.Files) != 0 {
		t.Fatalf("form = %d %s", rr.Code, rr.Body.String())
	}
	if form.MaxFiles != maxDamageFiles {
		t.Errorf("max files = %d", form.MaxFiles)
	}

	// ONE SENTENCE FOR EVERY REFUSAL.
	bad := serve(h.router, httptest.NewRequest(http.MethodGet, "/public/damage/not-a-token", nil))
	if bad.Code != http.StatusNotFound || !strings.Contains(bad.Body.String(), invalidLinkMessage) {
		t.Fatalf("unknown token = %d %s", bad.Code, bad.Body.String())
	}

	// Nothing to look at is not a report.
	empty := postDamageJSON(h.router, public, "115766")
	if empty.Code != http.StatusUnprocessableEntity {
		t.Fatalf("submit with no files = %d %s", empty.Code, empty.Body.String())
	}

	attachDamageFile(t, h, token, "image/jpeg")
	ok := postDamageJSON(h.router, public, "T3DPS-115766")
	if ok.Code != http.StatusOK {
		t.Fatalf("submit = %d %s", ok.Code, ok.Body.String())
	}

	reports, err := h.store.Q.ListDamageReports(h.ctx, gen.ListDamageReportsParams{
		BrandSlug: waBrand, RowLimit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 {
		t.Fatalf("filed %d reports, want 1", len(reports))
	}
	r := reports[0]
	if r.OrderNumber != "T3DPS-115766" || r.Status != damageNew || r.FileCount != 1 {
		t.Errorf("report = %+v", r)
	}
	// "T3DPS-115766" is matched on its longest digit run, so the report links
	// to Tensor's own order row and the queue can open it.
	if r.OrderID == nil || *r.OrderID != orderID {
		t.Errorf("order_id = %v, want %v", r.OrderID, orderID)
	}

	// A SECOND SUBMIT CORRECTS THE FIRST. The link stays open for its 24
	// hours, and somebody who finds another photograph must not become a
	// second line in the queue.
	attachDamageFile(t, h, token, "video/mp4")
	second := postDamageJSON(h.router, public, "115766")
	if second.Code != http.StatusOK {
		t.Fatalf("second submit = %d %s", second.Code, second.Body.String())
	}
	again, _ := h.store.Q.ListDamageReports(h.ctx, gen.ListDamageReportsParams{
		BrandSlug: waBrand, RowLimit: 10,
	})
	if len(again) != 1 || again[0].FileCount != 2 {
		t.Fatalf("after a second submit: %d reports, %+v", len(again), again)
	}

	// Reopening shows what they already sent, rather than an empty form.
	rr = serve(h.router, httptest.NewRequest(http.MethodGet, public, nil))
	_ = json.Unmarshal(rr.Body.Bytes(), &form)
	if !form.Submitted || len(form.Files) != 2 || form.OrderNumber != "115766" {
		t.Fatalf("reopened form = %s", rr.Body.String())
	}

	// An expired link is refused exactly as an unknown one is.
	if _, err := h.store.Pool.Exec(h.ctx,
		"UPDATE damage_links SET expires_at = now() - interval '1 hour'"); err != nil {
		t.Fatal(err)
	}
	stale := serve(h.router, httptest.NewRequest(http.MethodGet, public, nil))
	if stale.Code != http.StatusNotFound || !strings.Contains(stale.Body.String(), invalidLinkMessage) {
		t.Fatalf("expired token = %d %s", stale.Code, stale.Body.String())
	}
}

// An expired window lets the same person ask again.
//
// The partial unique index covers only the OPEN statuses, so without the
// sweep closing them a customer whose link ran out would be told for ever
// that they already have one.
func TestIntegrationDamageLinkReissuedAfterExpiry(t *testing.T) {
	h := newWAHarness(t, nil)
	h.tap(t, content.OptionDamage)
	first := h.meta.last(t).URL

	if _, err := h.store.Pool.Exec(h.ctx,
		"UPDATE damage_links SET expires_at = now() - interval '1 hour'"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Q.ExpireDamageLinks(h.ctx); err != nil {
		t.Fatal(err)
	}

	h.tap(t, content.OptionDamage)
	second := h.meta.last(t)
	if second.URL == "" || second.URL == first {
		t.Fatalf("after expiry the customer got %q (first was %q)", second.URL, first)
	}
	if n := countRows(t, h, "SELECT count(*) FROM damage_links"); n != 2 {
		t.Fatalf("damage links = %d, want the old one kept and a new one", n)
	}
}

// What the upload route turns away before anything is stored.
//
// The test server has no object storage, so a well-formed upload stops at
// that gate - which is what proves the checks in front of it ran first. A
// file that is not a photograph or a video never gets that far.
func TestIntegrationDamageUploadRefusals(t *testing.T) {
	h := newWAHarness(t, nil)
	h.tap(t, content.OptionDamage)
	token := strings.TrimPrefix(h.meta.last(t).URL, waFrontendURL+"/d/")
	path := "/public/damage/" + token + "/files"

	cases := []struct {
		name, filename, contentType string
		want                        int
	}{
		{"a photograph", "shot.jpg", "image/jpeg", http.StatusServiceUnavailable},
		{"a video", "clip.mp4", "video/mp4", http.StatusServiceUnavailable},
		{"a web page", "page.html", "text/html", http.StatusUnprocessableEntity},
		{"a script wearing an image type", "run.sh", "image/jpeg", http.StatusUnprocessableEntity},
		{"an image extension over another type", "shot.jpg", "application/zip", http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		rr := serve(h.router, uploadRequest(t, path, tc.filename, tc.contentType, 32))
		if rr.Code != tc.want {
			t.Errorf("%s = %d, want %d (%s)", tc.name, rr.Code, tc.want, rr.Body.String())
		}
	}

	// A token nobody issued, refused with the one sentence - and before the
	// body is looked at.
	nope := serve(h.router, uploadRequest(t, "/public/damage/guessed/files", "shot.jpg", "image/jpeg", 32))
	if nope.Code != http.StatusNotFound || !strings.Contains(nope.Body.String(), invalidLinkMessage) {
		t.Fatalf("unknown token upload = %d %s", nope.Code, nope.Body.String())
	}
}

// The cap is per LINK, not per request: eight files is the whole allowance
// however they are sent.
func TestIntegrationDamageFileCap(t *testing.T) {
	h := newWAHarness(t, nil)
	h.tap(t, content.OptionDamage)
	token := strings.TrimPrefix(h.meta.last(t).URL, waFrontendURL+"/d/")
	for i := 0; i < maxDamageFiles; i++ {
		attachDamageFile(t, h, token, "image/jpeg")
	}
	rr := serve(h.router, uploadRequest(t,
		"/public/damage/"+token+"/files", "ninth.jpg", "image/jpeg", 32))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("the ninth file = %d %s", rr.Code, rr.Body.String())
	}
}

// The queue's own endpoints, and who may use them.
func TestIntegrationDamageReportsAPI(t *testing.T) {
	h := newWAHarness(t, nil)
	paidOrder(t, h.store, "T3DPS-115766", 7002)

	h.tap(t, content.OptionDamage)
	token := strings.TrimPrefix(h.meta.last(t).URL, waFrontendURL+"/d/")
	fileID := attachDamageFile(t, h, token, "image/jpeg")
	if rr := postDamageJSON(h.router, "/public/damage/"+token, "115766"); rr.Code != http.StatusOK {
		t.Fatalf("submit = %d %s", rr.Code, rr.Body.String())
	}

	reader := h.minter.mint(t, []string{auth.WhatsAppRead.Key()})
	writer := h.minter.mint(t, []string{auth.WhatsAppRead.Key(), auth.WhatsAppReply.Key()})
	base := "/brands/" + waBrand + "/damage-reports"

	rr := doJSON(h.router, http.MethodGet, base, reader, nil)
	var list struct {
		Items  []damageReportJSON `json:"items"`
		Counts map[string]int32   `json:"counts"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if rr.Code != http.StatusOK || len(list.Items) != 1 {
		t.Fatalf("list = %d %s", rr.Code, rr.Body.String())
	}
	if list.Counts[damageNew] != 1 || list.Counts[""] != 1 {
		t.Errorf("counts = %v", list.Counts)
	}
	if list.Items[0].FileCount != 1 {
		t.Errorf("file count = %d", list.Items[0].FileCount)
	}

	id := list.Items[0].ID
	drawer := doJSON(h.router, http.MethodGet, base+"/"+id, reader, nil)
	var one struct {
		Files []damageFileJSON `json:"files"`
	}
	_ = json.Unmarshal(drawer.Body.Bytes(), &one)
	if drawer.Code != http.StatusOK || len(one.Files) != 1 || one.Files[0].ID != fileID.String() {
		t.Fatalf("detail = %d %s", drawer.Code, drawer.Body.String())
	}

	// A FILE ID IS NOT A CAPABILITY. The route joins report -> link -> file,
	// so an id that belongs to nothing on this report does not resolve -
	// without that this would serve any file_asset in the database by id.
	stray := doJSON(h.router, http.MethodGet,
		base+"/"+id+"/files/"+uuid.NewString(), reader, nil)
	if stray.Code != http.StatusNotFound {
		t.Errorf("a stray file id = %d", stray.Code)
	}

	// whatsapp:read is enough to look; it is not enough to close one.
	if rr := doJSON(h.router, http.MethodPatch, base+"/"+id, reader,
		map[string]string{"status": damageResolved}); rr.Code != http.StatusForbidden {
		t.Fatalf("patch without whatsapp:reply = %d", rr.Code)
	}
	if rr := doJSON(h.router, http.MethodPatch, base+"/"+id, writer,
		map[string]string{"status": "exploded"}); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("patch with a nonsense status = %d", rr.Code)
	}

	rr = doJSON(h.router, http.MethodPatch, base+"/"+id, writer,
		map[string]string{"status": damageInReview, "resolution_note": "replacement posted"})
	var updated damageReportJSON
	_ = json.Unmarshal(rr.Body.Bytes(), &updated)
	if rr.Code != http.StatusOK || updated.Status != damageInReview ||
		updated.ResolutionNote != "replacement posted" || updated.StatusChangedAt == nil {
		t.Fatalf("patch = %d %s", rr.Code, rr.Body.String())
	}
}

// --- helpers -----------------------------------------------------------------

// attachDamageFile puts a file on a link the way a finished upload would.
//
// Written straight to the database rather than posted, because the upload
// route needs object storage and these tests are about everything around it.
func attachDamageFile(t *testing.T, h *waHarness, token, contentType string) uuid.UUID {
	t.Helper()
	link, err := h.store.Q.GetDamageLinkByTokenHash(h.ctx, hashPersonalisationToken(token))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := h.store.Q.InsertFileAsset(h.ctx, gen.InsertFileAssetParams{
		ID: id, Filename: id.String() + ".bin", ContentType: contentType,
		SizeBytes: 1024, StorageKey: "damage/" + waBrand + "/" + id.String(),
		UploadedBy: damageUploader,
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Q.AttachDamageFile(h.ctx, gen.AttachDamageFileParams{
		LinkID: link.ID, FileID: id,
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func postDamageJSON(router http.Handler, path, orderID string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"order_id": orderID})
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return serve(router, req)
}

func uploadRequest(t *testing.T, path, filename, contentType string, size int) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreatePart(textprotoHeader(filename, contentType))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(part, bytes.NewReader(bytes.Repeat([]byte{0x7f}, size))); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func countRows(t *testing.T, h *waHarness, query string) int {
	t.Helper()
	var n int
	if err := h.store.Pool.QueryRow(h.ctx, query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func textprotoHeader(filename, contentType string) textproto.MIMEHeader {
	head := make(textproto.MIMEHeader)
	head.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	head.Set("Content-Type", contentType)
	return head
}

// defaultNode is one node of the shipped tree, for a test that needs to know
// what the bot will say before the shop has edited anything.
func defaultNode(t *testing.T, key string) content.Node {
	t.Helper()
	for _, n := range content.DefaultTree() {
		if n.Key == key {
			return n
		}
	}
	t.Fatalf("the default tree has no %q node", key)
	return content.Node{}
}
