package admission

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

// Service records queue changes and delegates safe release decisions.
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
	if err := s.controller.queue.Add(plan.ChamberID, plan.ID, plan.Vessels); err != nil {
		return err
	}
	_, err := s.recorder.Record(ctx, plan.ChamberID, model.EventPlanCreated, plan.ID, 0, map[string]any{
		"plan_id": plan.ID, "queued": len(plan.Vessels), "at": s.now().UTC(),
	})
	return err
}

func (s *Service) Admit(ctx context.Context, cycle model.TransitCycle) (Decision, error) {
	decision, err := s.controller.Evaluate(ctx, cycle)
	if err != nil || !decision.Allowed {
		return decision, err
	}
	_, err = s.recorder.Record(ctx, cycle.ChamberID, model.EventSignalChanged, cycle.ID, cycle.Generation, decision)
	if err != nil {
		return decision, err
	}
	_, err = s.recorder.Record(ctx, cycle.ChamberID, model.EventAdmissionAdvanced, cycle.ID, cycle.Generation, map[string]any{
		"vessel_id": decision.VesselID, "state": model.AdmissionAdmitted,
		"at": s.now().UTC(),
	})
	return decision, err
}

// AdmitNext clears the next standing-by vessel once the previous one is fully
// resolved. It is a no-op decision when there is nothing left to release.
func (s *Service) AdmitNext(ctx context.Context, cycle model.TransitCycle) (Decision, error) {
	return s.Admit(ctx, cycle)
}

// ConfirmEntry records that the cleared vessel actually entered the chamber.
func (s *Service) ConfirmEntry(ctx context.Context, cycle model.TransitCycle, vesselID uuid.UUID) (QueueEntry, error) {
	entry, err := s.controller.ConfirmEntry(cycle, vesselID)
	if err != nil {
		return QueueEntry{}, err
	}
	if err := s.recordProgress(ctx, cycle, entry, ""); err != nil {
		return QueueEntry{}, err
	}
	return entry, nil
}

// Withdraw stands a vessel down and releases the cursor to the next vessel.
func (s *Service) Withdraw(ctx context.Context, cycle model.TransitCycle, vesselID uuid.UUID, note string) (QueueEntry, error) {
	entry, err := s.controller.Withdraw(cycle, vesselID, note)
	if err != nil {
		return QueueEntry{}, err
	}
	if err := s.recordProgress(ctx, cycle, entry, note); err != nil {
		return QueueEntry{}, err
	}
	return entry, nil
}

// ReportMissing marks a failed/uncontactable vessel and blocks the convoy until
// it is explicitly withdrawn.
func (s *Service) ReportMissing(ctx context.Context, cycle model.TransitCycle, vesselID uuid.UUID, note string) (QueueEntry, error) {
	entry, err := s.controller.ReportMissing(cycle, vesselID, note)
	if err != nil {
		return QueueEntry{}, err
	}
	if err := s.recordProgress(ctx, cycle, entry, note); err != nil {
		return QueueEntry{}, err
	}
	return entry, nil
}

func (s *Service) recordProgress(ctx context.Context, cycle model.TransitCycle, entry QueueEntry, note string) error {
	payload := map[string]any{
		"plan_id": entry.PlanID, "vessel_id": entry.Vessel.ID,
		"state": entry.State, "at": s.now().UTC(),
	}
	if note != "" {
		payload["note"] = note
	}
	if progress, ok := s.controller.Progress(cycle.ChamberID, entry.PlanID); ok {
		payload["progress"] = progress
	}
	_, err := s.recorder.Record(ctx, cycle.ChamberID, model.EventAdmissionAdvanced, cycle.ID, cycle.Generation, payload)
	return err
}

// Progress returns the confirmation tally for a plan in a chamber.
func (s *Service) Progress(chamberID string, planID uuid.UUID) (PlanProgress, error) {
	progress, ok := s.controller.Progress(chamberID, planID)
	if !ok {
		return PlanProgress{}, errors.New("plan has no vessels in the chamber queue")
	}
	return progress, nil
}

// ReadyToSeal reports whether the convoy can be locked in: every vessel reached
// a terminal admission outcome (entered or explicitly withdrawn) and no vessel
// is still standing by, cleared, or blocked on a failure.
func (s *Service) ReadyToSeal(chamberID string, planID uuid.UUID) bool {
	progress, ok := s.controller.Progress(chamberID, planID)
	return ok && progress.Resolved
}

func (s *Service) Blocked(chamberID string) bool { return s.controller.Blocked(chamberID) }

// Head returns the vessel currently owning the admission cursor for a chamber.
func (s *Service) Head(chamberID string) (QueueEntry, bool) {
	return s.controller.queue.Peek(chamberID)
}

func (s *Service) RemovePlan(chamberID string, plan model.ConvoyPlan) []QueueEntry {
	return s.controller.queue.RemovePlan(chamberID, plan.ID)
}

func (s *Service) Restore(chamberID string, entries []QueueEntry) {
	s.controller.queue.Restore(chamberID, entries)
}

func (s *Service) List(chamberID string) []QueueEntry {
	return s.controller.queue.List(chamberID)
}
