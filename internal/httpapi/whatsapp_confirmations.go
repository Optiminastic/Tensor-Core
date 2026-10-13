package httpapi

// The personalisation confirmation: after an order is paid, the customer is
// shown the names Tensor will print and asked to confirm or change them.
//
// PULLED, LIKE THE ORDERS THEMSELVES. Orders reach Tensor through the periodic
// Shopify sync, not a webhook, so confirmations are a sweep over the orders
// table rather than a reaction to an event: every few minutes, every brand with
// confirmations on, every paid order since they were switched on that has no
// confirmation yet. The outcome goes back to Shopify as an order tag, which is
// what production reads.
//
// One order, one transaction. The confirmation row and the send commit
// together: a send that fails for a reason that may pass leaves no row, so the
// next sweep tries again; a permanent failure stores the order as unreachable.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/integrations/whatsapp"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/bot"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/phone"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/summary"
)

const (
	confirmSweepBatch      = 50
	confirmReminderAfter   = 24 * time.Hour
	confirmAutoAfter       = 48 * time.Hour
	whatsappMessageKeep    = 180 * 24 * time.Hour
	fallbackCustomerName   = "there"
	shopifyOrderGIDPattern = "gid://shopify/Order/%d"
)

// recipientErrorCodes are Meta refusals about the NUMBER - not on WhatsApp,
// not registered, or not on a test number's allow-list. Only these mean the
// customer cannot be reached. Every other refusal (a template not yet
// approved, a revoked token) is the shop's configuration, and marking orders
// unreachable for it would tag every order while the real fault sits unfixed.
var recipientErrorCodes = map[int]bool{131026: true, 131030: true, 133010: true}

// errConfirmationsBlocked stops a brand's sweep for this round: its sends are
// failing for a reason that is not about any one customer.
var errConfirmationsBlocked = errors.New("whatsapp confirmations blocked by configuration")

// WhatsAppConfirmSweepWorker sends confirmations for newly paid orders.
type WhatsAppConfirmSweepWorker struct {
	river.WorkerDefaults[production.WhatsAppConfirmSweepArgs]
	server *Server
	logger *slog.Logger
}

func NewWhatsAppConfirmSweepWorker(server *Server, logger *slog.Logger) *WhatsAppConfirmSweepWorker {
	return &WhatsAppConfirmSweepWorker{server: server, logger: orDefault(logger)}
}

func (w *WhatsAppConfirmSweepWorker) Work(ctx context.Context, _ *river.Job[production.WhatsAppConfirmSweepArgs]) error {
	brands, err := w.server.store.Q.ListWhatsAppBrands(ctx)
	if err != nil {
		return err
	}
	for _, brand := range brands {
		if err := w.sweepBrand(ctx, brand); err != nil {
			w.logger.Error("whatsapp confirmation sweep", "brand", brand, "error", err)
		}
	}
	return nil
}

func (w *WhatsAppConfirmSweepWorker) sweepBrand(ctx context.Context, brand string) error {
	q := w.server.store.Q
	settings := w.server.whatsappBotSettingsFor(ctx, brand)
	if !settings.ConfirmationsEnabled {
		return q.StopWhatsAppConfirmations(ctx, brand)
	}
	since, err := q.StartWhatsAppConfirmations(ctx, brand)
	if err != nil {
		return err
	}
	orders, err := q.ListOrdersAwaitingConfirmation(ctx, gen.ListOrdersAwaitingConfirmationParams{
		OrderPrefix: settings.OrderPrefix, Since: since, RowLimit: confirmSweepBatch,
	})
	if err != nil {
		return err
	}
	turn := w.server.newBotTurn(ctx, brand, "", w.logger)
	for _, o := range orders {
		err := turn.confirmOrder(ctx, o)
		if errors.Is(err, errConfirmationsBlocked) {
			return err
		}
		if err != nil {
			w.logger.Warn("whatsapp confirmation not sent", "brand", brand, "order", o.OrderNumber, "error", err)
		}
	}
	return nil
}

