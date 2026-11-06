package music

import (
	"errors"
	"slices"
)

var (
	ErrEntryNotWaiting = errors.New("entry is not waiting")
	ErrQueueChanged    = errors.New("requester queue changed")
	ErrQueuePosition   = errors.New("invalid requester queue position")
)

// RequesterEntries returns detached entries in this requester's waiting order.
func (q *Queue) RequesterEntries(requester string) []Entry {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := make([]Entry, len(q.waiting[requester]))
	for i, entry := range q.waiting[requester] {
		entries[i] = cloneEntry(entry)
	}
	return entries
}

// MoveWaiting moves one entry to a one-based final position. expectedIDs is
// the displayed requester order; changes to other requesters do not conflict.
// Validation, mutation and persistence share the queue's mutation boundary.
func (q *Queue) MoveWaiting(requester, id string, position int, expectedIDs []string) error {
	q.mutationMu.Lock()
	defer q.mutationMu.Unlock()
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := q.waiting[requester]
	index := slices.IndexFunc(entries, func(e Entry) bool { return e.ID == id })
	if index < 0 {
		return ErrEntryNotWaiting
	}
	if len(expectedIDs) != len(entries) {
		return ErrQueueChanged
	}
	for i, entry := range entries {
		if entry.ID != expectedIDs[i] {
			return ErrQueueChanged
		}
	}
	if position < 1 || position > len(entries) {
		return ErrQueuePosition
	}
	to := position - 1
	if index == to {
		return nil
	}
	entry := entries[index]
	if index < to {
		copy(entries[index:to], entries[index+1:to+1])
	} else {
		copy(entries[to+1:index+1], entries[to:index])
	}
	entries[to] = entry
	q.persistLocked()
	return nil
}

// RemoveWaiting never removes the current entry, even if it was selected
// while waiting. The owner and item ID must both still match.
func (q *Queue) RemoveWaiting(requester, id string) error {
	q.mutationMu.Lock()
	defer q.mutationMu.Unlock()
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := q.waiting[requester]
	index := slices.IndexFunc(entries, func(e Entry) bool { return e.ID == id })
	if index < 0 {
		return ErrEntryNotWaiting
	}
	q.waiting[requester] = slices.Delete(entries, index, index+1)
	if len(q.waiting[requester]) == 0 {
		delete(q.waiting, requester)
		q.order = slices.DeleteFunc(q.order, func(owner string) bool { return owner == requester })
	}
	q.persistLocked()
	return nil
}
