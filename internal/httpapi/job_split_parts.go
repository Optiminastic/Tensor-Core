package httpapi

// A combo that became one job.
//
// An order that arrived before its product was set up in the registry got
// one job - JOB-116633 - where the registry now names three parts: plank,
// rose and keychain. adoptCurrentPart could only fix the one-part case, so
// such an order printed its plank (through the hardcoded Dual Name Plank
// path) and never made the other two at all.
//
// Rebuild now splits it: the job becomes the first part (JOB-116633-PLANK)
// and the other parts are created beside it, numbered and filled exactly as
// job creation would have made them, then all of them render from the
// registry.
//
// Only while the job is still queued and on no bed. A job that is already
// printing keeps its number and its part, and the operator is told why.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// errCannotSplit is a refusal the operator can act on; its text is shown.
var errCannotSplit = errors.New("cannot split into parts")

// splitIntoParts makes the missing part jobs for a multi-part product's
// single job, and returns the job as it now stands plus the jobs it created.
// A job that is not such a case comes back unchanged with nothing created.
func (s *Server) splitIntoParts(
	ctx context.Context, job gen.ProductionJob,
) (gen.ProductionJob, []gen.ProductionJob, error) {
	roles := singleJobRoles(s.partRolesForSKU(ctx, deref(job.Sku)))
	if len(roles) < 2 || isOneOfRoles(job.PartRole, roles) || job.OrderID == nil {
		return job, nil, nil
	}
	if job.BatchID != nil || job.Status != production.StatusQueued {
		return job, nil, fmt.Errorf("%w: %s is already on a bed or past queued, so its parts cannot be made from it",
			errCannotSplit, job.JobNumber)
	}
	order, err := s.store.Q.GetOrderByID(ctx, *job.OrderID)
	if err != nil {
		return job, nil, fmt.Errorf("load the order: %w", err)
	}
	siblings, err := s.store.Q.ListProductionJobsForOrder(ctx, job.OrderID)
	if err != nil {
		return job, nil, fmt.Errorf("load the order's jobs: %w", err)
	}
	for _, sib := range siblings {
		if sib.ID != job.ID && strings.EqualFold(deref(sib.Sku), deref(job.Sku)) && isOneOfRoles(sib.PartRole, roles) {
			return job, nil, nil // already split; never make the parts twice
		}
	}
	li, ok := s.lineForJob(ctx, order, job)
	if !ok {
		return job, nil, fmt.Errorf("%w: order %s has no line for %s", errCannotSplit, order.OrderNumber, deref(job.Sku))
	}
	return s.insertParts(ctx, order, li, job, roles)
}

// insertParts renames the job onto the first part and creates the rest.
func (s *Server) insertParts(
	ctx context.Context, order gen.Order, li production.LineItem, job gen.ProductionJob, roles []string,
) (gen.ProductionJob, []gen.ProductionJob, error) {
	minted := map[string]bool{}
	numbers := make([]string, len(roles))
	for i, role := range roles {
		n, err := s.numberForJob(ctx, order.OrderNumber, jobNumberForPart(order.OrderNumber, role), i, minted)
		if err != nil {
			return job, nil, err
		}
		minted[n], numbers[i] = true, n
	}
	status, confirms := production.AutoValidatePersonalisation(li)
	var created []gen.ProductionJob
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		n, err := q.SplitProductionJobIntoPart(ctx, gen.SplitProductionJobIntoPartParams{
			ID: job.ID, JobNumber: numbers[0], PartRole: roles[0],
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: %s moved on while it was being split", errCannotSplit, job.JobNumber)
		}
		for i, role := range roles[1:] {
			p := s.jobParamsFor(jobParamsInput{
				order: order, shopifyID: order.ShopifyOrderID, li: li, jobNumber: numbers[i+1],
				role: role, quantity: job.Quantity, status: status, confirms: confirms, generated: true,
			})
			made, err := q.InsertProductionJob(ctx, p)
			if err != nil {
				return err
			}
			related := job.ID
			if err := recordJobEvent(ctx, q, jobEvent{
				JobID: made.ID, EventType: production.EventJobCreated, ActorID: systemActor, RelatedJobID: &related,
				Metadata: map[string]any{"order_id": order.ID.String(), "split_from": job.JobNumber},
			}); err != nil {
				return err
			}
			created = append(created, made)
		}
		return nil
	})
	if err != nil {
		return job, nil, err
	}
	job.JobNumber, job.PartRole = numbers[0], roles[0]
	return job, created, nil
}

// lineForJob finds the order line a job was made from, by SKU.
func (s *Server) lineForJob(ctx context.Context, order gen.Order, job gen.ProductionJob) (production.LineItem, bool) {
	var items []production.LineItem
	if err := json.Unmarshal(order.LineItems, &items); err != nil {
		return production.LineItem{}, false
	}
	for _, li := range items {
		li = s.resolveLineSKU(ctx, li)
		if strings.EqualFold(strings.TrimSpace(deref(li.SKU)), strings.TrimSpace(deref(job.Sku))) {
			return li, true
		}
	}
	return production.LineItem{}, false
}

func isOneOfRoles(role string, roles []string) bool {
	for _, r := range roles {
		if strings.EqualFold(roleOrBody(r), roleOrBody(role)) {
			return true
		}
	}
	return false
}
