package logging

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRepetitionIsolationRecoveryAndBound(t *testing.T) {
	var out bytes.Buffer
	l, err := New(&out, Options{Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	r := Repetition{now: func() time.Time { return now }}
	fail := func(key string) { r.Failure(t.Context(), l, key, "failure", "event", "test.failed", "guild_id", key) }
	fail("guild-a")
	fail("guild-a")
	fail("guild-b")
	if strings.Count(out.String(), "\n") != 2 {
		t.Fatalf("incorrect aggregation: %s", out.String())
	}
	now = now.Add(time.Second)
	r.Resolve(t.Context(), l, "guild-a")
	if !strings.Contains(out.String(), `"suppressed":1`) || !strings.Contains(out.String(), `"duration_ms":1000`) {
		t.Fatalf("missing summary: %s", out.String())
	}
	before := strings.Count(out.String(), "\n")
	fail("guild-a")
	if strings.Count(out.String(), "\n") != before+1 {
		t.Fatal("recurrence suppressed")
	}
	for i := 0; i < RepetitionCapacity+10; i++ {
		fail(fmt.Sprint(i))
		fail(fmt.Sprint(i))
	}
	if len(r.entries) != RepetitionCapacity {
		t.Fatal("unbounded aggregation")
	}
	if !strings.Contains(out.String(), `"summary_reason":"capacity"`) {
		t.Fatal("eviction lost suppressed records")
	}
	r.Close(t.Context(), l)
	if len(r.entries) != 0 {
		t.Fatal("close retained state")
	}
}

func TestRepetitionInterval(t *testing.T) {
	var out bytes.Buffer
	l, _ := New(&out, Options{Format: "json"})
	now := time.Unix(100, 0)
	r := Repetition{now: func() time.Time { return now }}
	for i := 0; i < 3; i++ {
		r.Failure(t.Context(), l, "same", "failed", "event", "test.failed")
	}
	now = now.Add(RepetitionInterval)
	r.Failure(t.Context(), l, "same", "failed", "event", "test.failed")
	if strings.Count(out.String(), "\n") != 3 || !strings.Contains(out.String(), `"suppressed":2`) {
		t.Fatalf("bad interval: %s", out.String())
	}
}
