package music

import (
	"path/filepath"
	"testing"
)

func TestLowerLimitPreservesQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	q, err := OpenQueue(10, path)
	if err != nil {
		t.Fatal(err)
	}
	q.Add(Requester{ID: "a"}, []Track{{URL: "1"}, {URL: "2"}, {URL: "3"}}, "", "")
	restored, err := OpenQueue(1, path)
	if err != nil {
		t.Fatal(err)
	}
	_, pending := restored.Snapshot()
	if len(pending) != 3 {
		t.Fatal("lost entries")
	}
	if n, err := restored.Add(Requester{ID: "a"}, []Track{{URL: "4"}}, "", ""); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}
