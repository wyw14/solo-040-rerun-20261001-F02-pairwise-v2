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
	Allowed  bool       `json:"allowed"`
	Reason   string     `json:"reason"`
	VesselID uuid.UUID  `json:"vessel_id,omitempty"`
	Entry    QueueEntry `json:"entry,omitempty"`
}

// ContactMonitor reports whether a cleared vessel lost contact (expired lease).
type ContactMonitor interface {
	LostContact(vesselID uuid.UUID, now time.Time) bool
}

// Controller grants admission one vessel at a time from the FIFO queue head.
type Controller struct {
	queue   *Queue
	gates   *gate.State
	signals *signal.Store
	contact ContactMonitor
	now     func() time.Time
}

func NewController(queue *Queue, gates *gate.State, signals *signal.Store, now func() time.Time) *Controller {
	if now == nil {
		now = time.Now
	}
	return &Controller{queue: queue, gates: gates, signals: signals, now: now}
}

// BindConnect attaches the lease-based lost-contact detector.
func (c *Controller) BindContact(monitor ContactMonitor) { c.contact = monitor }

// detectLostHead fails an already-cleared head whose contact expired. It is a
// no-op for vessels that were never cleared or whose lease is still active.
func (c *Controller) detectLostHead(head QueueEntry, cycle model.TransitCycle) (QueueEntry, bool) {
	if c.contact == nil || head.Status != StatusAllowed {
		return head, false
	}
	if !c.contact.LostContact(head.Vessel.ID, c.now()) {
		return head, false
	}
	entry, err := c.queue.MarkFailed(cycle.ChamberID, head.Vessel.ID, "cleared vessel lost contact (lease expired)", c.now())
	if err != nil {
		return head, false
	}
	c.signals.Set(entry.Vessel.ID, cycle.ID, cycle.Generation, model.SignalStop, c.now())
	return entry, true
}

// SweepLostHead fails the cleared FIFO head if its lease has expired. It runs
// before every admission action so an expired vessel can never be confirmed
// in and the next vessel can never be released past it.
func (c *Controller) SweepLostHead(cycle model.TransitCycle) (QueueEntry, bool) {
	if cycle.Stage != model.StagePlanned && cycle.Stage != model.StageAdmitting {
		return QueueEntry{}, false
	}
	head, blocked, ok := c.queue.Head(cycle.ChamberID)
	if !ok || blocked {
		return QueueEntry{}, false
	}
	return c.detectLostHead(head, cycle)
}

// headForCycle returns the active FIFO head, rejecting cross-plan or blocked heads.
func (c *Controller) headForCycle(cycle model.TransitCycle) (QueueEntry, error) {
	if cycle.Stage != model.StagePlanned && cycle.Stage != model.StageAdmitting {
		return QueueEntry{}, errors.New("lock entry is already sealed for this transit")
	}
	head, blocked, ok := c.queue.Head(cycle.ChamberID)
	if !ok {
		return QueueEntry{}, errors.New("queue empty")
	}
	if blocked {
		return head, errors.New("ahead vessel failed or lost contact; later vessels cannot be released")
	}
	if head.PlanID != cycle.PlanID {
		return head, errors.New("head vessel belongs to another plan")
	}
	return head, nil
}

// entryIsSafe confirms the opposite gate stays sealed while vessels enter.
func (c *Controller) entryIsSafe(cycle model.TransitCycle) bool {
	opposite := c.gates.Position(cycle.ChamberID, cycle.Direction.Opposite())
	return opposite.Position.Sealed()
}

// Allow clears the current head vessel to enter. Later vessels are never
// released: only the FIFO head can receive a proceed signal.
func (c *Controller) Allow(ctx context.Context, cycle model.TransitCycle) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	head, err := c.headForCycle(cycle)
	if err != nil {
		return Decision{VesselID: head.Vessel.ID, Reason: err.Error()}, nil
	}
	if !c.entryIsSafe(cycle) {
		return Decision{VesselID: head.Vessel.ID, Reason: "opposite gate is not sealed"}, nil
	}
	entry, err := c.queue.AllowHead(cycle.ChamberID, cycle.PlanID, head.Vessel.ID, c.now())
	if err != nil {
		return Decision{VesselID: head.Vessel.ID, Reason: err.Error()}, nil
	}
	state := c.signals.Set(entry.Vessel.ID, cycle.ID, cycle.Generation, model.SignalProceed, c.now())
	if state.Aspect != model.SignalProceed {
		return Decision{}, errors.New("failed to commit admission signal")
	}
	return Decision{Allowed: true, VesselID: entry.Vessel.ID, Entry: entry, Reason: "sealed chamber ready"}, nil
}

