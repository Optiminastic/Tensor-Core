package httpapi

// The WhatsApp bot's River workers: answering a customer message, and noticing
// that staff took a conversation over.
//
// The decisions are all in whatsappbot/bot, which is pure. These workers load
// the conversation (locked, so one customer is handled one message at a time),
// feed the event through bot.Step, perform the actions it returns, and save.
//
// SENDS ARE NOT RETRIED. A WhatsApp send is not idempotent (see the whatsapp
// package), and retrying a job would also resend every reply the job had
// already made. So a failed send cancels the job and is logged; a failed
// Shopify read is retried, because it happens before anything was sent.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/integrations/whatsapp"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/bot"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/order"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/phone"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/summary"
)

// whatsappInboundMaxAttempts bounds retries of a Shopify read that keeps failing.
const whatsappInboundMaxAttempts = 5

// WhatsAppInboundWorker answers one customer message.
type WhatsAppInboundWorker struct {
	river.WorkerDefaults[production.WhatsAppInboundArgs]
	server *Server
	logger *slog.Logger
}

func NewWhatsAppInboundWorker(server *Server, logger *slog.Logger) *WhatsAppInboundWorker {
	return &WhatsAppInboundWorker{server: server, logger: orDefault(logger)}
}

func (w *WhatsAppInboundWorker) Work(ctx context.Context, job *river.Job[production.WhatsAppInboundArgs]) error {
	if job.Attempt > whatsappInboundMaxAttempts {
		return river.JobCancel(fmt.Errorf("gave up after %d attempts", whatsappInboundMaxAttempts))
	}
	a := job.Args
	turn := w.server.newBotTurn(ctx, a.BrandSlug, a.WaID, w.logger)
	return w.server.withWhatsAppConversation(ctx, a.BrandSlug, a.WaID, func(q *gen.Queries, c bot.Conversation) (bot.Conversation, error) {
		ev, err := inboundEvent(ctx, q, a)
		if err != nil {
			return c, err
		}
		return turn.run(ctx, q, c, ev)
	})
}

// WhatsAppEchoWorker hands a conversation to staff when they reply from the
// WhatsApp Business app.
type WhatsAppEchoWorker struct {
	river.WorkerDefaults[production.WhatsAppEchoArgs]
	server *Server
}

func NewWhatsAppEchoWorker(server *Server) *WhatsAppEchoWorker {
	return &WhatsAppEchoWorker{server: server}
}

func (w *WhatsAppEchoWorker) Work(ctx context.Context, job *river.Job[production.WhatsAppEchoArgs]) error {
	at := job.Args.At
	if at.IsZero() {
		at = time.Now()
	}
	return w.server.withWhatsAppConversation(ctx, job.Args.BrandSlug, job.Args.WaID,
		func(_ *gen.Queries, c bot.Conversation) (bot.Conversation, error) {
			next, _ := bot.Step(c, bot.StaffEcho{}, at.UTC())
			return next, nil
		})
}

// withWhatsAppConversation locks one customer's conversation for the whole
// job and saves the result in the same commit.
func (s *Server) withWhatsAppConversation(
	ctx context.Context, brand, waID string,
	fn func(*gen.Queries, bot.Conversation) (bot.Conversation, error),
) error {
	return pgx.BeginFunc(ctx, s.store.Pool, func(tx pgx.Tx) error {
		q := s.store.Q.WithTx(tx)
		if err := q.EnsureWhatsAppConversation(ctx, gen.EnsureWhatsAppConversationParams{BrandSlug: brand, WaID: waID}); err != nil {
			return err
		}
		row, err := q.LockWhatsAppConversation(ctx, gen.LockWhatsAppConversationParams{BrandSlug: brand, WaID: waID})
		if err != nil {
			return err
		}
		c, err := conversationFromRow(row)
		if err != nil {
			return err
		}
		next, err := fn(q, c)
		if err != nil {
			return err
		}
		return saveConversation(ctx, q, brand, next)
	})
}

// botTurn performs a bot's actions for one brand and one customer.
type botTurn struct {
	server   *Server
	logger   *slog.Logger
	brand    string
	waID     string
	wa       *whatsapp.Client
	settings whatsappBotSettings
	shop     string
	token    string
	shopOK   bool
}

func (s *Server) newBotTurn(ctx context.Context, brand, waID string, logger *slog.Logger) *botTurn {
	shop, token, ok := shopifyCredentials(s.store.Q.GetConnectionWithToken(ctx,
		gen.GetConnectionWithTokenParams{BrandSlug: brand, Provider: shopifyProvider}))
	return &botTurn{
		server: s, logger: orDefault(logger), brand: brand, waID: waID,
		wa: s.whatsappFor(ctx, brand), settings: s.whatsappBotSettingsFor(ctx, brand),
		shop: shop, token: token, shopOK: ok,
	}
}

