package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/slogtest"
)

func TestStandardHandlerContract(t *testing.T) {
	var out bytes.Buffer
	l, err := New(&out, Options{Format: "json", Level: "debug"})
	if err != nil {
		t.Fatal(err)
	}
	err = slogtest.TestHandler(l.Handler(), func() []map[string]any {
		var results []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatal(err)
			}
			results = append(results, record)
		}
		return results
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReservedAndDuplicateKeys(t *testing.T) {
	var out bytes.Buffer
	l, _ := New(&out, Options{Format: "json"})
	l.With("event", "old").Info("actual", "event", "new", "msg", "fake", "level", "ERROR", "time", "fake", slog.Attr{})
	text := out.String()
	for _, key := range []string{"msg", "level", "time", "event"} {
		if strings.Count(text, `"`+key+`":`) != 1 {
			t.Fatalf("duplicate %s: %s", key, text)
		}
	}
	if strings.Contains(text, "fake") || strings.Contains(text, `"event":"old"`) {
		t.Fatalf("reserved attributes replaced output: %s", text)
	}
}

func TestUnknownIdentifiersAreOmitted(t *testing.T) {
	var out bytes.Buffer
	l, _ := New(&out, Options{Format: "json"})
	l.Info("record", "channel_id", "0", "bot_id", "", "guild_id", "3")
	if strings.Contains(out.String(), "channel_id") || strings.Contains(out.String(), "bot_id") || !strings.Contains(out.String(), `"guild_id":"3"`) {
		t.Fatalf("invalid identity fields: %s", out.String())
	}
}

func TestComponentLevelsAndGroups(t *testing.T) {
	var out bytes.Buffer
	l, err := New(&out, Options{Format: "json", Components: map[string]string{"voice": "debug"}})
	if err != nil {
		t.Fatal(err)
	}
	l.Debug("hidden")
	Component(l, "voice").WithGroup("attempt").With("generation", 1).WithGroup("detail").With("old", true).Info("transition", "new", true)
	Component(l, "voice").Debug("decision")
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("records: %s", out.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	attempt := record["attempt"].(map[string]any)
	if attempt["generation"] != float64(1) {
		t.Fatalf("lost bound attribute: %v", record)
	}
	detail := attempt["detail"].(map[string]any)
	if detail["old"] != true || detail["new"] != true {
		t.Fatalf("lost grouped attributes: %v", record)
	}
}

func TestDependencySecretsAndSingleLine(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			l, err := New(&out, Options{Format: format, Level: "debug"})
			if err != nil {
				t.Fatal(err)
			}
			d := Dependency(l, "discord").With("token", "SYNTHETIC_SECRET")
			for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
				d.Log(t.Context(), level, "SYNTHETIC_SECRET\nforged", "error", errors.New("SYNTHETIC_SECRET"), "code", 4006)
			}
			l.Info("safe\nmessage", "token", "SYNTHETIC_SECRET", "error", errors.New("SYNTHETIC_SECRET"))
			if strings.Contains(out.String(), "SYNTHETIC_SECRET") {
				t.Fatal("secret leaked")
			}
			if strings.Count(out.String(), "\n") != 5 {
				t.Fatalf("unexpected line count: %s", out.String())
			}
			if !strings.Contains(out.String(), "4006") {
				t.Fatal("safe status omitted")
			}
		})
	}
}

type failedWriter struct {
	mu    sync.Mutex
	calls int
}

func (w *failedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	return 0, errors.New("unavailable")
}
func TestConcurrentWriterFailure(t *testing.T) {
	w := &failedWriter{}
	l, err := New(w, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() { Component(l, "voice").Info("result", "event", "voice.result") })
	}
	wg.Wait()
	if w.calls != 20 {
		t.Fatalf("recursive or missing writes: %d", w.calls)
	}
}

func TestInvalidOptionsDoNotEchoValues(t *testing.T) {
	for _, o := range []Options{{Level: "SECRET"}, {Format: "SECRET"}, {Components: map[string]string{"SECRET": "debug"}}, {Components: map[string]string{"voice": "SECRET"}}} {
		err := o.Validate()
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("unsafe validation: %v", err)
		}
	}
}
