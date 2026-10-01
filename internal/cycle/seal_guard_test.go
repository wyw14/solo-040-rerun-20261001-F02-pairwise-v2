package cycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wyw14/cry-96/internal/admission"
	"github.com/wyw14/cry-96/internal/gate"
	"github.com/wyw14/cry-96/internal/hydraulics"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/signal"
)

type sealFixture struct {
	t         *testing.T
	ctx       context.Context
	chamberID string
	plan      model.ConvoyPlan
	cycles    *Coordinator
	admission *admission.Service
	gates     *gate.Controller
	now       func() time.Time
}

func newSealFixture(t *testing.T, vesselCount int) sealFixture {
	t.Helper()
	clock := time.Unix(1000, 0)
	now := func() time.Time { return clock }
	recorder := journal.NewRecorder(journal.NewMemoryStore(), now)
	chamberID := "chamber-1"
	gateState := gate.NewState(chamberID)
	gateCommands := gate.NewCommands()
	gateController := gate.NewController(gateState, gateCommands, recorder, now)
	ramp := hydraulics.NewRamp(time.Millisecond, now)
	hydraulicsController := hydraulics.NewController(gateCommands, recorder, ramp, now)
	queue := admission.NewQueue()
	signals := signal.NewStore()
	controller := admission.NewController(queue, gateState, signals, now)
	admissionService := admission.NewService(controller, recorder, now)
	coordinator := NewCoordinator(gateController, hydraulicsController, admissionService, recorder, now)

	vessels := make([]model.Vessel, 0, vesselCount)
	for i := 0; i < vesselCount; i++ {
		vessel, err := model.NewVessel("River Star", "RS", 40)
		if err != nil {
			t.Fatalf("vessel: %v", err)
		}
		vessels = append(vessels, vessel)
	}
	plan, err := model.NewConvoyPlan(chamberID, model.DirectionUpstream, vessels, now())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	ctx := context.Background()
	if err := admissionService.Enqueue(ctx, plan); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := coordinator.Start(ctx, plan); err != nil {
		t.Fatalf("start: %v", err)
	}
	return sealFixture{
		t: t, ctx: ctx, chamberID: chamberID, plan: plan,
		cycles: coordinator, admission: admissionService, gates: gateController, now: now,
	}
}

func (f sealFixture) current() model.TransitCycle {
	cycleValue, _ := f.cycles.Current(f.chamberID)
	return cycleValue
}

// settleGate issues a gate command and acks it, driving one stage transition.
func (f sealFixture) settleGate(open bool) {
	command, err := f.gates.Request(f.ctx, f.current(), model.DirectionUpstream, open)
	if err != nil {
		f.t.Fatalf("request gate open=%v: %v", open, err)
	}
	ack := model.DeviceAck{
		CommandID: command.ID, CycleID: command.CycleID, ChamberID: command.ChamberID,
		Generation: command.Generation, Epoch: command.Epoch, Device: command.Device,
		Settled: true, ObservedAt: f.now(),
	}
	if err := f.cycles.ApplyGateAck(f.ctx, ack); err != nil {
		f.t.Fatalf("ack gate open=%v: %v", open, err)
	}
}

// enterHead clears the current head and confirms it entered.
func (f sealFixture) enterHead() {
	cycleValue := f.current()
	decision, err := f.admission.Admit(f.ctx, cycleValue)
	if err != nil {
		f.t.Fatalf("admit: %v", err)
	}
	if !decision.Allowed {
		f.t.Fatalf("admit not allowed: %s", decision.Reason)
	}
	if _, err := f.admission.ConfirmEntry(f.ctx, cycleValue, decision.VesselID); err != nil {
		f.t.Fatalf("confirm entry: %v", err)
	}
}

// requestSeal issues the upstream close and returns the ack error (if any).
func (f sealFixture) requestSeal() error {
	command, err := f.gates.Request(f.ctx, f.current(), model.DirectionUpstream, false)
	if err != nil {
		return err
	}
	ack := model.DeviceAck{
		CommandID: command.ID, CycleID: command.CycleID, ChamberID: command.ChamberID,
		Generation: command.Generation, Epoch: command.Epoch, Device: command.Device,
		Settled: true, ObservedAt: f.now(),
	}
	return f.cycles.ApplyGateAck(f.ctx, ack)
}

func TestSealBlockedUntilAllVesselsEnter(t *testing.T) {
	f := newSealFixture(t, 2)

	// Planned -> Admitting (entry gate opens).
	f.settleGate(true)
	if stage := f.current().Stage; stage != model.StageAdmitting {
		t.Fatalf("stage = %s, want admitting", stage)
	}

	// Only the first vessel enters; sealing must be refused.
	f.enterHead()
	err := f.requestSeal()
	if err == nil {
		t.Fatal("seal accepted with a vessel still outside the chamber")
	}
	if !strings.Contains(err.Error(), "cannot seal") {
		t.Fatalf("seal error = %q, want a cannot-seal reason", err.Error())
	}
	if stage := f.current().Stage; stage != model.StageAdmitting {
		t.Fatalf("stage = %s, want still admitting after blocked seal", stage)
	}
}

func TestSealAllowedAfterEveryVesselEnters(t *testing.T) {
	f := newSealFixture(t, 2)
	f.settleGate(true)
	f.enterHead()

	// First vessel resolved; second vessel now owns the cursor and is cleared.
	f.enterHead()
	progress, err := f.admission.Progress(f.chamberID, f.plan.ID)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if !progress.Completed {
		t.Fatalf("progress = %+v, want completed", progress)
	}
	if err := f.requestSeal(); err != nil {
		t.Fatalf("seal after full entry: %v", err)
	}
	if stage := f.current().Stage; stage != model.StageSealed {
		t.Fatalf("stage = %s, want sealed", stage)
	}
}

func TestSealBlockedByFailedVesselThenWithdraw(t *testing.T) {
	f := newSealFixture(t, 2)
	f.settleGate(true)
	f.enterHead()

	cycleValue := f.current()
	decision, err := f.admission.Admit(f.ctx, cycleValue)
	if err != nil || !decision.Allowed {
		t.Fatalf("admit second vessel: %v allowed=%v", err, decision.Allowed)
	}
	if _, err := f.admission.ReportMissing(f.ctx, cycleValue, decision.VesselID, "lost contact"); err != nil {
		t.Fatalf("report missing: %v", err)
	}
	if err := f.requestSeal(); err == nil {
		t.Fatal("seal accepted with a vessel that lost contact")
	}
	// Standing the vessel down resolves the convoy and permits sealing.
	if _, err := f.admission.Withdraw(f.ctx, cycleValue, decision.VesselID, "stood down"); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if err := f.requestSeal(); err != nil {
		t.Fatalf("seal after withdraw: %v", err)
	}
}