// run feeds one event through the state machine, performing actions in order.
// A lookup's answer is fed back in as the next event.
func (t *botTurn) run(ctx context.Context, q *gen.Queries, c bot.Conversation, ev bot.Event) (bot.Conversation, error) {
	now := time.Now().UTC()
	pending := []bot.Event{ev}
	for len(pending) > 0 {
		next := pending[0]
		pending = pending[1:]
		var actions []bot.Action
		c, actions = bot.Step(c, next, now)
		for _, a := range actions {
			follow, err := t.perform(ctx, q, a)
			if err != nil {
				return c, err
			}
			if follow != nil {
				pending = append(pending, follow)
			}
		}
	}
	return c, nil
}

func (t *botTurn) perform(ctx context.Context, q *gen.Queries, a bot.Action) (bot.Event, error) {
	switch v := a.(type) {
	case bot.SendText:
		res, err := t.wa.SendText(ctx, t.waID, v.Text)
		return nil, t.recordSend(ctx, q, res, err, "text", v.Text)
	case bot.SendMenu:
		res, err := t.wa.SendList(ctx, t.waID, v.Text, v.Menu.Button, listRows(v.Menu))
		return nil, t.recordSend(ctx, q, res, err, "menu", v.Text)
	case bot.LookupOrder:
		return t.lookup(ctx, v)
	case bot.TagOrder:
		return nil, t.requireShop(t.server.shopify.AddOrderTags(ctx, t.shop, t.token, v.OrderID, v.Tag))
	case bot.AppendNote:
		return nil, t.requireShop(t.server.shopify.AppendOrderNote(ctx, t.shop, t.token, v.OrderID, v.Note))
	case bot.RecordConfirmation:
		return nil, recordConfirmationReply(ctx, q, v)
	default:
		return nil, fmt.Errorf("unknown bot action %T", a)
	}
}

// recordSend logs a reply in the inbox, or cancels the job when the send
// failed - see the file comment for why a send is never retried.
func (t *botTurn) recordSend(ctx context.Context, q *gen.Queries, res whatsapp.SendResult, err error, kind, body string) error {
	if err != nil {
		t.logger.Warn("whatsapp bot reply not sent", "brand", t.brand, "wa", phone.Mask(t.waID), "error", err)
		return river.JobCancel(err)
	}
	status := res.Status
	_, err = recordWhatsAppMessage(ctx, q, t.brand, t.waID, res.MessageID, directionOut, kind, body, "", &status, time.Now(), "")
	return err
}

func (t *botTurn) requireShop(err error) error {
	if !t.shopOK {
		return river.JobCancel(errors.New("brand has no connected Shopify store"))
	}
	return err
}

// lookup reads the order live and checks the asking number is the order's own.
func (t *botTurn) lookup(ctx context.Context, l bot.LookupOrder) (bot.Event, error) {
	notFound := bot.LookupResult{Purpose: l.Purpose, Outcome: bot.OrderNotFound}
	if !t.shopOK {
		t.logger.Warn("whatsapp order lookup without a Shopify connection", "brand", t.brand)
		return notFound, nil
	}
	o, found, err := t.server.shopify.OrderByName(ctx, t.shop, t.token, t.settings.OrderPrefix+l.Number)
	if err != nil {
		return nil, err // retried: nothing has been sent in this step yet
	}
	if !found {
		return notFound, nil
	}
	converted := botOrder(o)
	if !converted.HasPhone(t.waID) {
		t.logger.Info("whatsapp order lookup phone mismatch", "brand", t.brand, "order", o.Name, "wa", phone.Mask(t.waID))
		return bot.LookupResult{Purpose: l.Purpose, Outcome: bot.OrderPhoneMismatch}, nil
	}
	return bot.LookupResult{Purpose: l.Purpose, Outcome: bot.OrderFound, Order: converted}, nil
}

