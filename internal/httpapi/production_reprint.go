package httpapi

import (
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// reprintParamsFor builds the insert params for a reprint of src.
//
// It replaces the old cloneForReprint, which copied the customer/personalisation
// fields but dropped the whole slicing-and-geometry snapshot: colours,
// support_used, infill_pct, the nozzle diameters, flow, quality, machine_family
// and the bounding box. Those are exactly the planner's groupKey inputs and the
// bed packer's geometry, so a reprint built the old way landed in a degenerate
// grouping bucket and could not be placed on a bed - which defeated the whole
// point of giving reprints urgent priority. splitProductionJob already carried
// them correctly; this is the same field set.
//
// The job number is passed in rather than minted here: it comes from a database
// sequence (NextJobNumber) so it needs a queries handle, and every caller is
// already inside the transaction that inserts the reprint.
//
// quantity is the number of units actually being reprinted, which may be fewer
// than src's: one damaged part out of five is a reprint of one. The source job
// keeps its own quantity - it is the record of a print that really happened,
// and rewriting it would falsify history and corrupt the split-group progress
// sums. Per-unit weights are scaled so a partial reprint does not reserve
// filament for the whole original run.
func reprintParamsFor(src gen.ProductionJob, quantity int32, jobNumber string) gen.InsertProductionJobParams {

	// Lower is more urgent; a job already more urgent than the cap keeps its
	// own priority rather than being demoted.
	priority := src.Priority
	if priority > production.UrgentPriority {
		priority = production.UrgentPriority
	}

	return gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: jobNumber,
		OrderID: src.OrderID, ShopifyOrderID: src.ShopifyOrderID,
		ShopifyCustomerID: src.ShopifyCustomerID, CustomerName: src.CustomerName,
		Description: src.Description, Quantity: quantity,
		Status:          production.StatusQueued,
		AssemblyStatus:  production.AssemblyPending,
		QcStatus:        production.QcPending,
		PackagingStatus: production.PackagingPending,
		Sku:             src.Sku, ProductName: src.ProductName,
		Material: src.Material, Colour: src.Colour, NozzleProfile: src.NozzleProfile,
		FilamentGramsRequired:     production.ScaleForQuantity(db.NumFloatPtr(src.FilamentGramsRequired), src.Quantity, quantity),
		PrintFileID:               src.PrintFileID,
		EstimatedPrintTimeMinutes: src.EstimatedPrintTimeMinutes,
		DueDate:                   src.DueDate, Priority: priority,
		PersonalisationName:        src.PersonalisationName,
		PersonalisationFont:        src.PersonalisationFont,
		PersonalisationColour:      src.PersonalisationColour,
		PersonalisationVariant:     src.PersonalisationVariant,
		PersonalisationStatus:      src.PersonalisationStatus,
		NameConfirmed:              src.NameConfirmed,
		PhotoConfirmed:             src.PhotoConfirmed,
		FontConfirmed:              src.FontConfirmed,
		ColourConfirmed:            src.ColourConfirmed,
		VariantConfirmed:           src.VariantConfirmed,
		CustomerApprovalReceived:   src.CustomerApprovalReceived,
		PersonalisationPhotoFileID: src.PersonalisationPhotoFileID,
		ReprintOfJobID:             ptr(src.ID),
		// The slicing/geometry snapshot the planner and bed packer read.
		Colours: src.Colours, SupportUsed: src.SupportUsed, InfillPct: db.NumFloatPtr(src.InfillPct),
		LeftNozzleMm: db.NumFloatPtr(src.LeftNozzleMm), RightNozzleMm: db.NumFloatPtr(src.RightNozzleMm),
		FlowPct: db.NumFloatPtr(src.FlowPct), QualityMm: db.NumFloatPtr(src.QualityMm), MachineFamily: src.MachineFamily,
		BboxXMm: db.NumFloatPtr(src.BboxXMm), BboxYMm: db.NumFloatPtr(src.BboxYMm), BboxZMm: db.NumFloatPtr(src.BboxZMm),
		SupportWeightG: production.ScaleForQuantity(db.NumFloatPtr(src.SupportWeightG), src.Quantity, quantity),
		PurgeWeightG:   production.ScaleForQuantity(db.NumFloatPtr(src.PurgeWeightG), src.Quantity, quantity),
		ColourCount:    src.ColourCount,
	}
}
