package admission

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/gate"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/signal"
)

type fakeContact struct{ lost map[uuid.UUID]bool }

func (f fakeContact) LostContact(id uuid.UUID, _ time.Time) bool { return f.lost[id] }

func testVessels(t *testing.T) []model.Vessel {
	t.Helper()
	names := []string{"River Star", "Delta Dawn", "Harbor Light"}
	vessels := make([]model.Vessel, 0, len(names))
	for _, name := range names {
		vessel, err := model.NewVessel(name, name[:2], 50)
		if err != nil {
			t.Fatalf("vessel: %v", err)
		}
		vessels = append(vessels, vessel)
	}
	return vessels
}

func newHarness(t *testing.T, contact ContactMonitor) (*Service, *gate.State, model.ConvoyPlan, model.TransitCycle, func() time.Time) {
	t.Helper()
	base := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	clock := base
	now := func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}
	gates := gate.NewState("c1")
	signals := signal.NewStore()
	queue := NewQueue()
	controller := NewController(queue, gates, signals, now)
	if contact != nil {
		controller.BindContact(contact)
	}
	recorder := journal.NewRecorder(journal.NewMemoryStore(), now)
	service := NewService(controller, recorder, now)

	plan, err := model.NewConvoyPlan("c1", model.DirectionUpstream, testVessels(t), now())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := service.Enqueue(context.Background(), plan); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	cycle := model.TransitCycle{
		ID: uuid.New(), ChamberID: "c1", PlanID: plan.ID,
		Direction: model.DirectionUpstream, Stage: model.StageAdmitting, Generation: 1,
	}
	return service, gates, plan, cycle, now
}

func TestAllowReleasesOnlyFIFOHead(t *testing.T) {
	service, _, plan, cycle, _ := newHarness(t, nil)
	ctx := context.Background()

	first, err := service.Allow(ctx, cycle)
	if err != nil || !first.Allowed {
		t.Fatalf("first allow: %+v err=%v", first, err)
	}
	if first.VesselID != plan.Vessels[0].ID {
		t.Fatalf("expected first vessel, got %s", first.VesselID)
	}

	// A second Allow while the head is still outstanding must not release vessel two.
	again, err := service.Allow(ctx, cycle)
	if err != nil || !again.Allowed || again.VesselID != plan.Vessels[0].ID {
		t.Fatalf("expected head to remain vessel one, got %+v err=%v", again, err)
	}

	// Confirming a later vessel directly is rejected.
	if _, err := service.ConfirmEntry(ctx, cycle, plan.Vessels[1].ID); err == nil {
		t.Fatal("later vessel must not be confirmable before the head")
	}

	if _, err := service.ConfirmEntry(ctx, cycle, uuid.Nil); err != nil {
		t.Fatalf("confirm head: %v", err)
	}

	// Vessel two is now the head and can be cleared.
	second, err := service.Allow(ctx, cycle)
	if err != nil || !second.Allowed || second.VesselID != plan.Vessels[1].ID {
		t.Fatalf("expected vessel two after head entered, got %+v err=%v", second, err)
	}
}

func TestLostHeadBlocksLaterVessels(t *testing.T) {
	contact := fakeContact{lost: map[uuid.UUID]bool{}}
	service, _, plan, cycle, _ := newHarness(t, contact)
	ctx := context.Background()

	first, err := service.Allow(ctx, cycle)
	if err != nil || !first.Allowed {
		t.Fatalf("allow first: %v", err)
	}

	// The cleared vessel loses contact (its lease expires).
	contact.lost[plan.Vessels[0].ID] = true

	if _, err := service.ConfirmEntry(ctx, cycle, uuid.Nil); err == nil {
		t.Fatal("lost head must not be confirmed into the chamber")
	}
	if _, blocked := service.BlockingVessel("c1"); !blocked {
		t.Fatal("failed head should be reported as the blocking vessel")
	}

	// No later vessel can ever be released past the failed head.
	next, err := service.Allow(ctx, cycle)
	if err != nil {
		t.Fatalf("allow after loss returned error: %v", err)
	}
	if next.Allowed {
		t.Fatal("later vessel must not be released while head is lost")
	}
	if _, err := service.ConfirmEntry(ctx, cycle, plan.Vessels[1].ID); err == nil {
		t.Fatal("later vessel entry must stay blocked")
	}
	if service.PlanEntryComplete(cycle) {
		t.Fatal("plan cannot be entry-complete while a vessel is failed")
	}
}

func TestManualMarkLostBlocksQueue(t *testing.T) {
	service, _, _, cycle, _ := newHarness(t, nil)
	ctx := context.Background()
	if _, err := service.Allow(ctx, cycle); err != nil {
		t.Fatalf("allow: %v", err)
	}
	lost, err := service.MarkLost(ctx, cycle, uuid.Nil, "radio silence")
	if err != nil {
		t.Fatalf("mark lost: %v", err)
	}
	if lost.Status != StatusFailed || lost.Reason != "radio silence" {
		t.Fatalf("unexpected lost entry: %+v", lost)
	}
	if next, err := service.Allow(ctx, cycle); err != nil || next.Allowed {
		t.Fatalf("queue must stay blocked after manual loss, got %+v err=%v", next, err)
	}
}

func TestWithdrawThenComplete(t *testing.T) {
	service, _, plan, cycle, _ := newHarness(t, nil)
	ctx := context.Background()

	// First vessel withdraws while still standing by.
	if _, err := service.Withdraw(ctx, cycle, plan.Vessels[0].ID, "cargo issue"); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	// Remaining vessels enter normally.
	if _, err := service.Allow(ctx, cycle); err != nil {
		t.Fatalf("allow vessel two: %v", err)
	}
	if _, err := service.ConfirmEntry(ctx, cycle, uuid.Nil); err != nil {
		t.Fatalf("confirm vessel two: %v", err)
	}
	if _, err := service.Allow(ctx, cycle); err != nil {
		t.Fatalf("allow vessel three: %v", err)
	}
	if _, err := service.ConfirmEntry(ctx, cycle, uuid.Nil); err != nil {
		t.Fatalf("confirm vessel three: %v", err)
	}
	if !service.PlanEntryComplete(cycle) {
		t.Fatal("plan with one withdrawn and two entered must be entry-complete")
	}
}

func TestOppositeGateMustStaySealed(t *testing.T) {
	service, gates, _, cycle, _ := newHarness(t, nil)
	// Open the opposite (downstream) gate: entry is unsafe.
	if err := gates.Observe("c1", model.DirectionDownstream, model.GateOpen, time.Now()); err != nil {
		t.Fatalf("observe: %v", err)
	}
	decision, err := service.Allow(context.Background(), cycle)
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	if decision.Allowed {
		t.Fatal("vessel must not be cleared while the opposite gate is open")
	}
}