// confirmOrder decides what one paid order needs and, when it needs a
// confirmation, sends it.
func (t *botTurn) confirmOrder(ctx context.Context, o gen.Order) error {
	items, err := summaryItems(o.LineItems)
	if err != nil {
		return err
	}
	conf := gen.InsertOrderConfirmationParams{
		OrderID: o.ID, BrandSlug: t.brand, OrderNumber: o.OrderNumber,
		ShopifyGid: fmt.Sprintf(shopifyOrderGIDPattern, o.ShopifyOrderID),
		FirstName:  firstName(o.CustomerName),
	}
	sum := summary.Build(items)
	conf.Summary = sum.Text
	to, reachable := orderRecipient(items, o)
	conf.WaID = to

	return pgx.BeginFunc(ctx, t.server.store.Pool, func(tx pgx.Tx) error {
		q := t.server.store.Q.WithTx(tx)
		switch {
		case !sum.HasPersonalisation:
			conf.Status = string(bot.ConfirmationNotNeeded)
			_, err := q.InsertOrderConfirmation(ctx, conf)
			return err
		case !reachable:
			conf.Status = string(bot.ConfirmationUnreachable)
			if _, err := q.InsertOrderConfirmation(ctx, conf); err != nil {
				return err
			}
			return t.tag(ctx, conf.ShopifyGid, content.TagUnreachable)
		case optedOut(ctx, q, t.brand, to) || !t.settings.mayMessage(to):
			conf.Status = string(bot.ConfirmationSkipped)
			_, err := q.InsertOrderConfirmation(ctx, conf)
			return err
		}
		conf.Status = string(bot.ConfirmationPending)
		if _, err := q.InsertOrderConfirmation(ctx, conf); err != nil {
			return err
		}
		row, err := q.GetOrderConfirmation(ctx, o.ID)
		if err != nil {
			return err
		}
		return t.sendConfirmation(ctx, tx, q, row, content.TemplateConfirm, bot.ConfirmationSent)
	})
}

// sendConfirmation sends the confirm or reminder template. The first send also
// schedules the reminder and the auto-confirm, in the same transaction.
func (t *botTurn) sendConfirmation(
	ctx context.Context, tx pgx.Tx, q *gen.Queries, conf gen.OrderConfirmation,
	template string, status bot.ConfirmationStatus,
) error {
	res, err := t.wa.SendTemplate(ctx, whatsapp.TemplateMessage{
		To: conf.WaID, Name: template, Language: content.TemplateLanguage,
		BodyParams:         []string{conf.FirstName, conf.OrderNumber, conf.Summary},
		QuickReplyPayloads: []string{content.PayloadConfirm + conf.ShopifyGid, content.PayloadChange + conf.ShopifyGid},
	})
	if err != nil {
		var metaErr *whatsapp.Error
		switch {
		case errors.As(err, &metaErr) && recipientErrorCodes[metaErr.Code]:
			t.logger.Warn("whatsapp confirmation undeliverable", "order", conf.OrderNumber, "wa", phone.Mask(conf.WaID), "error", err)
			return t.markUnreachable(ctx, q, conf.OrderID, conf.ShopifyGid)
		case errors.As(err, &metaErr) && metaErr.Permanent():
			return fmt.Errorf("%w: %v", errConfirmationsBlocked, err)
		}
		return err // rolls the row back, so the next sweep tries again
	}

	msgID := res.MessageID
	if err := q.MarkOrderConfirmationSent(ctx, gen.MarkOrderConfirmationSentParams{
		OrderID: conf.OrderID, Status: string(status), WaMessageID: &msgID,
	}); err != nil {
		return err
	}
	accepted := res.Status
	body := fmt.Sprintf("[%s] %s: %s", template, conf.OrderNumber, conf.Summary)
	if _, err := recordWhatsAppMessage(ctx, q, t.brand, conf.WaID, msgID, directionOut, "template", body, "", &accepted, time.Now(), ""); err != nil {
		return err
	}
	if status != bot.ConfirmationSent {
		return nil
	}
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	_, err = client.InsertManyTx(ctx, tx, []river.InsertManyParams{
		{Args: production.WhatsAppReminderArgs{OrderID: conf.OrderID}, InsertOpts: &river.InsertOpts{ScheduledAt: now.Add(confirmReminderAfter)}},
		{Args: production.WhatsAppAutoConfirmArgs{OrderID: conf.OrderID}, InsertOpts: &river.InsertOpts{ScheduledAt: now.Add(confirmAutoAfter)}},
	})
	return err
}

