package admission

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/gate"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/signal"
)

type Decision struct {
	Allowed  bool                 `json:"allowed"`
	Reason   string               `json:"reason"`
	VesselID uuid.UUID            `json:"vessel_id,omitempty"`
	State    model.AdmissionState `json:"state,omitempty"`
}

// Controller grants admission only from observed sealed gate positions and
// advances exactly one queued vessel at a time.
type Controller struct {
	queue   *Queue
	gates   *gate.State
	signals *signal.Store
	now     func() time.Time
}

func NewController(queue *Queue, gates *gate.State, signals *signal.Store, now func() time.Time) *Controller {
	if now == nil {
		now = time.Now
	}
	return &Controller{queue: queue, gates: gates, signals: signals, now: now}
}

// Evaluate clears the single cursor vessel for the active transit. It never
// releases a vessel queued behind another one.
func (c *Controller) Evaluate(ctx context.Context, cycle model.TransitCycle) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	entry, ok := c.queue.Peek(cycle.ChamberID)
	if !ok {
		return Decision{Reason: "queue empty"}, nil
	}
	decision := Decision{VesselID: entry.Vessel.ID, State: entry.State}
	if entry.PlanID != cycle.PlanID {
		decision.Reason = "head vessel belongs to another plan"
		return decision, nil
	}
	switch entry.State {
	case model.AdmissionFailed:
		decision.Reason = "head vessel failed admission or lost contact; convoy is blocked"
		return decision, nil
	case model.AdmissionEntered:
		decision.Reason = "head vessel already entered; await next vessel confirmation"
		return decision, nil
	case model.AdmissionWithdrawn:
		decision.Reason = "head vessel was withdrawn"
		return decision, nil
	case model.AdmissionAdmitted:
		decision.Reason = "head vessel already cleared; awaiting entry confirmation"
		return decision, nil
	}
	// Before the entry gate opens, a vessel may only be cleared from a fully
	// sealed chamber. While admitting, the entry gate is open and the convoy
	// streams in one confirmed vessel at a time.
	if cycle.Stage != model.StageAdmitting && !c.gates.Sealed(cycle.ChamberID) {
		decision.Reason = "chamber gates are not sealed"
		return decision, nil
	}
	// With the chamber sealed, only the very first vessel may be pre-cleared at
	// transit start. Every later vessel must wait until the entry gate is open.
	if cycle.Stage != model.StageAdmitting {
		if progress, ok := c.queue.PlanProgress(cycle.ChamberID, entry.PlanID); ok {
			if progress.Admitted+progress.Entered+progress.Withdrawn+progress.Failed > 0 {
				decision.Reason = "entry gate is not open; later vessels wait for the admitting stage"
				return decision, nil
			}
		}
	}
	granted, err := c.queue.Grant(cycle.ChamberID, entry.Vessel.ID, c.now())
	if err != nil {
		return Decision{}, err
	}
	state := c.signals.Set(entry.Vessel.ID, cycle.ID, cycle.Generation, model.SignalProceed, c.now())
	if state.Aspect != model.SignalProceed {
		return Decision{}, errors.New("failed to commit admission decision")
	}
	return Decision{
		Allowed: true, VesselID: granted.Vessel.ID,
		State: model.AdmissionAdmitted, Reason: "sealed chamber ready",
	}, nil
}

// ConfirmEntry records that a cleared vessel physically entered the chamber.
func (c *Controller) ConfirmEntry(cycle model.TransitCycle, vesselID uuid.UUID) (QueueEntry, error) {
	if vesselID == uuid.Nil {
		entry, ok := c.queue.Peek(cycle.ChamberID)
		if !ok {
			return QueueEntry{}, errors.New("admission queue is empty")
		}
		vesselID = entry.Vessel.ID
	}
	entry, err := c.queue.MarkEntered(cycle.ChamberID, vesselID, c.now())
	if err != nil {
		return QueueEntry{}, err
	}
	return entry, nil
}

// standDown pulls back the proceed aspect (with fencing) before a vessel leaves
// the active flow through withdrawal or a missing/failed report.
func (c *Controller) standDown(cycle model.TransitCycle, vesselID uuid.UUID) {
	current, ok := c.signals.Get(vesselID)
	if !ok || current.CycleID != cycle.ID || current.Generation != cycle.Generation {
		return
	}
	if current.Aspect == model.SignalProceed {
		c.signals.Set(vesselID, cycle.ID, cycle.Generation, model.SignalHold, c.now())
	}
}

// Withdraw stands a vessel down and moves it past the cursor, unblocking the
// next waiting vessel. A failed vessel can only be cleared this way.
func (c *Controller) Withdraw(cycle model.TransitCycle, vesselID uuid.UUID, note string) (QueueEntry, error) {
	c.standDown(cycle, vesselID)
	return c.queue.Withdraw(cycle.ChamberID, vesselID, note, c.now())
}

// ReportMissing marks a vessel that missed its clearance or lost contact. The
// cursor stays on that vessel in the failed state and every following vessel is
// held until an operator withdraws it.
func (c *Controller) ReportMissing(cycle model.TransitCycle, vesselID uuid.UUID, note string) (QueueEntry, error) {
	if vesselID == uuid.Nil {
		entry, ok := c.queue.Peek(cycle.ChamberID)
		if !ok {
			return QueueEntry{}, errors.New("admission queue is empty")
		}
		vesselID = entry.Vessel.ID
	}
	c.standDown(cycle, vesselID)
	return c.queue.MarkFailed(cycle.ChamberID, vesselID, note, c.now())
}

func (c *Controller) Progress(chamberID string, planID uuid.UUID) (PlanProgress, bool) {
	return c.queue.PlanProgress(chamberID, planID)
}

func (c *Controller) Blocked(chamberID string) bool { return c.queue.Blocked(chamberID) }

func (c *Controller) OwnerAllowed(vesselID uuid.UUID, owner string, token uint64, lease model.Lease, now time.Time) bool {
	return lease.VesselID == vesselID && lease.Owner == owner &&
		lease.FencingToken == token && lease.Active(now)
}

func (c *Controller) Queue() *Queue { return c.queue }
