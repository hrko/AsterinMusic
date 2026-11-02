package music

import (
	"path/filepath"
	"reflect"
	"testing"
)

func insertionTracks(names ...string) []Track {
	var ts []Track
	for _, n := range names {
		ts = append(ts, Track{URL: n, Title: n})
	}
	return ts
}
func assertOrder(t *testing.T, q *Queue, want ...string) {
	t.Helper()
	_, entries := q.Snapshot()
	var got []string
	for _, e := range entries {
		got = append(got, e.Track.URL)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPrependPreservesRoundRobin(t *testing.T) {
	a, b, c := Requester{ID: "a"}, Requester{ID: "b"}, Requester{ID: "c"}
	q := NewQueue(500)
	q.Add(a, insertionTracks("A1", "A2", "A3"), "", "")
	active, _ := q.Start()
	q.Add(b, insertionTracks("B1", "B2"), "", "")
	q.AddAt(a, insertionTracks("X"), "", "", Insertion{Front: true})
	assertOrder(t, q, "B1", "X", "B2", "A2", "A3")
	q.AddAt(a, insertionTracks("Y"), "", "", Insertion{Front: true})
	q.AddAt(c, insertionTracks("C1"), "", "", Insertion{Front: true})
	assertOrder(t, q, "B1", "C1", "Y", "B2", "X", "A2", "A3")
	current, _ := q.Snapshot()
	if current.ID != active.ID {
		t.Fatal("active track interrupted")
	}
}

func TestAttachedListAnchorAndFallback(t *testing.T) {
	for _, front := range []bool{false, true} {
		for _, state := range []string{"waiting", "playing", "finished", "cleared"} {
			t.Run(state+map[bool]string{false: "-tail", true: "-front"}[front], func(t *testing.T) {
				q := NewQueue(500)
				user := Requester{ID: "a"}
				// Same URL is deliberately present twice: anchor by entry ID, not URL.
				q.Add(user, insertionTracks("X"), "", "")
				_, anchor, err := q.AddAt(user, insertionTracks("X"), "", "", Insertion{})
				if err != nil {
					t.Fatal(err)
				}
				q.Add(user, insertionTracks("Y"), "", "")
				switch state {
				case "playing", "finished":
					first, _ := q.Start()
					q.Finish(first.ID)
					second, _ := q.Start()
					if state == "finished" {
						q.Finish(second.ID)
					}
				case "cleared":
					q.Clear()
					q.Add(user, insertionTracks("Y"), "", "")
				}
				q.AddAt(user, insertionTracks("L1", "L2"), "list", "offer", Insertion{Front: front, AfterID: anchor})
				if state == "waiting" {
					assertOrder(t, q, "X", "X", "L1", "L2", "Y")
				} else if front {
					assertOrder(t, q, "L1", "L2", "Y")
				} else {
					assertOrder(t, q, "Y", "L1", "L2")
				}
			})
		}
	}
}

func TestPrependCapacityValidationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	q, err := OpenQueue(3, path)
	if err != nil {
		t.Fatal(err)
	}
	user := Requester{ID: "a"}
	q.Add(user, insertionTracks("old"), "", "")
	if n, _, err := q.AddAt(user, []Track{{URL: "ok"}, {}}, "", "", Insertion{Front: true}); n != 0 || err == nil {
		t.Fatal("invalid batch changed queue")
	}
	n, first, err := q.AddAt(user, insertionTracks("L1", "L2", "L3"), "list", "offer", Insertion{Front: true})
	if n != 2 || first == "" || err != nil {
		t.Fatalf("partial add: %d %s %v", n, first, err)
	}
	restored, err := OpenQueue(3, path)
	if err != nil {
		t.Fatal(err)
	}
	assertOrder(t, restored, "L1", "L2", "old")
	_, entries := restored.Snapshot()
	if entries[0].ID != first || entries[0].ImportID != "offer" || entries[0].Requester.ID != "a" {
		t.Fatal("metadata lost")
	}
	if n, _, err := restored.AddAt(user, insertionTracks("full"), "", "", Insertion{Front: true}); n != 0 || err != nil {
		t.Fatal("full queue accepted")
	}
}
