// Package music contains Discord-independent playback state.
package music

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const DefaultQueueLimit = 500

type Track struct {
	ThumbnailURL  string   `json:"thumbnail_url,omitempty"`
	ChannelID     string   `json:"channel_id,omitempty"`
	ChannelTitle  string   `json:"channel_title,omitempty"`
	TitleSource   string   `json:"title_source,omitempty"`
	TitleLanguage string   `json:"title_language,omitempty"`
	URL           string   `json:"url"`
	Title         string   `json:"title"`
	Site          string   `json:"site"`
	Duration      *float64 `json:"duration,omitempty"`
}

type Requester struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Entry struct {
	ID          string    `json:"id"`
	Track       Track     `json:"track"`
	Requester   Requester `json:"requester"`
	AddedAt     time.Time `json:"added_at"`
	PlaylistURL string    `json:"playlist_url,omitempty"`
	ImportID    string    `json:"import_id,omitempty"`
}

// Queue is a concurrent-safe, per-guild round-robin queue. The active user
// rejoins at the tail only when Finish is called (including stop/skip/failure).
type Queue struct {
	logger      *slog.Logger
	mutationMu  sync.Mutex // Mutations and their durable writes are ordered.
	mu          sync.Mutex
	limit       int
	sequence    uint64
	waiting     map[string][]Entry
	order       []string
	current     *Entry
	statePath   string
	accessScope string
	lastSaveErr error
	writeState  func(string, []byte) error // Optional deterministic storage boundary for tests.
}

func NewQueue(limit int) *Queue {
	if limit <= 0 {
		limit = DefaultQueueLimit
	}
	return &Queue{limit: limit, waiting: make(map[string][]Entry)}
}

// Add preserves batch order and returns the number accepted within the limit.
func (q *Queue) Add(who Requester, tracks []Track, playlist, importID string) (int, error) {
	n, _, err := q.AddAt(who, tracks, playlist, importID, Insertion{})
	return n, err
}

// Insertion changes only the requester's own waiting list. A waiting anchor
// takes precedence over Front; an absent/current anchor uses the fallback.
type Insertion struct {
	Front   bool
	AfterID string
}

func (q *Queue) AddAt(who Requester, tracks []Track, playlist, importID string, position Insertion) (int, string, error) {
	q.mutationMu.Lock()
	defer q.mutationMu.Unlock()
	if who.ID == "" {
		return 0, "", errors.New("requester ID is required")
	}
	for _, t := range tracks {
		if t.URL == "" {
			return 0, "", errors.New("track URL is required")
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	count := 0
	for _, entries := range q.waiting {
		count += len(entries)
	}
	n := min(len(tracks), max(0, q.limit-count))
	if n == 0 {
		// A rejected/empty batch does not change state, but can retry a
		// previous failed save after the storage becomes available again.
		if q.lastSaveErr != nil {
			q.persistLocked()
		}
		return 0, "", nil
	}
	defer q.persistLocked()
	if len(q.waiting[who.ID]) == 0 && (q.current == nil || q.current.Requester.ID != who.ID) {
		q.order = append(q.order, who.ID)
	}
	entries := make([]Entry, 0, n)
	for _, t := range tracks[:n] {
		q.sequence++
		entries = append(entries, Entry{
			ID: fmt.Sprint(q.sequence), Track: cloneTrack(t), Requester: who,
			AddedAt: time.Now().UTC(), PlaylistURL: playlist, ImportID: importID,
		})
	}
	waiting := q.waiting[who.ID]
	index := len(waiting)
	if position.Front {
		index = 0
	}
	if position.AfterID != "" {
		for i, entry := range waiting {
			if entry.ID == position.AfterID {
				index = i + 1
				break
			}
		}
	}
	updated := make([]Entry, 0, len(waiting)+n)
	updated = append(updated, waiting[:index]...)
	updated = append(updated, entries...)
	q.waiting[who.ID] = append(updated, waiting[index:]...)
	return n, entries[0].ID, nil
}

// Start does not replace an active track. The caller must Finish it first.
func (q *Queue) Start() (Entry, bool) {
	q.mutationMu.Lock()
	defer q.mutationMu.Unlock()
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.current != nil || len(q.order) == 0 {
		return Entry{}, false
	}
	user := q.order[0]
	q.order = q.order[1:]
	entry := q.waiting[user][0]
	q.waiting[user] = q.waiting[user][1:]
	q.current = &entry
	q.persistLocked()
	return cloneEntry(entry), true
}

// Finish checks the entry ID so a late callback cannot finish a newer track.
func (q *Queue) Finish(id string) bool {
	q.mutationMu.Lock()
	defer q.mutationMu.Unlock()
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.current == nil || q.current.ID != id {
		return false
	}
	user := q.current.Requester.ID
	if len(q.waiting[user]) > 0 {
		q.order = append(q.order, user)
	} else {
		delete(q.waiting, user)
	}
	q.current = nil
	q.persistLocked()
	return true
}

func (q *Queue) Clear() {
	q.mutationMu.Lock()
	defer q.mutationMu.Unlock()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.waiting = make(map[string][]Entry)
	q.order = nil
	q.persistLocked()
}

// Snapshot returns independent copies in projected playback order.
func (q *Queue) Snapshot() (*Entry, []Entry) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var current *Entry
	order := append([]string(nil), q.order...)
	if q.current != nil {
		copy := cloneEntry(*q.current)
		current = &copy
		if len(q.waiting[copy.Requester.ID]) > 0 {
			order = append(order, copy.Requester.ID)
		}
	}
	indices := make(map[string]int)
	var result []Entry
	for len(order) > 0 {
		user := order[0]
		order = order[1:]
		i := indices[user]
		result = append(result, cloneEntry(q.waiting[user][i]))
		indices[user]++
		if indices[user] < len(q.waiting[user]) {
			order = append(order, user)
		}
	}
	return current, result
}

func cloneTrack(t Track) Track {
	if t.Duration != nil {
		d := *t.Duration
		t.Duration = &d
	}
	return t
}

func cloneEntry(e Entry) Entry { e.Track = cloneTrack(e.Track); return e }
