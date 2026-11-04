package music

import (
	"asterinmusic/internal/logging"
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestQueuePersistenceSafeFailureAndRecovery(t *testing.T) {
	var out bytes.Buffer
	logger, err := logging.New(&out, logging.Options{Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	q := NewQueue(10)
	q.statePath = "PRIVATE_PATH"
	q.SetLogger(logger.With("guild_id", "1"))
	q.writeState = func(string, []byte) error { return errors.New("PRIVATE_STORAGE_ERROR") }
	_, _ = q.Add(Requester{ID: "a"}, []Track{{URL: "PRIVATE_URL", Title: "PRIVATE_TITLE"}}, "", "")
	if q.PersistenceError() == nil {
		t.Fatal("failure not retained")
	}
	q.writeState = func(string, []byte) error { return nil }
	_, _ = q.Add(Requester{ID: "a"}, []Track{{URL: "another"}}, "", "")
	text := out.String()
	if strings.Contains(text, "PRIVATE") {
		t.Fatalf("private values leaked: %s", text)
	}
	for _, expected := range []string{`"level":"ERROR"`, `"event":"state.queue.save_failed"`, `"event":"state.queue.save_recovered"`, `"guild_id":"1"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %s: %s", expected, text)
		}
	}
}