// markUnreachable records that the customer cannot be messaged, once, and
// tells production through the order's tags.
func (t *botTurn) markUnreachable(ctx context.Context, q *gen.Queries, orderID uuid.UUID, gid string) error {
	rows, err := q.TransitionOrderConfirmation(ctx, gen.TransitionOrderConfirmationParams{
		OrderID: orderID, Status: string(bot.ConfirmationUnreachable),
		FromStatuses: []string{string(bot.ConfirmationPending), string(bot.ConfirmationSent), string(bot.ConfirmationReminded)},
	})
	if err != nil || rows == 0 {
		return err
	}
	return t.tag(ctx, gid, content.TagUnreachable)
}

func (t *botTurn) tag(ctx context.Context, gid, tag string) error {
	if !t.shopOK {
		t.logger.Warn("whatsapp tag skipped: no Shopify connection", "brand", t.brand, "tag", tag)
		return nil
	}
	return t.server.shopify.AddOrderTags(ctx, t.shop, t.token, gid, tag)
}

// WhatsAppReminderWorker re-sends the confirmation once if nobody answered.
type WhatsAppReminderWorker struct {
	river.WorkerDefaults[production.WhatsAppReminderArgs]
	server *Server
	logger *slog.Logger
}

func NewWhatsAppReminderWorker(server *Server, logger *slog.Logger) *WhatsAppReminderWorker {
	return &WhatsAppReminderWorker{server: server, logger: orDefault(logger)}
}