// recordConfirmationReply stores the customer's answer. A customer may change
// their mind, so any status can move to the new one.
func recordConfirmationReply(ctx context.Context, q *gen.Queries, r bot.RecordConfirmation) error {
	conf, err := q.GetOrderConfirmationByGID(ctx, r.OrderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var changeText *string
	if r.ChangeText != "" {
		changeText = &r.ChangeText
	}
	_, err = q.TransitionOrderConfirmation(ctx, gen.TransitionOrderConfirmationParams{
		OrderID: conf.OrderID, Status: string(r.Status), ChangeText: changeText,
		CustomerReply: true, FromStatuses: allConfirmationStatuses,
	})
	return err
}

var allConfirmationStatuses = []string{
	string(bot.ConfirmationPending), string(bot.ConfirmationSent), string(bot.ConfirmationReminded),
	string(bot.ConfirmationConfirmed), string(bot.ConfirmationChangeRequested),
	string(bot.ConfirmationAutoConfirmed), string(bot.ConfirmationUnreachable),
}

// inboundEvent turns a stored message into a bot event. A tap on a
// confirmation template's button is resolved to its order here, so the bot
// stays pure.
func inboundEvent(ctx context.Context, q *gen.Queries, a production.WhatsAppInboundArgs) (bot.Event, error) {
	switch a.MessageKind {
	case whatsapp.KindText:
		return bot.TextIn{Text: a.Text}, nil
	case whatsapp.KindButton:
		return buttonEvent(ctx, q, a.ButtonID)
	default:
		return bot.MediaIn{Kind: a.MessageKind}, nil
	}
}

func buttonEvent(ctx context.Context, q *gen.Queries, id string) (bot.Event, error) {
	confirmed := strings.HasPrefix(id, content.PayloadConfirm)
	if !confirmed && !strings.HasPrefix(id, content.PayloadChange) {
		return bot.ButtonIn{ID: id}, nil
	}
	gid := strings.TrimPrefix(strings.TrimPrefix(id, content.PayloadConfirm), content.PayloadChange)
	conf, err := q.GetOrderConfirmationByGID(ctx, gid)
	if errors.Is(err, pgx.ErrNoRows) {
		return bot.ButtonIn{ID: id}, nil // an order we never asked about: the bot shows the menu
	}
	if err != nil {
		return nil, err
	}
	return bot.ConfirmationReply{OrderID: conf.ShopifyGid, OrderName: conf.OrderNumber, Confirmed: confirmed}, nil
}

func listRows(m content.Menu) []whatsapp.ListRow {
	rows := make([]whatsapp.ListRow, 0, len(m.Options))
	for _, o := range m.Options {
		rows = append(rows, whatsapp.ListRow{ID: o.ID, Title: o.Title, Description: o.Description})
	}
	return rows
}

// botOrder converts a live Shopify order to the bot's order shape. The
// "WhatsApp Number" a customer typed at checkout counts as one of its phones.
func botOrder(o shopify.OrderStatus) order.Order {
	item := summary.LineItem{}
	for _, a := range o.Attributes {
		item.Attributes = append(item.Attributes, summary.Attribute{Key: a.Key, Value: a.Value})
	}
	out := order.Order{
		ID: o.ID, Name: o.Name, CreatedAt: o.CreatedAt, CancelledAt: o.CancelledAt,
		ShippingTitle: o.ShippingTitle, FulfillmentStatus: o.FulfillmentStatus,
		Phones: append(append([]string{}, o.Phones...), summary.WhatsAppNumber([]summary.LineItem{item})),
	}
	for _, f := range o.Fulfillments {
		of := order.Fulfillment{DisplayStatus: f.DisplayStatus}
		for _, tr := range f.Tracking {
			of.Tracking = append(of.Tracking, order.Tracking{Company: tr.Company, Number: tr.Number, URL: tr.URL})
		}
		out.Fulfillments = append(out.Fulfillments, of)
	}
	return out
}

func conversationFromRow(row gen.WhatsappConversation) (bot.Conversation, error) {
	var ctx bot.Context
	if len(row.Context) > 0 {
		if err := json.Unmarshal(row.Context, &ctx); err != nil {
			return bot.Conversation{}, fmt.Errorf("conversation context: %w", err)
		}
	}
	return bot.Conversation{
		WaID: row.WaID, State: bot.State(row.State), Ctx: ctx,
		LastCustomerAt: timePtr(row.LastCustomerAt), LastStaffAt: timePtr(row.LastStaffAt),
		HumanSince: timePtr(row.HumanSince), OptedOut: row.OptedOut,
	}, nil
}

func saveConversation(ctx context.Context, q *gen.Queries, brand string, c bot.Conversation) error {
	raw, err := json.Marshal(c.Ctx)
	if err != nil {
		return err
	}
	return q.SaveWhatsAppConversation(ctx, gen.SaveWhatsAppConversationParams{
		BrandSlug: brand, WaID: c.WaID, State: string(c.State), Context: raw,
		LastCustomerAt: db.Timestamptz(c.LastCustomerAt), LastStaffAt: db.Timestamptz(c.LastStaffAt),
		HumanSince: db.Timestamptz(c.HumanSince), OptedOut: c.OptedOut,
	})
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func orDefault(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}
