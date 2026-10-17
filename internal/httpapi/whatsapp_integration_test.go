package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertest"
	"github.com/riverqueue/river/rivertype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/bot"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
)

const (
	waBrand       = "gifting"
	waPhoneID     = "PNID-TEST"
	waAppSecret   = "test-app-secret"
	waVerifyToken = "test-verify-token"
	waCustomer    = "919876543210"
	waFrontendURL = "https://tensor.test"
)

// metaSend is one message Tensor posted to the fake Graph API.
type metaSend struct {
	To      string
	Type    string
	Text    string
	Buttons int
	// Rows is a list message's row count. A list and a button message are
	// both "interactive" to Meta but carry their choices in different
	// places, so counting only buttons reads a nine-row FAQ as empty.
	Rows     int
	Template string
	Payloads []string
	// URL and ButtonLabel are a cta_url message's link and the word on the
	// button. A cta_url is "interactive" to Meta like a button message, but
	// carries its one action under action.parameters rather than in a
	// buttons array - so without these a damage link reads as an interactive
	// message with no buttons and no text to check.
	URL         string
	ButtonLabel string
}

type fakeMeta struct {
	mu    sync.Mutex
	sends []metaSend
	fail  string // a Graph error body to answer with instead of success
}

func (f *fakeMeta) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var m struct {
		To          string                `json:"to"`
		Type        string                `json:"type"`
		Text        struct{ Body string } `json:"text"`
		Interactive struct {
			Body   struct{ Text string } `json:"body"`
			Action struct {
				Buttons  []any `json:"buttons"`
				Sections []struct {
					Rows []any `json:"rows"`
				} `json:"sections"`
				Parameters struct {
					DisplayText string `json:"display_text"`
					URL         string `json:"url"`
				} `json:"parameters"`
			} `json:"action"`
		} `json:"interactive"`
		Template struct {
			Name       string `json:"name"`
			Components []struct {
				SubType    string `json:"sub_type"`
				Parameters []struct {
					Payload string `json:"payload"`
				} `json:"parameters"`
			} `json:"components"`
		} `json:"template"`
	}
	_ = json.Unmarshal(raw, &m)

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, f.fail)
		return
	}
	s := metaSend{To: m.To, Type: m.Type, Text: m.Text.Body, Template: m.Template.Name}
	if m.Type == "interactive" {
		s.Text = m.Interactive.Body.Text
		s.Buttons = len(m.Interactive.Action.Buttons)
		for _, sec := range m.Interactive.Action.Sections {
			s.Rows += len(sec.Rows)
		}
		s.URL = m.Interactive.Action.Parameters.URL
		s.ButtonLabel = m.Interactive.Action.Parameters.DisplayText
	}
	for _, c := range m.Template.Components {
		if c.SubType == "quick_reply" && len(c.Parameters) > 0 {
			s.Payloads = append(s.Payloads, c.Parameters[0].Payload)
		}
	}
	f.sends = append(f.sends, s)
	_, _ = fmt.Fprintf(w, `{"messages":[{"id":"wamid.out.%d"}]}`, len(f.sends))
}

func (f *fakeMeta) last(t *testing.T) metaSend {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) == 0 {
		t.Fatal("nothing was sent to WhatsApp")
	}
	return f.sends[len(f.sends)-1]
}

func (f *fakeMeta) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

// fakeShopifyOrders answers the order-by-name lookup the bot makes. Not
// fakeShopify: that name belongs to the win-back suite's checkout fake in
// this same package, and two of them do not compile.
type fakeShopifyOrders struct {
	mu      sync.Mutex
	order   string // the orders.nodes JSON answered to OrderByName
	tags    []string
	notes   []string
	lookups []string
}

func (f *fakeShopifyOrders) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var call struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.Unmarshal(raw, &call)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.Contains(call.Query, "OrderByName"):
		f.lookups = append(f.lookups, fmt.Sprint(call.Variables["q"]))
		_, _ = fmt.Fprintf(w, `{"data":{"orders":{"nodes":[%s]}}}`, f.order)
	case strings.Contains(call.Query, "AddTags"):
		for _, tag := range call.Variables["tags"].([]any) {
			f.tags = append(f.tags, fmt.Sprint(tag))
		}
		_, _ = io.WriteString(w, `{"data":{"tagsAdd":{"userErrors":[]}}}`)
	case strings.Contains(call.Query, "OrderNote"):
		_, _ = io.WriteString(w, `{"data":{"order":{"note":null}}}`)
	case strings.Contains(call.Query, "SetNote"):
		input := call.Variables["input"].(map[string]any)
		f.notes = append(f.notes, fmt.Sprint(input["note"]))
		_, _ = io.WriteString(w, `{"data":{"orderUpdate":{"userErrors":[]}}}`)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

