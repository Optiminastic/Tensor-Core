package production

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// WhatsAppInboundQueueName carries everything that changes a conversation.
//
// ONE WORKER, so a customer's messages are handled in the order they arrived:
// a tap on "Where is my order" followed a second later by the order number
// must not be answered the other way round. The conversation row is also
// locked per job, which serialises a customer; the single worker additionally
// keeps arrival order. A shop's inbound volume is tiny next to what one worker
// clears.
const WhatsAppInboundQueueName = "whatsapp_inbound"

// WhatsAppQueueName carries the confirmation lifecycle, delivery updates and
// housekeeping - everything that does not depend on arrival order.
const WhatsAppQueueName = "whatsapp"

// WhatsAppInboundArgs is one customer message, already stored.
type WhatsAppInboundArgs struct {
	BrandSlug   string `json:"brand_slug"`
	WaID        string `json:"wa_id"`
	MessageID   string `json:"message_id"`
	MessageKind string `json:"message_kind"`
	Text        string `json:"text,omitempty"`
	ButtonID    string `json:"button_id,omitempty"`
}

func (WhatsAppInboundArgs) Kind() string { return "whatsapp_inbound" }
func (WhatsAppInboundArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: WhatsAppInboundQueueName}
}

// WhatsAppEchoArgs records that staff replied from the WhatsApp Business app,
// which hands the conversation to a human.
type WhatsAppEchoArgs struct {
	BrandSlug string    `json:"brand_slug"`
	WaID      string    `json:"wa_id"`
	At        time.Time `json:"at"`
}

func (WhatsAppEchoArgs) Kind() string { return "whatsapp_echo" }
func (WhatsAppEchoArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: WhatsAppInboundQueueName}
}

// WhatsAppFailedArgs is a send Meta could not deliver.
type WhatsAppFailedArgs struct {
	BrandSlug string `json:"brand_slug"`
	MessageID string `json:"message_id"`
	ErrorCode int    `json:"error_code,omitempty"`
}

func (WhatsAppFailedArgs) Kind() string { return "whatsapp_failed" }
func (WhatsAppFailedArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: WhatsAppQueueName}
}

// WhatsAppConfirmSweepArgs sends the personalisation confirmation for every
// newly paid order of every brand that has confirmations on.
type WhatsAppConfirmSweepArgs struct{}

func (WhatsAppConfirmSweepArgs) Kind() string { return "whatsapp_confirm_sweep" }

// WhatsAppReminderArgs re-sends a confirmation nobody answered.
type WhatsAppReminderArgs struct {
	OrderID uuid.UUID `json:"order_id"`
}

func (WhatsAppReminderArgs) Kind() string { return "whatsapp_confirm_reminder" }
func (WhatsAppReminderArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: WhatsAppQueueName, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// WhatsAppAutoConfirmArgs lets production go ahead when nobody answered.
type WhatsAppAutoConfirmArgs struct {
	OrderID uuid.UUID `json:"order_id"`
}

func (WhatsAppAutoConfirmArgs) Kind() string { return "whatsapp_auto_confirm" }
func (WhatsAppAutoConfirmArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: WhatsAppQueueName, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// WhatsAppPurgeArgs deletes message history past its retention.
type WhatsAppPurgeArgs struct{}

func (WhatsAppPurgeArgs) Kind() string { return "whatsapp_purge" }

// PeriodicWhatsAppConstructor schedules one of the args-free WhatsApp jobs.
func PeriodicWhatsAppConstructor(args river.JobArgs, period time.Duration) func() (river.JobArgs, *river.InsertOpts) {
	return func() (river.JobArgs, *river.InsertOpts) {
		return args, &river.InsertOpts{
			Queue:      WhatsAppQueueName,
			UniqueOpts: river.UniqueOpts{ByPeriod: period},
		}
	}
}

// WhatsAppEnqueuer inserts WhatsApp jobs inside the caller's transaction, so a
// stored message and the job that answers it commit together or not at all.
type WhatsAppEnqueuer struct{ client *river.Client[pgx.Tx] }

func NewWhatsAppEnqueuer(client *river.Client[pgx.Tx]) *WhatsAppEnqueuer {
	return &WhatsAppEnqueuer{client: client}
}

// EnqueueTx inserts jobs in tx. A nil enqueuer inserts nothing.
func (e *WhatsAppEnqueuer) EnqueueTx(ctx context.Context, tx pgx.Tx, jobs []river.InsertManyParams) error {
	if e == nil || e.client == nil || len(jobs) == 0 {
		return nil
	}
	_, err := e.client.InsertManyTx(ctx, tx, jobs)
	return err
}
