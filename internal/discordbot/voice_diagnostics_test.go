package discordbot

import (
	"asterinmusic/internal/logging"
	"encoding/json"
	"github.com/gorilla/websocket"
	"strings"
	"testing"
	"time"
)

func TestTerminalVoiceFailureRetainsSafeCause(t *testing.T) {
	var out logBuffer
	l, _ := logging.New(&out, logging.Options{Format: "json"})
	c := testVoiceController(t)
	c.logger = l
	c.phase = voiceReady
	c.channel = 3
	c.transportClosed(c.generation, &websocket.CloseError{Code: 4006, Text: "PRIVATE_TOKEN"}, false)
	c.flushDiagnostics()
	text := out.String()
	for _, value := range []string{`"stage":"transport"`, `"reason":"websocket_closed"`, `"close_code":4006`, `"action":"hold_playback"`} {
		if !strings.Contains(text, value) {
			t.Fatalf("missing outcome attribute: %s", text)
		}
	}
	if strings.Contains(text, "PRIVATE_TOKEN") {
		t.Fatal("close text leaked")
	}
}

func TestVoiceTransitionCaptureNeverWritesUnderLock(t *testing.T) {
	w := blockingLogWriter{make(chan struct{}), make(chan struct{})}
	logger, _ := logging.New(w, logging.Options{})
	c := newVoiceConnection(1, 2)
	c.logger = logger
	c.channel = 3
	c.mu.Lock()
	c.signalLocked(voiceReady)
	c.mu.Unlock()
	select {
	case <-w.entered:
		t.Fatal("state transition wrote synchronously")
	default:
	}
	done := make(chan struct{})
	go func() { c.flushDiagnostics(); close(done) }()
	defer func() { close(w.release); <-done }()
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("missing deferred log")
	}
	changed := make(chan struct{})
	go func() { c.mu.Lock(); c.signalLocked(voiceRecovering); c.mu.Unlock(); close(changed) }()
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("Writer blocked a state transition")
	}
}

func TestVoiceTransitionOverflowPreservesOutcomeCounts(t *testing.T) {
	var out logBuffer
	l, _ := logging.New(&out, logging.Options{Format: "json", Level: "debug"})
	c := newVoiceConnection(1, 2)
	c.logger = l
	c.channel = 3
	const attempts = 1000
	for range attempts {
		c.mu.Lock()
		c.signalLocked(voiceFailed)
		c.signalLocked(voiceReady)
		c.mu.Unlock()
	}
	if len(c.diagnostics) > voiceDiagnosticCapacity || len(c.diagnosticOverflow) > 4 {
		t.Fatal("unbounded diagnostics")
	}
	c.flushDiagnostics()
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		n := 1
		if value, ok := event["occurrences"].(float64); ok {
			n = int(value)
		}
		counts[event["event"].(string)] += n
	}
	if counts["voice.recovery.failed"] != attempts || counts["voice.connection.ready"] != attempts {
		t.Fatalf("lost terminal outcomes: %v", counts)
	}
	if len(c.diagnostics) != 0 || len(c.diagnosticOverflow) != 0 {
		t.Fatal("retained flushed records")
	}
}
