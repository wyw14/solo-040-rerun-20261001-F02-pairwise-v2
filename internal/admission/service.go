package admission

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

// Update is the journaled view of one vessel's admission-state transition.
type Update struct {
	Entry  QueueEntry         `json:"entry"`
	Aspect model.SignalAspect `json:"aspect,omitempty"`
}

// Service records queue changes and drives per-vessel admission decisions.
type Service struct {
	controller *Controller
	recorder   *journal.Recorder
	now        func() time.Time
}

func NewService(controller *Controller, recorder *journal.Recorder, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{controller: controller, recorder: recorder, now: now}
}

func (s *Service) Enqueue(ctx context.Context, plan model.ConvoyPlan) error {
	if err := s.controller.queue.Add(plan.ChamberID, plan.ID, plan.Vessels, s.now()); err != nil {
		return err
	}
	_, err := s.recorder.Record(ctx, plan.ChamberID, model.EventPlanCreated, plan.ID, 0, map[string]any{
		"plan_id": plan.ID, "queued": len(plan.Vessels), "at": s.now().UTC(),
	})
	return err
}

func (s *Service) recordUpdate(ctx context.Context, cycle model.TransitCycle, entry QueueEntry, aspect model.SignalAspect) error {
	_, err := s.recorder.Record(ctx, cycle.ChamberID, model.EventAdmissionUpdated, cycle.ID, cycle.Generation, Update{
		Entry: entry, Aspect: aspect,
	})
	return err
}

// sweepFailures turns an expired-lease head into a recorded failed entry.
func (s *Service) sweepFailures(ctx context.Context, cycle model.TransitCycle) (QueueEntry, bool, error) {
	entry, failed := s.controller.SweepLostHead(cycle)
	if !failed {
		return QueueEntry{}, false, nil
	}
	if err := s.recordUpdate(ctx, cycle, entry, model.SignalStop); err != nil {
		return entry, true, err
	}
	return entry, true, nil
}

// Allow clears the FIFO head vessel to enter the chamber.
func (s *Service) Allow(ctx context.Context, cycle model.TransitCycle) (Decision, error) {
	if failed, blocked, err := s.sweepFailures(ctx, cycle); err != nil {
		return Decision{}, err
	} else if blocked {
		return Decision{VesselID: failed.Vessel.ID, Reason: "ahead vessel lost contact; later vessels cannot be released"}, nil
	}
	decision, err := s.controller.Allow(ctx, cycle)
	if err != nil || !decision.Allowed {
		return decision, err
	}
	if err := s.recordUpdate(ctx, cycle, decision.Entry, model.SignalProceed); err != nil {
		return decision, err
	}
	return decision, nil
}

// ConfirmEntry records the head vessel physically entering the chamber.
func (s *Service) ConfirmEntry(ctx context.Context, cycle model.TransitCycle, vesselID uuid.UUID) (QueueEntry, error) {
	if failed, blocked, err := s.sweepFailures(ctx, cycle); err != nil {
		return QueueEntry{}, err
	} else if blocked {
		return QueueEntry{}, errors.New("ahead vessel lost contact; later vessels cannot be released: " + failed.Vessel.ID.String())
	}
	entry, err := s.controller.ConfirmEntry(ctx, cycle, vesselID)
	if err != nil {
		return QueueEntry{}, err
	}
	err = s.recordUpdate(ctx, cycle, entry, model.SignalProceed)
	return entry, err
}

// Withdraw records the head vessel backing out before completing entry.
func (s *Service) Withdraw(ctx context.Context, cycle model.TransitCycle, vesselID uuid.UUID, reason string) (QueueEntry, error) {
	entry, err := s.controller.Withdraw(ctx, cycle, vesselID, reason)
	if err != nil {
		return QueueEntry{}, err
	}
	err = s.recordUpdate(ctx, cycle, entry, model.SignalStop)
	return entry, err
}

// MarkLost records head-vessel failure or lost contact and blocks the queue.
func (s *Service) MarkLost(ctx context.Context, cycle model.TransitCycle, vesselID uuid.UUID, reason string) (QueueEntry, error) {
	entry, err := s.controller.MarkLost(ctx, cycle, vesselID, reason)
	if err != nil {
		return QueueEntry{}, err
	}
	err = s.recordUpdate(ctx, cycle, entry, model.SignalStop)
	return entry, err
}

// PlanEntryComplete reports whether all vessels of the cycle plan finished entry.
func (s *Service) PlanEntryComplete(cycle model.TransitCycle) bool {
	return s.controller.PlanEntryComplete(cycle)
}

// BlockingVessel returns the failed head vessel that blocks admission, if any.
func (s *Service) BlockingVessel(chamberID string) (QueueEntry, bool) {
	entry, blocked, ok := s.controller.queue.Head(chamberID)
	return entry, ok && blocked
}

func (s *Service) RemovePlan(chamberID string, plan model.ConvoyPlan) []QueueEntry {
	return s.controller.queue.RemovePlan(chamberID, plan.ID)
}

func (s *Service) Restore(chamberID string, entries []QueueEntry) {
	s.controller.queue.Restore(chamberID, entries, s.now())
}

func (s *Service) List(chamberID string) []QueueEntry {
	return s.controller.queue.List(chamberID)
}
