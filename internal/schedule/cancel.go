package schedule

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/signal"
)

type Cancellation struct {
	Plan         model.ConvoyPlan `json:"plan"`
	RemovedQueue int              `json:"removed_queue"`
	Compensated  int              `json:"compensated"`
}

// Cancel reverts queue and field-signal effects as one observable operation.
func (p *Planner) Cancel(ctx context.Context, planID uuid.UUID, note string, publisher *signal.Publisher, publication signal.Publication) (Cancellation, error) {
	plan, err := p.require(planID)
	if err != nil {
		return Cancellation{}, err
	}
	if plan.Cancelled {
		return Cancellation{Plan: plan}, nil
	}
	removed := p.admission.RemovePlan(plan.ChamberID, plan)
	if err := publisher.Compensate(context.WithoutCancel(ctx), plan.ChamberID, publication); err != nil {
		p.admission.Restore(plan.ChamberID, removed)
		return Cancellation{}, errors.Join(errors.New("signal compensation failed"), err)
	}
	for index := range removed {
		removed[index].State = model.AdmissionWaiting
		removed[index].AdmittedAt = nil
		removed[index].EnteredAt = nil
		removed[index].ResolvedAt = nil
	}
	p.admission.Restore(plan.ChamberID, removed)
	plan.Cancelled = true
	plan.CancelNote = note
	p.replace(plan)
	_, err = p.recorder.Record(context.WithoutCancel(ctx), plan.ChamberID, model.EventPlanCancelled, plan.ID, 0, map[string]any{
		"note": note, "restored_queue": len(removed), "compensated_signals": len(publication.Published),
	})
	return Cancellation{
		Plan: plan, RemovedQueue: len(removed), Compensated: len(publication.Published),
	}, err
}
