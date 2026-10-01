package admission

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

// AdmissionStatus is the per-vessel state inside the lock-entry flow.
type AdmissionStatus string

const (
	// StatusStandby: vessel is waiting in FIFO order, not yet cleared to enter.
	StatusStandby AdmissionStatus = "standby"
	// StatusAllowed: vessel received the proceed signal and may enter the chamber.
	StatusAllowed AdmissionStatus = "allowed"
	// StatusEntered: vessel physically entered the chamber and was confirmed.
	StatusEntered AdmissionStatus = "entered"
	// StatusWithdrawn: vessel backed out before entering; it no longer blocks the queue.
	StatusWithdrawn AdmissionStatus = "withdrawn"
	// StatusFailed: vessel failed or lost contact after being cleared; it blocks the queue.
	StatusFailed AdmissionStatus = "failed"
)

// Settled reports whether the vessel has finished its admission attempt.
func (s AdmissionStatus) Settled() bool {
	return s == StatusEntered || s == StatusWithdrawn || s == StatusFailed
}

func (s AdmissionStatus) Valid() bool {
	switch s {
	case StatusStandby, StatusAllowed, StatusEntered, StatusWithdrawn, StatusFailed:
		return true
	default:
		return false
	}
}

type QueueEntry struct {
	Vessel    model.Vessel    `json:"vessel"`
	PlanID    uuid.UUID       `json:"plan_id"`
	Status    AdmissionStatus `json:"status"`
	Reason    string          `json:"reason,omitempty"`
	AllowedAt *time.Time      `json:"allowed_at,omitempty"`
	EnteredAt *time.Time      `json:"entered_at,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Queue preserves FIFO order while tracking per-vessel admission progress.
type Queue struct {
	mu      sync.RWMutex
	entries map[string][]QueueEntry
}

func NewQueue() *Queue { return &Queue{entries: make(map[string][]QueueEntry)} }

func (q *Queue) Add(chamberID string, planID uuid.UUID, vessels []model.Vessel, now time.Time) error {
	if chamberID == "" || planID == uuid.Nil || len(vessels) == 0 {
		return errors.New("queue addition requires chamber, plan and vessels")
	}
	q.mu.Lock()
	stamped := now.UTC()
	for _, vessel := range vessels {
		q.entries[chamberID] = append(q.entries[chamberID], QueueEntry{
			Vessel: vessel, PlanID: planID, Status: StatusStandby, UpdatedAt: stamped,
		})
	}
	q.mu.Unlock()
	return nil
}

// Head returns the first vessel still in the admission flow (standby or
// allowed). A failed vessel ahead of it blocks the queue: blocked is true and
// entry describes the blocker. Settled (entered/withdrawn) vessels are skipped.
func (q *Queue) Head(chamberID string) (entry QueueEntry, blocked bool, ok bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	for _, candidate := range q.entries[chamberID] {
		switch candidate.Status {
		case StatusFailed:
			return candidate, true, true
		case StatusStandby, StatusAllowed:
			return candidate, false, true
		}
	}
	return QueueEntry{}, false, false
}

// Find returns the current queue entry for a vessel.
func (q *Queue) Find(chamberID string, vesselID uuid.UUID) (QueueEntry, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	for _, entry := range q.entries[chamberID] {
		if entry.Vessel.ID == vesselID {
			return entry, true
		}
	}
	return QueueEntry{}, false
}

// AllowHead clears the FIFO head for entry. The head must be the given plan's
// standby vessel; a failed vessel ahead of it rejects the call.
func (q *Queue) AllowHead(chamberID string, planID, vesselID uuid.UUID, now time.Time) (QueueEntry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for index := range q.entries[chamberID] {
		entry := &q.entries[chamberID][index]
		switch entry.Status {
		case StatusFailed:
			return QueueEntry{}, errors.New("ahead vessel has failed or lost contact; queue is blocked")
		case StatusStandby, StatusAllowed:
			if entry.PlanID != planID || entry.Vessel.ID != vesselID {
				return QueueEntry{}, errors.New("vessel is not the FIFO head of this plan")
			}
			if entry.Status == StatusStandby {
				stamped := now.UTC()
				entry.Status = StatusAllowed
				entry.AllowedAt = &stamped
				entry.UpdatedAt = stamped
				entry.Reason = ""
			}
			return *entry, nil
		}
	}
	return QueueEntry{}, errors.New("no vessel awaiting admission")
}

// transitionHead moves the FIFO head matching vesselID to a terminal status.
func (q *Queue) transitionHead(chamberID string, vesselID uuid.UUID, status AdmissionStatus, reason string, now time.Time) (QueueEntry, error) {
	if !status.Settled() {
		return QueueEntry{}, errors.New("transition target must be a settled admission status")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for index := range q.entries[chamberID] {
		entry := &q.entries[chamberID][index]
		switch entry.Status {
		case StatusFailed:
			return QueueEntry{}, errors.New("queue is already blocked by a failed vessel")
		case StatusStandby, StatusAllowed:
			if entry.Vessel.ID != vesselID {
				return QueueEntry{}, errors.New("vessel is not the FIFO head; later vessels cannot be processed first")
			}
			if entry.Status == StatusStandby && status != StatusWithdrawn {
				return QueueEntry{}, errors.New("standby vessel can only be withdrawn before it is allowed")
			}
			stamped := now.UTC()
			entry.Status = status
			entry.Reason = reason
			entry.UpdatedAt = stamped
			if status == StatusEntered {
				entry.EnteredAt = &stamped
			}
			return *entry, nil
		}
	}
	return QueueEntry{}, errors.New("no active vessel at the queue head")
}

func (q *Queue) MarkEntered(chamberID string, vesselID uuid.UUID, now time.Time) (QueueEntry, error) {
	return q.transitionHead(chamberID, vesselID, StatusEntered, "", now)
}

func (q *Queue) MarkWithdrawn(chamberID string, vesselID uuid.UUID, reason string, now time.Time) (QueueEntry, error) {
	return q.transitionHead(chamberID, vesselID, StatusWithdrawn, reason, now)
}

func (q *Queue) MarkFailed(chamberID string, vesselID uuid.UUID, reason string, now time.Time) (QueueEntry, error) {
	return q.transitionHead(chamberID, vesselID, StatusFailed, reason, now)
}

// PlanEntryComplete reports whether every vessel of the plan has finished
// entering (entered or orderly withdrawn) with none failed or outstanding.
func (q *Queue) PlanEntryComplete(chamberID string, planID uuid.UUID) bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	total := 0
	for _, entry := range q.entries[chamberID] {
		if entry.PlanID != planID {
			continue
		}
		total++
		if entry.Status != StatusEntered && entry.Status != StatusWithdrawn {
			return false
		}
	}
	return total > 0
}

// PlanProgress summarises admission progress for one plan.
func (q *Queue) PlanProgress(chamberID string, planID uuid.UUID) (total, entered, allowed, pending int) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	for _, entry := range q.entries[chamberID] {
		if entry.PlanID != planID {
			continue
		}
		total++
		switch entry.Status {
		case StatusEntered:
			entered++
		case StatusAllowed:
			allowed++
		case StatusStandby:
			pending++
		}
	}
	return total, entered, allowed, pending
}

func (q *Queue) RemovePlan(chamberID string, planID uuid.UUID) []QueueEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := q.entries[chamberID]
	kept := entries[:0]
	var removed []QueueEntry
	for _, entry := range entries {
		if entry.PlanID == planID {
			removed = append(removed, entry)
		} else {
			kept = append(kept, entry)
		}
	}
	q.entries[chamberID] = append([]QueueEntry(nil), kept...)
	return removed
}

// Restore re-attaches entries (for example after a compensating cancellation).
// Restored vessels start over from standby so they are never silently cleared.
func (q *Queue) Restore(chamberID string, entries []QueueEntry, now time.Time) {
	q.mu.Lock()
	current := q.entries[chamberID]
	restored := make([]QueueEntry, 0, len(entries)+len(current))
	stamped := now.UTC()
	for _, entry := range entries {
		entry.Status = StatusStandby
		entry.Reason = ""
		entry.AllowedAt = nil
		entry.EnteredAt = nil
		entry.UpdatedAt = stamped
		restored = append(restored, entry)
	}
	q.entries[chamberID] = append(restored, current...)
	q.mu.Unlock()
}

func (q *Queue) List(chamberID string) []QueueEntry {
	q.mu.RLock()
	result := append([]QueueEntry(nil), q.entries[chamberID]...)
	q.mu.RUnlock()
	return result
}