// ConfirmEntry records that the allowed head vessel physically entered.
func (c *Controller) ConfirmEntry(ctx context.Context, cycle model.TransitCycle, vesselID uuid.UUID) (QueueEntry, error) {
	if err := ctx.Err(); err != nil {
		return QueueEntry{}, err
	}
	head, err := c.headForCycle(cycle)
	if err != nil {
		return QueueEntry{}, err
	}
	if vesselID != uuid.Nil && vesselID != head.Vessel.ID {
		return QueueEntry{}, errors.New("only the FIFO head vessel can be confirmed")
	}
	if head.Status != StatusAllowed {
		return QueueEntry{}, errors.New("vessel has not been cleared to enter; allow it first")
	}
	return c.queue.MarkEntered(cycle.ChamberID, head.Vessel.ID, c.now())
}

// Withdraw records the head vessel backing out before completing entry.
func (c *Controller) Withdraw(ctx context.Context, cycle model.TransitCycle, vesselID uuid.UUID, reason string) (QueueEntry, error) {
	if err := ctx.Err(); err != nil {
		return QueueEntry{}, err
	}
	head, err := c.headForCycle(cycle)
	if err != nil {
		return QueueEntry{}, err
	}
	if vesselID != uuid.Nil && vesselID != head.Vessel.ID {
		return QueueEntry{}, errors.New("only the FIFO head vessel can withdraw")
	}
	entry, err := c.queue.MarkWithdrawn(cycle.ChamberID, head.Vessel.ID, reason, c.now())
	if err != nil {
		return QueueEntry{}, err
	}
	c.signals.Set(entry.Vessel.ID, cycle.ID, cycle.Generation, model.SignalStop, c.now())
	return entry, nil
}

// MarkLost records that the cleared head vessel failed or lost contact. The
// vessel stays at the head in failed status and blocks every later vessel.
func (c *Controller) MarkLost(ctx context.Context, cycle model.TransitCycle, vesselID uuid.UUID, reason string) (QueueEntry, error) {
	if err := ctx.Err(); err != nil {
		return QueueEntry{}, err
	}
	head, err := c.headForCycle(cycle)
	if err != nil {
		return QueueEntry{}, err
	}
	if vesselID != uuid.Nil && vesselID != head.Vessel.ID {
		return QueueEntry{}, errors.New("only the FIFO head vessel can be marked lost")
	}
	if head.Status != StatusAllowed {
		return QueueEntry{}, errors.New("only a vessel already cleared to enter can be marked lost")
	}
	if reason == "" {
		reason = "vessel lost contact during entry"
	}
	entry, err := c.queue.MarkFailed(cycle.ChamberID, head.Vessel.ID, reason, c.now())
	if err != nil {
		return QueueEntry{}, err
	}
	c.signals.Set(entry.Vessel.ID, cycle.ID, cycle.Generation, model.SignalStop, c.now())
	return entry, nil
}

// PlanEntryComplete reports whether every vessel of the cycle's plan has
// completed entry, which is the precondition for sealing the chamber.
func (c *Controller) PlanEntryComplete(cycle model.TransitCycle) bool {
	return c.queue.PlanEntryComplete(cycle.ChamberID, cycle.PlanID)
}

func (c *Controller) OwnerAllowed(vesselID uuid.UUID, owner string, token uint64, lease model.Lease, now time.Time) bool {
	return lease.VesselID == vesselID && lease.Owner == owner &&
		lease.FencingToken == token && lease.Active(now)
}

func (c *Controller) Queue() *Queue { return c.queue }
