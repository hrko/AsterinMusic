package music

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSnapshotDuringBlockedSave(t *testing.T) {
	q := NewQueue(10)
	q.statePath = filepath.Join(t.TempDir(), "queue.json")
	entered, release := make(chan struct{}), make(chan struct{})
	q.writeState = func(path string, b []byte) error {
		close(entered)
		<-release
		return writeQueueState(path, b)
	}
	added := make(chan struct{})
	go func() { q.Add(Requester{ID: "a"}, []Track{{URL: "first"}}, "", ""); close(added) }()
	<-entered
	read := make(chan []Entry, 1)
	go func() { _, entries := q.Snapshot(); read <- entries }()
	select {
	case entries := <-read:
		if len(entries) != 1 {
			t.Error("in-memory mutation not visible")
		}
	case <-time.After(time.Second):
		close(release)
		<-added
		t.Fatal("save blocked snapshot")
	}
	select {
	case <-added:
		t.Fatal("mutation returned before save completed")
	default:
	}
	close(release)
	<-added
	if err := q.PersistenceError(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenQueue(10, q.statePath)
	if err != nil {
		t.Fatal(err)
	}
	_, entries := r.Snapshot()
	if len(entries) != 1 || entries[0].Track.URL != "first" {
		t.Fatal("saved mutation lost")
	}
}

func TestNoopAddDoesNotRewriteCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	q, err := OpenQueue(1, path)
	if err != nil {
		t.Fatal(err)
	}
	who := Requester{ID: "a"}
	q.Add(who, []Track{{URL: "first"}}, "", "")
	if err := q.PersistenceError(); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1000000000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	for _, tracks := range [][]Track{nil, {{URL: "overflow"}}} {
		if n, err := q.Add(who, tracks, "", ""); n != 0 || err != nil {
			t.Fatalf("add: %d %v", n, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(stamp) {
			t.Fatal("unchanged queue was rewritten")
		}
	}
}

func TestNoopAddRetriesFailedCheckpoint(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "storage")
			if err := os.WriteFile(dir, []byte("unavailable"), 0600); err != nil {
				t.Fatal(err)
			}
			q := NewQueue(1)
			q.statePath = filepath.Join(dir, "queue.json")
			who := Requester{ID: "a"}
			q.Add(who, []Track{{URL: "first"}}, "", "")
			if q.PersistenceError() == nil {
				t.Fatal("expected save failure")
			}
			if err := os.Remove(dir); err != nil {
				t.Fatal(err)
			}
			tracks := []Track{{URL: "overflow"}}
			if empty {
				tracks = nil
			}
			if n, err := q.Add(who, tracks, "", ""); n != 0 || err != nil {
				t.Fatalf("add: %d %v", n, err)
			}
			if err := q.PersistenceError(); err != nil {
				t.Fatal(err)
			}
			restored, err := OpenQueue(1, q.statePath)
			if err != nil {
				t.Fatal(err)
			}
			_, waiting := restored.Snapshot()
			if len(waiting) != 1 || waiting[0].Track.URL != "first" {
				t.Fatalf("unsaved entry lost: %#v", waiting)
			}
		})
	}
}

func TestCheckpointRestartFairOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "queue.json")
	q, err := OpenQueue(10, path)
	if err != nil {
		t.Fatal(err)
	}
	a, b := Requester{ID: "a", Name: "Alice"}, Requester{ID: "b", Name: "Bob"}
	q.Add(a, []Track{{URL: "a1", Title: "日本語タイトル", TitleSource: "youtube_data_api", TitleLanguage: "ja", ChannelID: "UCabcdefghijklmnopqrstuv", ChannelTitle: "channel", ThumbnailURL: "https://i.ytimg.com/example.jpg"}, {URL: "a2"}, {URL: "a3"}}, "playlist", "import")
	current, _ := q.Start()
	q.Add(b, []Track{{URL: "b1"}, {URL: "b2"}}, "", "")
	q.Add(a, []Track{{URL: "a4"}}, "", "")
	_, pending := q.Snapshot()
	want := append([]Entry{current}, pending...)
	r, err := OpenQueue(10, path)
	if err != nil {
		t.Fatal(err)
	}
	var got []Entry
	for {
		entry, ok := r.Start()
		if !ok {
			break
		}
		got = append(got, entry)
		r.Finish(entry.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restart changed fair order or metadata:\ngot %#v\nwant %#v", got, want)
	}
	r, err = OpenQueue(10, path)
	if err != nil {
		t.Fatal(err)
	}
	_, left := r.Snapshot()
	if len(left) != 0 {
		t.Fatal("completed tracks resurrected")
	}
	r.Add(a, []Track{{URL: "new"}}, "", "")
	next, _ := r.Start()
	if next.ID == current.ID {
		t.Fatal("entry IDs reused")
	}
	if r.PersistenceError() != nil {
		t.Fatal(r.PersistenceError())
	}
}

func TestCheckpointFullQueueAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	q, _ := OpenQueue(1, path)
	user := Requester{ID: "a"}
	q.Add(user, []Track{{URL: "first"}}, "", "")
	q.Start()
	q.Add(user, []Track{{URL: "second"}}, "", "")
	r, err := OpenQueue(1, path)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := r.Add(user, []Track{{URL: "overflow"}}, "", ""); n != 0 || err != nil {
		t.Fatalf("overflow: %d %v", n, err)
	}
	// A second restart before /play must preserve both tracks too.
	r, err = OpenQueue(1, path)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := r.Start()
	r.Clear()
	r, err = OpenQueue(1, path)
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := r.Start()
	if restored.Track.URL != first.Track.URL {
		t.Fatal("clear removed active track")
	}
	r.Finish(restored.ID)
	if _, ok := r.Start(); ok {
		t.Fatal("clear resurrected pending entry")
	}
}

func TestCheckpointCorruptAndUnwritable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	bad := []byte(`{"version":1,"sequence":1,"order":["ghost"]}`)
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenQueue(10, path); err == nil {
		t.Fatal("corrupt state accepted")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(bad) {
		t.Fatal("corrupt file overwritten")
	}
	q := NewQueue(10)
	q.statePath = filepath.Join(path, "cannot-write.json")
	q.Add(Requester{ID: "a"}, []Track{{URL: "a"}}, "", "")
	if q.PersistenceError() == nil {
		t.Fatal("save failure hidden")
	}
	_, pending := q.Snapshot()
	if len(pending) != 1 {
		t.Fatal("save failure discarded in-memory entry")
	}
}
