package music

import (
	"asterinmusic/internal/logging"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
)

type queueState struct {
	Version     int                `json:"version"`
	Sequence    uint64             `json:"sequence"`
	Waiting     map[string][]Entry `json:"waiting"`
	Order       []string           `json:"order"`
	Current     *Entry             `json:"current,omitempty"`
	AccessScope string             `json:"access_scope,omitempty"`
}

// OpenQueue restores manual entries only. A previously active entry is retried
// from its beginning, retaining the round-robin position of all other users.
// Use one process per state path. A corrupt file is never silently overwritten.
func OpenQueue(limit int, path string) (*Queue, error) {
	q := NewQueue(limit)
	q.statePath = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return q, nil
	}
	if err != nil {
		return nil, err
	}
	var state queueState
	if err = json.Unmarshal(b, &state); err != nil {
		return nil, fmt.Errorf("decode queue state: %w", err)
	}
	if err = validateState(state, q.limit); err != nil {
		return nil, err
	}
	q.sequence, q.waiting, q.order = state.Sequence, state.Waiting, state.Order
	q.accessScope = state.AccessScope
	if q.waiting == nil {
		q.waiting = make(map[string][]Entry)
	}
	if state.Current != nil {
		user := state.Current.Requester.ID
		q.waiting[user] = append([]Entry{*state.Current}, q.waiting[user]...)
		q.order = append([]string{user}, q.order...)
	}
	return q, nil
}

func validateState(s queueState, limit int) error {
	invalid := errors.New("invalid queue state; keep the file for recovery")
	if s.Version != 1 {
		return invalid
	}
	if s.AccessScope != "" {
		id, err := strconv.ParseUint(s.AccessScope, 10, 64)
		if err != nil || id == 0 || strconv.FormatUint(id, 10) != s.AccessScope {
			return invalid
		}
	}
	seen := map[string]bool{}
	check := func(e Entry, user string) bool {
		id, err := strconv.ParseUint(e.ID, 10, 64)
		if err != nil || id == 0 || id > s.Sequence || seen[e.ID] || user == "" || e.Requester.ID != user || e.Track.URL == "" {
			return false
		}
		seen[e.ID] = true
		return true
	}
	active := ""
	if s.Current != nil {
		active = s.Current.Requester.ID
		if !check(*s.Current, active) {
			return invalid
		}
	}
	ordered := map[string]bool{}
	for _, user := range s.Order {
		if user == active || ordered[user] || len(s.Waiting[user]) == 0 {
			return invalid
		}
		ordered[user] = true
	}
	count := 0
	for user, entries := range s.Waiting {
		if len(entries) > 0 && user != active && !ordered[user] {
			return invalid
		}
		for _, entry := range entries {
			if !check(entry, user) {
				return invalid
			}
			count++
		}
	}
	// A lowered configured limit must not discard previously accepted entries.
	return nil
}

func (q *Queue) PersistenceError() error {
	q.mutationMu.Lock()
	defer q.mutationMu.Unlock()
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.lastSaveErr
}

// AccessScope belongs to the application; an empty value means legacy/unknown.
func (q *Queue) AccessScope() string { q.mu.Lock(); defer q.mu.Unlock(); return q.accessScope }
func (q *Queue) SetAccessScope(scope string) error {
	q.mutationMu.Lock()
	defer q.mutationMu.Unlock()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.accessScope = scope
	q.persistLocked()
	return q.lastSaveErr
}

// SetLogger sets the queue's owner-scoped logger. Queue mutation and logging
// configuration are serialized; no process-global logger is used.
func (q *Queue) SetLogger(logger *slog.Logger) {
	q.mutationMu.Lock()
	defer q.mutationMu.Unlock()
	q.logger = logging.Component(logger, "state")
}

func (q *Queue) persistLocked() {
	if q.statePath == "" {
		return
	}
	b, err := json.Marshal(queueState{Version: 1, Sequence: q.sequence, Waiting: q.waiting, Order: q.order, Current: q.current, AccessScope: q.accessScope})
	if err == nil {
		write := q.writeState
		if write == nil {
			write = writeQueueState
		}
		// mutationMu preserves mutation/write order while readers use mu.
		q.mu.Unlock()
		err = write(q.statePath, b)
		q.mu.Lock()
	}
	previous := q.lastSaveErr
	q.lastSaveErr = err
	logger := logging.OrDiscard(q.logger)
	q.mu.Unlock()
	if err != nil {
		reason := "storage"
		if errors.Is(err, os.ErrPermission) {
			reason = "permission"
		} else if errors.Is(err, os.ErrNotExist) {
			reason = "not_found"
		}
		logger.Error("queue checkpoint failed", "event", "state.queue.save_failed", "reason", reason, "action", "retain_memory", "queue_retained", true)
	} else if previous != nil {
		logger.Info("queue persistence recovered", "event", "state.queue.save_recovered")
	}
	q.mu.Lock()
}

func writeQueueState(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".queue-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