func (w *WhatsAppReminderWorker) Work(ctx context.Context, job *river.Job[production.WhatsAppReminderArgs]) error {
	return pgx.BeginFunc(ctx, w.server.store.Pool, func(tx pgx.Tx) error {
		q := w.server.store.Q.WithTx(tx)
		conf, err := q.GetOrderConfirmation(ctx, job.Args.OrderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if conf.Status != string(bot.ConfirmationSent) || optedOut(ctx, q, conf.BrandSlug, conf.WaID) {
			return nil
		}
		turn := w.server.newBotTurn(ctx, conf.BrandSlug, conf.WaID, w.logger)
		return turn.sendConfirmation(ctx, tx, q, conf, content.TemplateReminder, bot.ConfirmationReminded)
	})
}

// WhatsAppAutoConfirmWorker lets production go ahead when nobody answered.
type WhatsAppAutoConfirmWorker struct {
	river.WorkerDefaults[production.WhatsAppAutoConfirmArgs]
	server *Server
	logger *slog.Logger
}

func NewWhatsAppAutoConfirmWorker(server *Server, logger *slog.Logger) *WhatsAppAutoConfirmWorker {
	return &WhatsAppAutoConfirmWorker{server: server, logger: orDefault(logger)}
}

func (w *WhatsAppAutoConfirmWorker) Work(ctx context.Context, job *river.Job[production.WhatsAppAutoConfirmArgs]) error {
	return pgx.BeginFunc(ctx, w.server.store.Pool, func(tx pgx.Tx) error {
		q := w.server.store.Q.WithTx(tx)
		conf, err := q.GetOrderConfirmation(ctx, job.Args.OrderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		rows, err := q.TransitionOrderConfirmation(ctx, gen.TransitionOrderConfirmationParams{
			OrderID: conf.OrderID, Status: string(bot.ConfirmationAutoConfirmed),
			FromStatuses: []string{string(bot.ConfirmationSent), string(bot.ConfirmationReminded)},
		})
		if err != nil || rows == 0 {
			return err // answered in time, or already handled
		}
		turn := w.server.newBotTurn(ctx, conf.BrandSlug, conf.WaID, w.logger)
		return turn.tag(ctx, conf.ShopifyGid, content.TagAutoConfirmed)
	})
}

// WhatsAppFailedWorker handles a send Meta could not deliver. For a
// confirmation it means the customer cannot be reached on WhatsApp.
type WhatsAppFailedWorker struct {
	river.WorkerDefaults[production.WhatsAppFailedArgs]
	server *Server
	logger *slog.Logger
}

func NewWhatsAppFailedWorker(server *Server, logger *slog.Logger) *WhatsAppFailedWorker {
	return &WhatsAppFailedWorker{server: server, logger: orDefault(logger)}
}

func (w *WhatsAppFailedWorker) Work(ctx context.Context, job *river.Job[production.WhatsAppFailedArgs]) error {
	return pgx.BeginFunc(ctx, w.server.store.Pool, func(tx pgx.Tx) error {
		q := w.server.store.Q.WithTx(tx)
		messageID := job.Args.MessageID
		conf, err := q.GetOrderConfirmationByMessageID(ctx, &messageID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		w.logger.Warn("whatsapp confirmation delivery failed", "order", conf.OrderNumber, "code", job.Args.ErrorCode)
		turn := w.server.newBotTurn(ctx, conf.BrandSlug, conf.WaID, w.logger)
		return turn.markUnreachable(ctx, q, conf.OrderID, conf.ShopifyGid)
	})
}

// WhatsAppPurgeWorker deletes message history past its retention.
type WhatsAppPurgeWorker struct {
	river.WorkerDefaults[production.WhatsAppPurgeArgs]
	server *Server
	logger *slog.Logger
}

func NewWhatsAppPurgeWorker(server *Server, logger *slog.Logger) *WhatsAppPurgeWorker {
	return &WhatsAppPurgeWorker{server: server, logger: orDefault(logger)}
}

func (w *WhatsAppPurgeWorker) Work(ctx context.Context, _ *river.Job[production.WhatsAppPurgeArgs]) error {
	cutoff := time.Now().Add(-whatsappMessageKeep)
	n, err := w.server.store.Q.PurgeWhatsAppMessages(ctx, db.Timestamptz(&cutoff))
	if err == nil && n > 0 {
		w.logger.Info("purged old whatsapp messages", "count", n)
	}
	return err
}

// summaryItems reads an order's line snapshot into the summary builder's shape.
func summaryItems(raw []byte) ([]summary.LineItem, error) {
	var lines []production.LineItem
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &lines); err != nil {
			return nil, fmt.Errorf("order line items: %w", err)
		}
	}
	items := make([]summary.LineItem, 0, len(lines))
	for _, l := range lines {
		item := summary.LineItem{Title: l.ProductName}
		if l.VariantTitle != nil {
			item.VariantTitle = *l.VariantTitle
		}
		for _, p := range l.Properties {
			item.Attributes = append(item.Attributes, summary.Attribute{Key: p.Name, Value: p.Value})
		}
		items = append(items, item)
	}
	return items, nil
}

// orderRecipient is the number to message: the WhatsApp number typed at
// checkout first, then the order's phone, then the shipping address's.
func orderRecipient(items []summary.LineItem, o gen.Order) (string, bool) {
	candidates := []string{summary.WhatsAppNumber(items)}
	if o.CustomerPhone != nil {
		candidates = append(candidates, *o.CustomerPhone)
	}
	var addr shopify.OrderAddress
	if len(o.ShippingAddress) > 0 && json.Unmarshal(o.ShippingAddress, &addr) == nil {
		candidates = append(candidates, addr.Phone)
	}
	for _, c := range candidates {
		if n, ok := phone.Normalize(c); ok {
			return n, true
		}
	}
	return "", false
}

func optedOut(ctx context.Context, q *gen.Queries, brand, waID string) bool {
	c, err := q.GetWhatsAppConversation(ctx, gen.GetWhatsAppConversationParams{BrandSlug: brand, WaID: waID})
	return err == nil && c.OptedOut
}

func firstName(customerName *string) string {
	if customerName == nil {
		return fallbackCustomerName
	}
	fields := strings.Fields(*customerName)
	if len(fields) == 0 {
		return fallbackCustomerName
	}
	return fields[0]
}