type waHarness struct {
	ctx    context.Context
	store  *db.Store
	server *Server
	router http.Handler
	meta   *fakeMeta
	shop   *fakeShopifyOrders
	minter *tokenMinter
}

func newWAHarness(t *testing.T, settings map[string]string) *waHarness {
	t.Helper()
	store := setupStore(t)
	seedGiftingBrand(t, store)
	ctx := context.Background()

	meta, shop := &fakeMeta{}, &fakeShopifyOrders{}
	metaSrv := httptest.NewServer(http.HandlerFunc(meta.handler))
	shopSrv := httptest.NewServer(http.HandlerFunc(shop.handler))
	t.Cleanup(metaSrv.Close)
	t.Cleanup(shopSrv.Close)

	base := map[string]string{
		"access_token": "wa-token", "phone_number_id": waPhoneID, "app_secret": waAppSecret,
		"webhook_verify_token": waVerifyToken, "bot_enabled": "true",
	}
	for k, v := range settings {
		base[k] = v
	}
	for k, v := range base {
		if err := store.Q.UpsertIntegrationSetting(ctx, gen.UpsertIntegrationSettingParams{
			BrandSlug: waBrand, Provider: providerWhatsApp, SettingKey: k, SettingValue: v,
		}); err != nil {
			t.Fatal(err)
		}
	}
	shopDomain, token := "test-shop.myshopify.com", "shop-token"
	if _, err := store.Q.UpsertConnection(ctx, gen.UpsertConnectionParams{
		ID: uuid.New(), BrandSlug: waBrand, Provider: shopifyProvider, Status: "connected",
		ExternalAccountID: &shopDomain, AccessToken: &token,
	}); err != nil {
		t.Fatal(err)
	}

	minter := newTokenMinter(t)
	cfg := config.Settings{
		Environment: "development", AuthAudience: "tensor-core",
		WhatsAppBaseURL: metaSrv.URL, ShopifyBaseURL: shopSrv.URL, ShopifyAPIVersion: "2024-10",
		FrontendURL: waFrontendURL,
	}
	server := NewServer(cfg, store, auth.NewGuards(minter.verifier, ""), nil)

	workers := river.NewWorkers()
	river.AddWorker(workers, NewWhatsAppInboundWorker(server, nil))
	river.AddWorker(workers, NewWhatsAppReminderWorker(server, nil))
	river.AddWorker(workers, NewWhatsAppAutoConfirmWorker(server, nil))
	client, err := river.NewClient(riverpgxv5.New(store.Pool), &river.Config{Workers: workers})
	if err != nil {
		t.Fatal(err)
	}
	server.EnableWhatsAppQueue(production.NewWhatsAppEnqueuer(client))

	return &waHarness{
		ctx: rivertest.WorkContext(ctx, client), store: store, server: server,
		router: server.Router(), meta: meta, shop: shop, minter: minter,
	}
}

func riverJob[T river.JobArgs](args T) *river.Job[T] {
	return &river.Job[T]{JobRow: &rivertype.JobRow{Attempt: 1}, Args: args}
}

func (h *waHarness) say(t *testing.T, text string) {
	t.Helper()
	h.inbound(t, production.WhatsAppInboundArgs{BrandSlug: waBrand, WaID: waCustomer, MessageKind: "text", Text: text})
}

func (h *waHarness) tap(t *testing.T, id string) {
	t.Helper()
	h.inbound(t, production.WhatsAppInboundArgs{BrandSlug: waBrand, WaID: waCustomer, MessageKind: "button", ButtonID: id})
}

func (h *waHarness) inbound(t *testing.T, a production.WhatsAppInboundArgs) {
	t.Helper()
	if err := NewWhatsAppInboundWorker(h.server, nil).Work(h.ctx, riverJob(a)); err != nil {
		t.Fatalf("inbound worker: %v", err)
	}
}

