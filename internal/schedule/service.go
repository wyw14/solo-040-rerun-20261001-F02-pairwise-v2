package schedule

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/signal"
)

// Service exposes planning and signal-publication workflows to the API.
type Service struct {
	planner   *Planner
	publisher *signal.Publisher
}

func NewService(planner *Planner, publisher *signal.Publisher) *Service {
	return &Service{planner: planner, publisher: publisher}
}

func (s *Service) Create(ctx context.Context, chamberID string, direction model.Direction, vessels []model.Vessel) (model.ConvoyPlan, error) {
	return s.planner.Create(ctx, chamberID, direction, vessels)
}

func (s *Service) Publish(ctx context.Context, planID uuid.UUID, cycle model.TransitCycle) (signal.Publication, error) {
	plan, ok := s.planner.Get(planID)
	if !ok || plan.Cancelled {
		return signal.Publication{}, errors.New("active convoy plan not found")
	}
	if plan.ChamberID != cycle.ChamberID || plan.ID != cycle.PlanID {
		return signal.Publication{}, errors.New("plan and transit do not match")
	}
	// Only the single vessel at the queue head may receive a proceed aspect;
	// vessels behind it must stay on stop until the head resolves.
	head, ok := s.planner.HeadVessel(plan)
	if !ok {
		return signal.Publication{}, errors.New("convoy is not at the chamber queue head")
	}
	return s.publisher.Publish(ctx, cycle, []model.Vessel{head}, model.SignalProceed)
}

func (s *Service) Cancel(ctx context.Context, planID uuid.UUID, note string, publication signal.Publication) (Cancellation, error) {
	return s.planner.Cancel(ctx, planID, note, s.publisher, publication)
}

func (s *Service) Active(chamberID string) []model.ConvoyPlan {
	return s.planner.Active(chamberID)
}
