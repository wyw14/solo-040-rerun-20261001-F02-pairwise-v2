package cycle

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/gate"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

type fakeAdmissionGate struct{ complete bool }

func (f fakeAdmissionGate) PlanEntryComplete(_ model.TransitCycle) bool { return f.complete }

func sealSetup(t *testing.T, complete bool) (*Coordinator, model.TransitCycle, model.DeviceAck) {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC) }
	recorder := journal.NewRecorder(journal.NewMemoryStore(), now)
	gateState := gate.NewState("c1")
	commands := gate.NewCommands()
	gateController := gate.NewController(gateState, commands, recorder, now)
	coordinator := NewCoordinator(gateController, nil, fakeAdmissionGate{complete: complete}, recorder, now)

	cycleValue := model.TransitCycle{
		ID: uuid.New(), ChamberID: "c1", PlanID: uuid.New(),
		Direction: model.DirectionUpstream, Stage: model.StageAdmitting, Generation: 1,
	}
	coordinator.current["c1"] = cycleValue

	// Issue the entry-gate close command whose ack drives admitting → sealed.
	command, err := gateController.Request(context.Background(), cycleValue, model.DirectionUpstream, false)
	if err != nil {
		t.Fatalf("issue close command: %v", err)
	}
	ack := model.DeviceAck{
		CommandID: command.ID, CycleID: command.CycleID, ChamberID: "c1",
		Generation: command.Generation, Epoch: command.Epoch, Device: command.Device, Settled: true,
	}
	return coordinator, cycleValue, ack
}

func TestSealRejectedWhileVesselsOutstanding(t *testing.T) {
	coordinator, _, ack := sealSetup(t, false)
	if err := coordinator.ApplyGateAck(context.Background(), ack); err == nil {
		t.Fatal("sealing must be rejected until every plan vessel completes entry")
	}
	current, _ := coordinator.Current("c1")
	if current.Stage != model.StageAdmitting {
		t.Fatalf("cycle must remain admitting, got %s", current.Stage)
	}
}

func TestSealAcceptedOnceEntryComplete(t *testing.T) {
	coordinator, _, ack := sealSetup(t, true)
	if err := coordinator.ApplyGateAck(context.Background(), ack); err != nil {
		t.Fatalf("sealing must succeed after all vessels entered: %v", err)
	}
	current, _ := coordinator.Current("c1")
	if current.Stage != model.StageSealed {
		t.Fatalf("cycle should be sealed, got %s", current.Stage)
	}
}