func (h *waHarness) jobs(t *testing.T, kind string) int {
	t.Helper()
	var n int
	if err := h.store.Pool.QueryRow(h.ctx, `SELECT count(*) FROM river_job WHERE kind = $1`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func signedWebhook(body string) *http.Request {
	mac := hmac.New(sha256.New, []byte(waAppSecret))
	mac.Write([]byte(body))
	req := httptest.NewRequest(http.MethodPost, "/integrations/whatsapp/webhook", bytes.NewReader([]byte(body)))
	req.Header.Set(headerHubSignature, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return req
}

func textWebhook(phoneID, messageID, text string) string {
	return fmt.Sprintf(`{"entry":[{"changes":[{"field":"messages","value":{"metadata":{"phone_number_id":%q},
"contacts":[{"wa_id":%q,"profile":{"name":"Asha"}}],
"messages":[{"from":%q,"id":%q,"timestamp":"%d","type":"text","text":{"body":%q}}]}}]}]}`,
		phoneID, waCustomer, waCustomer, messageID, time.Now().Unix(), text)
}

// imageWebhook is a customer photograph, with an optional caption.
func imageWebhook(phoneID, messageID, mediaID, caption string) string {
	return fmt.Sprintf(`{"entry":[{"changes":[{"field":"messages","value":{"metadata":{"phone_number_id":%q},
"contacts":[{"wa_id":%q,"profile":{"name":"Asha"}}],
"messages":[{"from":%q,"id":%q,"timestamp":"%d","type":"image",
"image":{"id":%q,"mime_type":"image/jpeg","sha256":"abc","caption":%q}}]}}]}]}`,
		phoneID, waCustomer, waCustomer, messageID, time.Now().Unix(), mediaID, caption)
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestIntegrationWhatsAppWebhook(t *testing.T) {
	h := newWAHarness(t, nil)

	hs := func(token string) int {
		return serve(h.router, httptest.NewRequest(http.MethodGet,
			"/integrations/whatsapp/webhook?hub.mode=subscribe&hub.challenge=42&hub.verify_token="+token, nil)).Code
	}
	if hs("wrong") != http.StatusForbidden || hs(waVerifyToken) != http.StatusOK {
		t.Fatal("verify-token handshake")
	}

	body := textWebhook(waPhoneID, "wamid.in.1", "hi")
	unsigned := httptest.NewRequest(http.MethodPost, "/integrations/whatsapp/webhook", strings.NewReader(body))
	if code := serve(h.router, unsigned).Code; code != http.StatusUnauthorized {
		t.Fatalf("unsigned webhook = %d", code)
	}
	for range 2 { // Meta retries; the second delivery must change nothing
		if code := serve(h.router, signedWebhook(body)).Code; code != http.StatusOK {
			t.Fatalf("signed webhook = %d", code)
		}
	}
	if n := h.jobs(t, production.WhatsAppInboundArgs{}.Kind()); n != 1 {
		t.Fatalf("inbound jobs = %d, want 1", n)
	}
	conv, err := h.store.Q.GetWhatsAppConversation(h.ctx, gen.GetWhatsAppConversationParams{BrandSlug: waBrand, WaID: waCustomer})
	if err != nil || conv.ProfileName != "Asha" || conv.LastMessagePreview != "hi" {
		t.Fatalf("conversation = %+v err=%v", conv, err)
	}

	stranger := textWebhook("SOMEONE-ELSE", "wamid.in.2", "hi")
	if code := serve(h.router, signedWebhook(stranger)).Code; code != http.StatusOK {
		t.Fatalf("unknown number = %d", code)
	}
	if n := h.jobs(t, production.WhatsAppInboundArgs{}.Kind()); n != 1 {
		t.Fatal("a message for a number no brand owns was acted on")
	}
}

func TestIntegrationWhatsAppMenuAndOrderLookup(t *testing.T) {
	h := newWAHarness(t, nil)

	h.say(t, "hi")
	if s := h.meta.last(t); s.Type != "interactive" || s.Buttons != 3 || s.To != waCustomer {
		t.Fatalf("greeting = %+v", s)
	}

	// "Other" opens the questions as a LIST, not buttons: nine answers do
	// not fit in three. A list row comes back as an ordinary button id,
	// which is what lets the bot answer both with one branch.
	//
	// It is also the tree's own ActionOpen in use - the second level is a
	// row pointing at another node, not a case in a switch.
	questions := defaultNode(t, content.QuestionsNode)
	h.tap(t, content.OptionOther)
	if s := h.meta.last(t); s.Rows != len(questions.Options) || s.Buttons != 0 || s.Text != questions.Body {
		t.Fatalf("questions list = %+v", s)
	}
	h.tap(t, content.FaqCOD)
	if s := h.meta.last(t); !strings.Contains(s.Text, "don't offer COD") {
		t.Fatalf("COD answer = %+v", s)
	}

	h.shop.order = `{"id":"gid://shopify/Order/9","name":"T3DPS-115766","createdAt":"2026-10-12T10:33:59Z",
"shippingLine":{"title":"FREE DISPATCH"},"displayFulfillmentStatus":"FULFILLED",
"shippingAddress":{"phone":"+91 98765 43210"},"lineItems":{"nodes":[]},
"fulfillments":[{"createdAt":"2026-10-13T10:00:00Z","displayStatus":"IN_TRANSIT","trackingInfo":[{"company":"Delhivery","number":"5990","url":"https://dl.example/5990"}]}]}`
	h.tap(t, content.OptionTrack)
	h.say(t, "T3DPS-115766")
	if len(h.shop.lookups) != 1 || h.shop.lookups[0] != "name:T3DPS-115766" {
		t.Fatalf("lookups = %v", h.shop.lookups)
	}
	if s := h.meta.last(t); !strings.Contains(s.Text, "Delhivery") || !strings.Contains(s.Text, "5990") {
		t.Fatalf("status reply = %+v", s)
	}

	var out int
	_ = h.store.Pool.QueryRow(h.ctx, `SELECT count(*) FROM whatsapp_messages WHERE direction='out'`).Scan(&out)
	if out != h.meta.count() {
		t.Fatalf("logged %d bot replies, sent %d", out, h.meta.count())
	}
}

func TestIntegrationWhatsAppPhoneMismatchHandsOff(t *testing.T) {
	h := newWAHarness(t, nil)
	h.shop.order = `{"id":"gid://shopify/Order/9","name":"T3DPS-115766","createdAt":"2026-10-12T10:33:59Z",
"shippingAddress":{"phone":"+91 90000 00000"},"lineItems":{"nodes":[]},"fulfillments":[]}`
	h.tap(t, content.OptionTrack)
	h.say(t, "115766")
	h.say(t, "115766")
	if s := h.meta.last(t); s.Text != content.Handoff {
		t.Fatalf("second mismatch = %q", s.Text)
	}
	before := h.meta.count()
	h.say(t, "hello?")
	if h.meta.count() != before {
		t.Fatal("the bot spoke while a human has the chat")
	}
}

// paidOrder stores an imported paid order the way the Shopify sync does.
func paidOrder(t *testing.T, store *db.Store, number string, shopifyID int64) uuid.UUID {
	t.Helper()
	id := uuid.New()
	lines, _ := json.Marshal([]production.LineItem{{
		ProductName: "Dual Name Plank",
		Properties: []production.LineProp{
			{Name: "STEP 4-First Name-", Value: "RAHUL"}, {Name: "STEP 5-Second Name", Value: "PRIYA"},
		},
	}})
	name, phoneNo, now := "Asha Rao", "+91 98765 43210", time.Now()
	if _, err := store.Q.UpsertPaidOrder(context.Background(), gen.UpsertPaidOrderParams{
		ID: id, ShopifyOrderID: shopifyID, OrderNumber: number, CustomerName: &name, CustomerPhone: &phoneNo,
		FinancialStatus: "paid", TotalPrice: 699, Currency: "INR", LineItems: lines, Status: "queued",
		PlacedAt: db.Timestamptz(&now), Attributes: []byte("[]"), Tags: []byte("[]"),
	}); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	return id
}

func (h *waHarness) sweep(t *testing.T) {
	t.Helper()
	if err := NewWhatsAppConfirmSweepWorker(h.server, nil).Work(h.ctx, riverJob(production.WhatsAppConfirmSweepArgs{})); err != nil {
		t.Fatal(err)
	}
}

func (h *waHarness) confirmation(t *testing.T, orderID uuid.UUID) gen.OrderConfirmation {
	t.Helper()
	c, err := h.store.Q.GetOrderConfirmation(h.ctx, orderID)
	if err != nil {
		t.Fatalf("confirmation: %v", err)
	}
	return c
}

func TestIntegrationWhatsAppConfirmationConfirmed(t *testing.T) {
	h := newWAHarness(t, map[string]string{"confirmations_enabled": "true"})
	old := paidOrder(t, h.store, "T3DPS-100", 100)
	h.sweep(t) // switches confirmations on; the order before it is never messaged
	if _, err := h.store.Q.GetOrderConfirmation(h.ctx, old); err == nil || h.meta.count() != 0 {
		t.Fatal("an order placed before confirmations were on was messaged")
	}

	time.Sleep(10 * time.Millisecond)
	orderID := paidOrder(t, h.store, "T3DPS-101", 101)
	h.sweep(t)
	h.sweep(t) // a second sweep must not send twice
	s := h.meta.last(t)
	if h.meta.count() != 1 || s.Template != content.TemplateConfirm || s.To != waCustomer {
		t.Fatalf("sends = %d, last = %+v", h.meta.count(), s)
	}
	gid := "gid://shopify/Order/101"
	if len(s.Payloads) != 2 || s.Payloads[0] != content.PayloadConfirm+gid {
		t.Fatalf("payloads = %v", s.Payloads)
	}
	if c := h.confirmation(t, orderID); c.Status != string(bot.ConfirmationSent) || !strings.Contains(c.Summary, "First Name RAHUL") {
		t.Fatalf("confirmation = %+v", c)
	}
	if h.jobs(t, production.WhatsAppReminderArgs{}.Kind()) != 1 || h.jobs(t, production.WhatsAppAutoConfirmArgs{}.Kind()) != 1 {
		t.Fatal("reminder and auto-confirm were not scheduled")
	}

	h.tap(t, content.PayloadConfirm+gid)
	if c := h.confirmation(t, orderID); c.Status != string(bot.ConfirmationConfirmed) {
		t.Fatalf("status = %s", c.Status)
	}
	if len(h.shop.tags) != 1 || h.shop.tags[0] != content.TagConfirmed {
		t.Fatalf("tags = %v", h.shop.tags)
	}

	if err := NewWhatsAppAutoConfirmWorker(h.server, nil).Work(h.ctx, riverJob(production.WhatsAppAutoConfirmArgs{OrderID: orderID})); err != nil {
		t.Fatal(err)
	}
	if len(h.shop.tags) != 1 {
		t.Fatalf("auto-confirm overrode the customer's answer: %v", h.shop.tags)
	}
}

func TestIntegrationWhatsAppConfirmationChangeAndSilence(t *testing.T) {
	h := newWAHarness(t, map[string]string{"confirmations_enabled": "true"})
	h.sweep(t)
	time.Sleep(10 * time.Millisecond)
	changed := paidOrder(t, h.store, "T3DPS-201", 201)
	silent := paidOrder(t, h.store, "T3DPS-202", 202)
	h.sweep(t)

	h.tap(t, content.PayloadChange+"gid://shopify/Order/201")
	h.say(t, "Second name should be PRIYAA")
	c := h.confirmation(t, changed)
	if c.Status != string(bot.ConfirmationChangeRequested) || c.ChangeText == nil || *c.ChangeText != "Second name should be PRIYAA" {
		t.Fatalf("change = %+v", c)
	}
	if len(h.shop.notes) != 1 || !strings.Contains(h.shop.notes[0], "PRIYAA") {
		t.Fatalf("notes = %v", h.shop.notes)
	}

	if err := NewWhatsAppReminderWorker(h.server, nil).Work(h.ctx, riverJob(production.WhatsAppReminderArgs{OrderID: silent})); err != nil {
		t.Fatal(err)
	}
	if s := h.meta.last(t); s.Template != content.TemplateReminder {
		t.Fatalf("reminder = %+v", s)
	}
	if err := NewWhatsAppAutoConfirmWorker(h.server, nil).Work(h.ctx, riverJob(production.WhatsAppAutoConfirmArgs{OrderID: silent})); err != nil {
		t.Fatal(err)
	}
	if c := h.confirmation(t, silent); c.Status != string(bot.ConfirmationAutoConfirmed) {
		t.Fatalf("silent order = %s", c.Status)
	}
	want := []string{content.TagChangeRequested, content.TagAutoConfirmed}
	if fmt.Sprint(h.shop.tags) != fmt.Sprint(want) {
		t.Fatalf("tags = %v, want %v", h.shop.tags, want)
	}
}

func TestIntegrationWhatsAppUnapprovedTemplateTagsNothing(t *testing.T) {
	h := newWAHarness(t, map[string]string{"confirmations_enabled": "true"})
	h.sweep(t)
	time.Sleep(10 * time.Millisecond)
	orderID := paidOrder(t, h.store, "T3DPS-301", 301)
	h.meta.fail = `{"error":{"message":"(#132001) Template name does not exist in the translation","code":132001}}`
	h.sweep(t)
	if _, err := h.store.Q.GetOrderConfirmation(h.ctx, orderID); err == nil {
		t.Fatal("a configuration error stored the order as handled")
	}
	if len(h.shop.tags) != 0 {
		t.Fatalf("a configuration error tagged orders: %v", h.shop.tags)
	}

	h.meta.fail = `{"error":{"message":"Message undeliverable","code":131026}}`
	h.sweep(t)
	if c := h.confirmation(t, orderID); c.Status != string(bot.ConfirmationUnreachable) {
		t.Fatalf("undeliverable = %s", c.Status)
	}
	if len(h.shop.tags) != 1 || h.shop.tags[0] != content.TagUnreachable {
		t.Fatalf("tags = %v", h.shop.tags)
	}
}

func TestIntegrationWhatsAppTestNumbersOnly(t *testing.T) {
	h := newWAHarness(t, map[string]string{"test_numbers": "+91 90000 00001, 9000000002", "confirmations_enabled": "true"})

	if code := serve(h.router, signedWebhook(textWebhook(waPhoneID, "wamid.in.t1", "hi"))).Code; code != http.StatusOK {
		t.Fatalf("webhook = %d", code)
	}
	if h.jobs(t, production.WhatsAppInboundArgs{}.Kind()) != 0 {
		t.Fatal("the bot answered a customer who is not on the test list")
	}
	var stored int
	_ = h.store.Pool.QueryRow(h.ctx, `SELECT count(*) FROM whatsapp_messages`).Scan(&stored)
	if stored != 1 {
		t.Fatalf("the message must still reach the inbox, stored = %d", stored)
	}

	h.sweep(t)
	time.Sleep(10 * time.Millisecond)
	orderID := paidOrder(t, h.store, "T3DPS-401", 401)
	h.sweep(t)
	if h.meta.count() != 0 {
		t.Fatal("a confirmation went to a customer who is not on the test list")
	}
	if c := h.confirmation(t, orderID); c.Status != string(bot.ConfirmationSkipped) {
		t.Fatalf("status = %s", c.Status)
	}
}

// The tree the Bot Messages page edits, and the bot obeying it.
//
// This is the whole point of the tree, so it is pinned end to end: a shop
// adds a button the code has never heard of, points it at a web address,
// and a customer tapping it gets a link - with no deploy anywhere.
func TestIntegrationBotTreeDrivesTheBot(t *testing.T) {
	h := newWAHarness(t, nil)
	reader := h.minter.mint(t, []string{auth.WhatsAppRead.Key()})
	writer := h.minter.mint(t, []string{auth.WhatsAppRead.Key(), auth.WhatsAppReply.Key()})
	path := "/brands/" + waBrand + "/whatsapp/bot-content"

	// Before anything is saved the page is given the code's own tree, so
	// there is always something to edit.
	rr := doJSON(h.router, http.MethodGet, path, reader, nil)
	var tree struct {
		Nodes    []botNodeJSON `json:"nodes"`
		FromCode bool          `json:"from_code"`
		Root     string        `json:"root"`
		Limits   struct {
			Buttons int `json:"buttons"`
			Rows    int `json:"rows"`
		} `json:"limits"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &tree)
	if rr.Code != http.StatusOK || !tree.FromCode || len(tree.Nodes) != 2 {
		t.Fatalf("default tree = %d %s", rr.Code, rr.Body.String())
	}
	if tree.Limits.Buttons != content.MaxButtons || tree.Root != content.RootNode {
		t.Fatalf("limits/root = %+v", tree)
	}

	// whatsapp:read may look; it may not rewrite what the bot says.
	if rr := doJSON(h.router, http.MethodPatch, path, reader, nil); rr.Code == http.StatusOK {
		t.Error("PATCH is not a method this router offers")
	}
	if rr := doJSON(h.router, http.MethodPut, path, reader,
		map[string]any{"nodes": tree.Nodes}); rr.Code != http.StatusForbidden {
		t.Fatalf("save without whatsapp:reply = %d", rr.Code)
	}

	// A FOURTH BUTTON IS REFUSED. Meta cannot send one, and finding that out
	// from a silent bot is the failure this check exists to prevent.
	tooMany := map[string]any{"nodes": []map[string]any{{
		"key": content.RootNode, "kind": "buttons", "body": "Hi",
		"options": []map[string]any{
			{"key": "a", "title": "One", "action": "answer", "answer": "1", "enabled": true},
			{"key": "b", "title": "Two", "action": "answer", "answer": "2", "enabled": true},
			{"key": "c", "title": "Three", "action": "answer", "answer": "3", "enabled": true},
			{"key": "d", "title": "Four", "action": "answer", "answer": "4", "enabled": true},
		},
	}}}
	rr = doJSON(h.router, http.MethodPut, path, writer, tooMany)
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "3 buttons") {
		t.Fatalf("a fourth button = %d %s", rr.Code, rr.Body.String())
	}

	// Now a tree the shop could plausibly have built: the damage button it
	// keeps, a link to the shop, and a question the code has never heard of
	// behind a list.
	saved := map[string]any{"nodes": []map[string]any{
		{
			"key": content.RootNode, "kind": "buttons", "body": "Hello from the shop",
			"options": []map[string]any{
				{"key": "damage", "title": "Broken item", "action": "damage", "enabled": true},
				{"key": "shop", "title": "Our shop", "action": "url",
					"target": "https://the3dprintingstore.in/", "button_label": "Visit store",
					"answer": "Have a look around:", "enabled": true},
				{"key": "more", "title": "Something else", "action": "open",
					"target": "q", "enabled": true},
			},
		},
		{
			"key": "q", "kind": "list", "body": "What can we help with?", "list_button": "See questions",
			"options": []map[string]any{
				{"key": "gift_wrap", "title": "Gift wrapping", "description": "Can you gift wrap it?",
					"action": "answer", "answer": "Yes — every order is gift wrapped free.", "enabled": true},
			},
		},
	}}
	if rr := doJSON(h.router, http.MethodPut, path, writer, saved); rr.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rr.Code, rr.Body.String())
	}

	// The greeting is now the shop's, with its own three buttons.
	h.say(t, "hi")
	if s := h.meta.last(t); s.Text != "Hello from the shop" || s.Buttons != 3 {
		t.Fatalf("greeting = %+v", s)
	}

	// A button the code has never heard of, opening another message.
	h.tap(t, "more")
	if s := h.meta.last(t); s.Rows != 1 || s.Text != "What can we help with?" {
		t.Fatalf("second level = %+v", s)
	}
	h.tap(t, "gift_wrap")
	if s := h.meta.last(t); !strings.Contains(s.Text, "gift wrapped free") {
		t.Fatalf("invented question = %+v", s)
	}

	// The URL action sends a link BUTTON, not a bare address in the text.
	h.tap(t, "shop")
	if s := h.meta.last(t); s.URL != "https://the3dprintingstore.in/" || s.ButtonLabel != "Visit store" {
		t.Fatalf("link option = %+v", s)
	}

	// Typing a position still works now the menu is the shop's: "2" is the
	// second button of THEIR greeting, not a fixed id.
	h.say(t, "2")
	if s := h.meta.last(t); s.URL != "https://the3dprintingstore.in/" {
		t.Fatalf("typed choice = %+v", s)
	}

	// And the damage button still reaches the flow the state machine owns,
	// under its reserved id, despite being renamed.
	h.tap(t, "damage")
	if n := countRows(t, h, "SELECT count(*) FROM damage_links"); n != 1 {
		t.Fatalf("renamed damage button minted %d links", n)
	}
}
