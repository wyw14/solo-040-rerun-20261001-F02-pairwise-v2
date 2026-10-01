package admission

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

// QueueEntry tracks one vessel through the per-vessel admission confirmation flow.
type QueueEntry struct {
	Vessel     model.Vessel         `json:"vessel"`
	PlanID     uuid.UUID            `json:"plan_id"`
	State      model.AdmissionState `json:"state"`
	AdmittedAt *time.Time           `json:"admitted_at,omitempty"`
	EnteredAt  *time.Time           `json:"entered_at,omitempty"`
	ResolvedAt *time.Time           `json:"resolved_at,omitempty"`
	Note       string               `json:"note,omitempty"`
}

// PlanProgress summarizes the convoy queue position for one plan.
type PlanProgress struct {
	PlanID        uuid.UUID  `json:"plan_id"`
	Total         int        `json:"total"`
	Waiting       int        `json:"waiting"`
	Admitted      int        `json:"admitted"`
	Entered       int        `json:"entered"`
	Withdrawn     int        `json:"withdrawn"`
	Failed        int        `json:"failed"`
	BlockedVessel *uuid.UUID `json:"blocked_vessel,omitempty"`
	// Resolved is true once every vessel entered or was withdrawn; the convoy no
	// longer occupies the admission cursor.
	Resolved bool `json:"resolved"`
	// Completed is true only when every vessel physically entered the chamber.
	Completed bool `json:"completed"`
}

// Queue preserves FIFO order while supporting convoy cancellation recovery.
type Queue struct {
	mu      sync.RWMutex
	entries map[string][]QueueEntry
}

func NewQueue() *Queue { return &Queue{entries: make(map[string][]QueueEntry)} }

func (q *Queue) Add(chamberID string, planID uuid.UUID, vessels []model.Vessel) error {
	if chamberID == "" || planID == uuid.Nil || len(vessels) == 0 {
		return errors.New("queue addition requires chamber, plan and vessels")
	}
	q.mu.Lock()
	for _, vessel := range vessels {
		q.entries[chamberID] = append(q.entries[chamberID], QueueEntry{
			Vessel: vessel, PlanID: planID, State: model.AdmissionWaiting,
		})
	}
	q.mu.Unlock()
	return nil
}

// activeIndex returns the index of the first vessel that still owns the queue
// cursor: a failed vessel keeps the cursor and blocks everyone behind it.
func activeIndex(entries []QueueEntry) int {
	for index := range entries {
		if !entries[index].State.Resolved() {
			return index
		}
	}
	return -1
}

// Peek returns the vessel currently owning the admission cursor. A failed entry
// is returned with State == failed so callers treat the queue as blocked.
func (q *Queue) Peek(chamberID string) (QueueEntry, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	entries := q.entries[chamberID]
	index := activeIndex(entries)
	if index < 0 {
		return QueueEntry{}, false
	}
	return entries[index], true
}

// transition applies a guarded state change to the cursor vessel in one
// lock-held critical section so concurrent requests can never release two
// vessels or skip past a failed one.
func (q *Queue) transition(chamberID string, vesselID uuid.UUID, allowed map[model.AdmissionState]model.AdmissionState, stampResolved bool, note string, now time.Time) (QueueEntry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := q.entries[chamberID]
	index := activeIndex(entries)
	if index < 0 {
		return QueueEntry{}, errors.New("admission queue is empty")
	}
	entry := entries[index]
	if entry.Vessel.ID != vesselID {
		if blocker, found := q.findLocked(entries, vesselID); found {
			return blocker, errors.New("vessel is not at the queue head")
		}
		return QueueEntry{}, errors.New("vessel is not queued for this chamber")
	}
	next, ok := allowed[entry.State]
	if !ok {
		return entry, errors.New("vessel cannot transition from " + string(entry.State))
	}
	stamped := now.UTC()
	entry.State = next
	if note != "" {
		entry.Note = note
	}
	switch next {
	case model.AdmissionAdmitted:
		entry.AdmittedAt = &stamped
	case model.AdmissionEntered:
		entry.EnteredAt = &stamped
	}
	if stampResolved {
		entry.ResolvedAt = &stamped
	}
	entries[index] = entry
	return entry, nil
}

