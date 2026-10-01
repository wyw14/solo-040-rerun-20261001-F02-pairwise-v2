package admission

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

func testVessels(n int) []model.Vessel {
	vessels := make([]model.Vessel, n)
	for i := range vessels {
		vessel, _ := model.NewVessel("Vessel", "CS", 10)
		// Force stable distinct identities even if construction rules change.
		vessel.ID = uuid.New()
		vessels[i] = vessel
	}
	return vessels
}

func TestQueueGrantsOneVesselAtATime(t *testing.T) {
	q := NewQueue()
	chamberID := "chamber-1"
	planID := uuid.New()
	vessels := testVessels(3)
	if err := q.Add(chamberID, planID, vessels); err != nil {
		t.Fatalf("add: %v", err)
	}

	now := time.Unix(100, 0)
	head, ok := q.Peek(chamberID)
	if !ok || head.Vessel.ID != vessels[0].ID {
		t.Fatalf("peek = %v, want first vessel", head)
	}

	// A vessel behind the head can never be granted.
	if _, err := q.Grant(chamberID, vessels[1].ID, now); err == nil {
		t.Fatal("granting second vessel before the head must fail")
	}

	if _, err := q.Grant(chamberID, vessels[0].ID, now); err != nil {
		t.Fatalf("grant head: %v", err)
	}
	// The head stays the cursor while admitted; it is not silently released.
	head, _ = q.Peek(chamberID)
	if head.State != model.AdmissionAdmitted {
		t.Fatalf("head state = %s, want admitted", head.State)
	}
	if _, err := q.MarkEntered(chamberID, vessels[0].ID, now); err != nil {
		t.Fatalf("mark entered: %v", err)
	}

	head, ok = q.Peek(chamberID)
	if !ok || head.Vessel.ID != vessels[1].ID {
		t.Fatalf("cursor did not advance to second vessel: %+v ok=%v", head, ok)
	}
	if head.State != model.AdmissionWaiting {
		t.Fatalf("second vessel state = %s, want waiting", head.State)
	}
}

func TestFailedVesselBlocksFollowingVessels(t *testing.T) {
	q := NewQueue()
	chamberID := "chamber-1"
	planID := uuid.New()
	vessels := testVessels(3)
	if err := q.Add(chamberID, planID, vessels); err != nil {
		t.Fatalf("add: %v", err)
	}
	now := time.Unix(100, 0)

	if _, err := q.Grant(chamberID, vessels[0].ID, now); err != nil {
		t.Fatalf("grant: %v", err)
	}
	failed, err := q.MarkFailed(chamberID, vessels[0].ID, "lost contact", now)
	if err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if failed.State != model.AdmissionFailed {
		t.Fatalf("state = %s, want failed", failed.State)
	}
	if !q.Blocked(chamberID) {
		t.Fatal("queue must report blocked")
	}

	// The failed vessel keeps the cursor: the next vessel must not move.
	head, _ := q.Peek(chamberID)
	if head.Vessel.ID != vessels[0].ID {
		t.Fatal("failed vessel must keep the cursor")
	}
	if _, err := q.Grant(chamberID, vessels[1].ID, now); err == nil {
		t.Fatal("following vessel must not be granted past a failure")
	}

	// Explicit withdrawal of the failed vessel unblocks the convoy.
	if _, err := q.Withdraw(chamberID, vessels[0].ID, "stood down", now); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	head, _ = q.Peek(chamberID)
	if head.Vessel.ID != vessels[1].ID {
		t.Fatal("cursor must advance after standing the failed vessel down")
	}
}

func TestPlanProgressCompletion(t *testing.T) {
	q := NewQueue()
	chamberID := "chamber-1"
	planID := uuid.New()
	vessels := testVessels(2)
	if err := q.Add(chamberID, planID, vessels); err != nil {
		t.Fatalf("add: %v", err)
	}
	now := time.Unix(100, 0)

	progress, ok := q.PlanProgress(chamberID, planID)
	if !ok || progress.Total != 2 || progress.Waiting != 2 || progress.Resolved {
		t.Fatalf("unexpected initial progress: %+v ok=%v", progress, ok)
	}

	if _, err := q.Grant(chamberID, vessels[0].ID, now); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := q.MarkEntered(chamberID, vessels[0].ID, now); err != nil {
		t.Fatalf("entered: %v", err)
	}
	progress, _ = q.PlanProgress(chamberID, planID)
	if progress.Resolved || progress.Completed {
		t.Fatalf("plan must not be resolved with vessel outstanding: %+v", progress)
	}

	if _, err := q.Grant(chamberID, vessels[1].ID, now); err != nil {
		t.Fatalf("grant second: %v", err)
	}
	if _, err := q.MarkFailed(chamberID, vessels[1].ID, "no contact", now); err != nil {
		t.Fatalf("failed: %v", err)
	}
	progress, _ = q.PlanProgress(chamberID, planID)
	if progress.Failed != 1 || progress.BlockedVessel == nil || progress.Resolved {
		t.Fatalf("failure must block completion: %+v", progress)
	}

	if _, err := q.Withdraw(chamberID, vessels[1].ID, "stood down", now); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	progress, _ = q.PlanProgress(chamberID, planID)
	if !progress.Resolved {
		t.Fatalf("plan must resolve once failure is stood down: %+v", progress)
	}
	if progress.Completed {
		t.Fatal("a withdrawn vessel means the convoy resolved but did not fully enter")
	}
}

func TestWithdrawWaitingVesselAdvancesCursor(t *testing.T) {
	q := NewQueue()
	chamberID := "chamber-1"
	planID := uuid.New()
	vessels := testVessels(2)
	if err := q.Add(chamberID, planID, vessels); err != nil {
		t.Fatalf("add: %v", err)
	}
	now := time.Unix(100, 0)

	if _, err := q.Withdraw(chamberID, vessels[0].ID, "not arriving", now); err != nil {
		t.Fatalf("withdraw waiting: %v", err)
	}
	head, ok := q.Peek(chamberID)
	if !ok || head.Vessel.ID != vessels[1].ID {
		t.Fatalf("cursor should advance to second vessel, got %+v ok=%v", head, ok)
	}
}
