package music

import (
	"path/filepath"
	"testing"
)

func TestQueueAccessScopeSurvivesWritesAndRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	q, err := OpenQueue(10, path)
	if err != nil {
		t.Fatal(err)
	}
	if q.AccessScope() != "" {
		t.Fatal("new queue has invented scope")
	}
	if err = q.SetAccessScope("123"); err != nil {
		t.Fatal(err)
	}
	if _, err = q.Add(Requester{ID: "1"}, []Track{{URL: "test"}}, "", ""); err != nil {
		t.Fatal(err)
	}
	q, err = OpenQueue(10, path)
	if err != nil {
		t.Fatal(err)
	}
	if q.AccessScope() != "123" {
		t.Fatal("scope lost on restore")
	}
	q.Clear()
	q, err = OpenQueue(10, path)
	if err != nil || q.AccessScope() != "123" {
		t.Fatal("clear lost scope", err)
	}
}
