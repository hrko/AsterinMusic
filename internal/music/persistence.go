package music

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
)

type queueState struct {
	Version  int                `json:"version"`
	Sequence uint64             `json:"sequence"`
	Waiting  map[string][]Entry `json:"waiting"`
	Order    []string           `json:"order"`
	Current  *Entry             `json:"current,omitempty"`
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
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.lastSaveErr
}

func (q *Queue) persistLocked() {
	if q.statePath == "" {
		return
	}
	q.lastSaveErr = q.saveLocked()
	if q.lastSaveErr != nil {
		slog.Error("queue checkpoint failed; restart may lose recent changes", "error", q.lastSaveErr)
	}
}

func (q *Queue) saveLocked() error {
	b, err := json.Marshal(queueState{1, q.sequence, q.waiting, q.order, q.current})
	if err != nil {
		return err
	}
	dir := filepath.Dir(q.statePath)
	if err = os.MkdirAll(dir, 0700); err != nil {
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
	return os.Rename(f.Name(), q.statePath)
}
