package cycle

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/admission"
	"github.com/wyw14/cry-96/internal/model"
)

// Service drives an operator-visible transit through its safe boundaries and
// exposes the per-vessel lock-entry confirmation flow.
type Service struct {
	coordinator *Coordinator
	admission   *admission.Service
}

func NewService(coordinator *Coordinator, admissionService *admission.Service) *Service {
	return &Service{coordinator: coordinator, admission: admissionService}
}

// Start opens a transit generation. No vessel is released yet: each vessel is
// cleared individually through AllowNext/ConfirmEntry.
func (s *Service) Start(ctx context.Context, plan model.ConvoyPlan) (model.TransitCycle, error) {
	return s.coordinator.Start(ctx, plan)
}

func (s *Service) Current(chamberID string) (model.TransitCycle, bool) {
	return s.coordinator.Current(chamberID)
}

func (s *Service) activeCycle(chamberID string) (model.TransitCycle, error) {
	cycleValue, ok := s.coordinator.Current(chamberID)
	if !ok || cycleValue.Stage.Terminal() {
		return model.TransitCycle{}, errors.New("active transit not found")
	}
	return cycleValue, nil
}

// AllowNext clears the FIFO head vessel to enter the chamber.
func (s *Service) AllowNext(ctx context.Context, chamberID string) (admission.Decision, error) {
	cycleValue, err := s.activeCycle(chamberID)
	if err != nil {
		return admission.Decision{}, err
	}
	return s.admission.Allow(ctx, cycleValue)
}

// ConfirmEntry records that the cleared head vessel physically entered.
func (s *Service) ConfirmEntry(ctx context.Context, chamberID string, vesselID uuid.UUID) (admission.QueueEntry, error) {
	cycleValue, err := s.activeCycle(chamberID)
	if err != nil {
		return admission.QueueEntry{}, err
	}
	return s.admission.ConfirmEntry(ctx, cycleValue, vesselID)
}

// Withdraw records the head vessel backing out before completing entry.
func (s *Service) Withdraw(ctx context.Context, chamberID string, vesselID uuid.UUID, reason string) (admission.QueueEntry, error) {
	cycleValue, err := s.activeCycle(chamberID)
	if err != nil {
		return admission.QueueEntry{}, err
	}
	return s.admission.Withdraw(ctx, cycleValue, vesselID, reason)
}

// MarkLost records head-vessel failure or lost contact, blocking later vessels.
func (s *Service) MarkLost(ctx context.Context, chamberID string, vesselID uuid.UUID, reason string) (admission.QueueEntry, error) {
	cycleValue, err := s.activeCycle(chamberID)
	if err != nil {
		return admission.QueueEntry{}, err
	}
	return s.admission.MarkLost(ctx, cycleValue, vesselID, reason)
}

// PlanEntryComplete reports whether all plan vessels finished lock entry.
func (s *Service) PlanEntryComplete(chamberID string) bool {
	cycleValue, ok := s.coordinator.Current(chamberID)
	if !ok {
		return false
	}
	return s.admission.PlanEntryComplete(cycleValue)
}
