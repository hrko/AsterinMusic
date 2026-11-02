package music

import (
	"fmt"
	"sync"
	"testing"
)

func add(t *testing.T, q *Queue, user string, names ...string) {
	t.Helper()
	var tracks []Track
	for _, name := range names {
		tracks = append(tracks, Track{URL: name, Title: name})
	}
	if _, err := q.Add(Requester{ID: user}, tracks, "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestRoundRobinAndLateJoin(t *testing.T) {
	q := NewQueue(500)
	add(t, q, "A", "A1", "A2", "A3")
	first, _ := q.Start()
	add(t, q, "B", "B1", "B2")
	add(t, q, "C", "C1")
	_, planned := q.Snapshot()
	want := []string{"B1", "C1", "A2", "B2", "A3"}
	if len(planned) != len(want) {
		t.Fatalf("plan length: %d", len(planned))
	}
	for i, e := range planned {
		if e.Track.Title != want[i] {
			t.Fatalf("plan: %v", planned)
		}
	}
	if !q.Finish(first.ID) {
		t.Fatal("finish")
	}
	for _, title := range want {
		e, ok := q.Start()
		if !ok || e.Track.Title != title {
			t.Fatalf("got %v, want %s", e, title)
		}
		if q.Finish(first.ID) {
			t.Fatal("stale callback consumed new track")
		}
		q.Finish(e.ID)
	}
	if _, ok := q.Start(); ok {
		t.Fatal("not empty")
	}
}

func TestSnapshotOwnershipAndStopResume(t *testing.T) {
	q := NewQueue(500)
	duration := 12.0
	q.Add(Requester{ID: "A", Name: "Alice"}, []Track{{URL: "same", Duration: &duration}, {URL: "same"}}, "playlist", "batch")
	duration = 99
	first, ok := q.Start()
	if !ok {
		t.Fatal("start")
	}
	if *first.Track.Duration != 12 {
		t.Fatal("input pointer retained")
	}
	*first.Track.Duration = 88
	current, _ := q.Snapshot()
	if *current.Track.Duration != 12 {
		t.Fatal("output pointer retained")
	}
	add(t, q, "B", "same")
	q.Finish(first.ID) // stop consumes A's turn without automatically starting B
	current, planned := q.Snapshot()
	if current != nil || len(planned) != 2 || planned[0].Requester.ID != "B" || planned[1].Requester.ID != "A" {
		t.Fatal(planned)
	}
	if planned[1].ImportID != "batch" || planned[1].Requester.Name != "Alice" {
		t.Fatal("lost attribution")
	}
	if planned[0].ID == planned[1].ID {
		t.Fatal("duplicates must have independent IDs")
	}
}

func TestClearAndAddWhilePlaying(t *testing.T) {
	q := NewQueue(2)
	add(t, q, "A", "one", "two", "discard")
	current, _ := q.Start()
	q.Clear()
	add(t, q, "A", "three")
	add(t, q, "B", "four")
	q.Finish(current.ID)
	e, _ := q.Start()
	if e.Track.Title != "four" {
		t.Fatalf("got %v", e)
	}
}

func TestConcurrentCapacityAndOwnership(t *testing.T) {
	q := NewQueue(100)
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Go(func() { q.Add(Requester{ID: fmt.Sprint(i)}, []Track{{URL: "duplicate"}}, "playlist", "batch") })
	}
	wg.Wait()
	_, entries := q.Snapshot()
	if len(entries) != 100 {
		t.Fatalf("length %d", len(entries))
	}
	ids := map[string]bool{}
	for _, e := range entries {
		if ids[e.ID] || e.Requester.ID == "" || e.ImportID != "batch" {
			t.Fatalf("invalid entry %v", e)
		}
		ids[e.ID] = true
	}
}
