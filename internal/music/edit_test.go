package music

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func entryIDs(entries []Entry) []string {
	ids := make([]string, len(entries))
	for i, entry := range entries {
		ids[i] = entry.ID
	}
	return ids
}

func TestEditPreservesRotationAndRejectsStaleOrder(t *testing.T) {
	q := NewQueue(20)
	q.Add(Requester{ID: "a"}, []Track{{URL: "same"}, {URL: "same"}, {URL: "third"}}, "", "")
	q.Add(Requester{ID: "b"}, []Track{{URL: "b"}}, "", "")
	current, _ := q.Start()
	before := q.RequesterEntries("a")
	ids := entryIDs(before)
	// A change to another requester does not invalidate this view.
	q.Add(Requester{ID: "b"}, []Track{{URL: "b2"}}, "", "")
	if err := q.MoveWaiting("a", before[1].ID, 1, ids); err != nil {
		t.Fatal(err)
	}
	active, waiting := q.Snapshot()
	if active.ID != current.ID || waiting[0].Requester.ID != "b" || waiting[1].ID != before[1].ID {
		t.Fatalf("rotation or current changed: %+v %+v", active, waiting)
	}
	if err := q.MoveWaiting("a", before[0].ID, 1, ids); !errors.Is(err, ErrQueueChanged) {
		t.Fatal(err)
	}
	if err := q.RemoveWaiting("a", current.ID); !errors.Is(err, ErrEntryNotWaiting) {
		t.Fatal(err)
	}
	if err := q.RemoveWaiting("b", before[0].ID); !errors.Is(err, ErrEntryNotWaiting) {
		t.Fatal(err)
	}
	if err := q.RemoveWaiting("a", before[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := q.RemoveWaiting("a", before[0].ID); !errors.Is(err, ErrEntryNotWaiting) {
		t.Fatal(err)
	}
	q.Finish(current.ID)
	_, waiting = q.Snapshot()
	if waiting[0].Requester.ID != "b" || waiting[1].ID != before[1].ID {
		t.Fatal(waiting)
	}
}

func TestEditPersistenceAndPosition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	q, err := OpenQueue(10, path)
	if err != nil {
		t.Fatal(err)
	}
	q.Add(Requester{ID: "a"}, []Track{{URL: "1"}, {URL: "2"}, {URL: "3"}}, "", "")
	entries := q.RequesterEntries("a")
	ids := entryIDs(entries)
	for _, position := range []int{0, 4} {
		if err := q.MoveWaiting("a", ids[0], position, ids); !errors.Is(err, ErrQueuePosition) {
			t.Fatal(err)
		}
	}
	if err := q.MoveWaiting("a", ids[0], 3, ids); err != nil {
		t.Fatal(err)
	}
	if err := q.RemoveWaiting("a", ids[1]); err != nil {
		t.Fatal(err)
	}
	r, err := OpenQueue(10, path)
	if err != nil {
		t.Fatal(err)
	}
	if got := entryIDs(r.RequesterEntries("a")); !reflect.DeepEqual(got, []string{ids[2], ids[0]}) {
		t.Fatal(got)
	}
	for _, id := range []string{ids[2], ids[0]} {
		if err := r.RemoveWaiting("a", id); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := r.Start(); ok {
		t.Fatal("empty requester left in rotation")
	}
	r.Add(Requester{ID: "a"}, []Track{{URL: "new"}}, "", "")
	if _, ok := r.Start(); !ok {
		t.Fatal("requester could not rejoin rotation")
	}
}

func TestEditSelectionConsumedAndSnapshotDetached(t *testing.T) {
	q := NewQueue(10)
	d := 5.0
	q.Add(Requester{ID: "a"}, []Track{{URL: "one", Duration: &d}, {URL: "two"}}, "", "")
	entries := q.RequesterEntries("a")
	*entries[0].Track.Duration = 99
	current, _ := q.Start()
	if *current.Track.Duration != 5 {
		t.Fatal("snapshot aliases queue")
	}
	if err := q.MoveWaiting("a", entries[0].ID, 1, entryIDs(entries)); !errors.Is(err, ErrEntryNotWaiting) {
		t.Fatal(err)
	}
	if err := q.MoveWaiting("a", entries[1].ID, 1, entryIDs(entries)); !errors.Is(err, ErrQueueChanged) {
		t.Fatal(err)
	}
}