// Grant clears the waiting cursor vessel to enter.
func (q *Queue) Grant(chamberID string, vesselID uuid.UUID, now time.Time) (QueueEntry, error) {
	return q.transition(chamberID, vesselID, map[model.AdmissionState]model.AdmissionState{
		model.AdmissionWaiting: model.AdmissionAdmitted,
	}, false, "", now)
}

// MarkEntered records that the cleared vessel physically entered the chamber.
func (q *Queue) MarkEntered(chamberID string, vesselID uuid.UUID, now time.Time) (QueueEntry, error) {
	return q.transition(chamberID, vesselID, map[model.AdmissionState]model.AdmissionState{
		model.AdmissionAdmitted: model.AdmissionEntered,
		model.AdmissionEntered:  model.AdmissionEntered,
	}, true, "", now)
}

// Withdraw pulls a vessel out of the flow. It is the only recovery path past a
// failed/missing vessel: an operator must explicitly stand that vessel down
// before the cursor may move to the next one.
func (q *Queue) Withdraw(chamberID string, vesselID uuid.UUID, note string, now time.Time) (QueueEntry, error) {
	if note == "" {
		note = "clearance withdrawn"
	}
	return q.transition(chamberID, vesselID, map[model.AdmissionState]model.AdmissionState{
		model.AdmissionWaiting:   model.AdmissionWithdrawn,
		model.AdmissionAdmitted:  model.AdmissionWithdrawn,
		model.AdmissionFailed:    model.AdmissionWithdrawn,
		model.AdmissionWithdrawn: model.AdmissionWithdrawn,
	}, true, note, now)
}

// MarkFailed records a missed clearance or lost contact and blocks the queue.
func (q *Queue) MarkFailed(chamberID string, vesselID uuid.UUID, note string, now time.Time) (QueueEntry, error) {
	if note == "" {
		note = "vessel failed to enter or lost contact"
	}
	return q.transition(chamberID, vesselID, map[model.AdmissionState]model.AdmissionState{
		model.AdmissionWaiting:  model.AdmissionFailed,
		model.AdmissionAdmitted: model.AdmissionFailed,
	}, false, note, now)
}

func (q *Queue) findLocked(entries []QueueEntry, vesselID uuid.UUID) (QueueEntry, bool) {
	for _, entry := range entries {
		if entry.Vessel.ID == vesselID {
			return entry, true
		}
	}
	return QueueEntry{}, false
}

// PlanProgress reports the confirmation progress of one plan within a chamber.
func (q *Queue) PlanProgress(chamberID string, planID uuid.UUID) (PlanProgress, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	progress := PlanProgress{PlanID: planID}
	found := false
	for _, entry := range q.entries[chamberID] {
		if entry.PlanID != planID {
			continue
		}
		found = true
		progress.Total++
		switch entry.State {
		case model.AdmissionWaiting:
			progress.Waiting++
		case model.AdmissionAdmitted:
			progress.Admitted++
		case model.AdmissionEntered:
			progress.Entered++
		case model.AdmissionWithdrawn:
			progress.Withdrawn++
		case model.AdmissionFailed:
			progress.Failed++
			if progress.BlockedVessel == nil {
				id := entry.Vessel.ID
				progress.BlockedVessel = &id
			}
		}
	}
	if !found {
		return PlanProgress{}, false
	}
	progress.Resolved = progress.Entered+progress.Withdrawn == progress.Total
	progress.Completed = progress.Entered == progress.Total
	return progress, true
}

// Blocked reports whether the chamber cursor is held by a failed vessel.
func (q *Queue) Blocked(chamberID string) bool {
	entry, ok := q.Peek(chamberID)
	return ok && entry.State == model.AdmissionFailed
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

func (q *Queue) Restore(chamberID string, entries []QueueEntry) {
	q.mu.Lock()
	current := q.entries[chamberID]
	restored := make([]QueueEntry, 0, len(entries)+len(current))
	for _, entry := range entries {
		// A restored entry re-enters the FIFO flow from standby; its previous
		// clearance is re-evaluated in order.
		entry.State = model.AdmissionWaiting
		entry.AdmittedAt = nil
		entry.EnteredAt = nil
		entry.ResolvedAt = nil
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
