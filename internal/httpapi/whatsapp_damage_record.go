package httpapi

// Filing a damage report, once.
//
// The bot's state machine decides that a report has been made; this writes it
// down. Pure database work, no network, which is what makes it safe for a job
// River may retry: the insert is keyed on the wamid of the message that closed
// the flow, so a second attempt finds the row already there and stands down.
//
// This is the only durable record of a damage report inside Tensor. The tag
// and the note that follow it go to Shopify and can fail, and the
// conversation's own scratchpad is cleared the moment the chat is handed to a
// human.

import (
	"context"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/bot"
)

// recordDamageReport files one report and links the photographs it was built
// from.
func recordDamageReport(
	ctx context.Context, q *gen.Queries, brand, waID, messageID string, v bot.RecordDamage,
) error {
	// Best effort: the bot resolves orders against Shopify, so a report can
	// name an order Tensor has never imported. The report is filed either
	// way; order_id simply stays null and the page shows the number alone.
	params := gen.InsertDamageReportParams{
		BrandSlug:       brand,
		WaID:            waID,
		ShopifyOrderGid: v.OrderID,
		OrderNumber:     v.OrderName,
		PhotoCount:      int32(v.Photos),
		WaMessageID:     messageID,
	}
	if found, err := q.FindOrderByNumber(ctx, v.OrderName); err == nil {
		id := found.ID
		params.OrderID = &id
		params.CustomerName = deref(found.CustomerName)
	}

	report, err := q.InsertDamageReport(ctx, params)
	if err != nil {
		// No row back means this report is already filed - an earlier attempt
		// of this same job, or a webhook Meta replayed. Not an error, and
		// deliberately not a second set of photo links either.
		return nil
	}
	if len(v.PhotoMessages) == 0 {
		return nil
	}
	_, err = q.LinkDamageReportPhotos(ctx, gen.LinkDamageReportPhotosParams{
		ReportID: report.ID, WaMessageIds: v.PhotoMessages, BrandSlug: brand,
	})
	return err
}
