package discordbot

import (
	"asterinmusic/internal/logging"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/disgoorg/disgo/voice"
	"github.com/gorilla/websocket"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type stalledVoiceOutput struct {
	entered, release chan struct{}
	once             sync.Once
}

func (w *stalledVoiceOutput) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func TestVoiceCloseDoesNotWaitForAsyncLogWriter(t *testing.T) {
	w := &stalledVoiceOutput{entered: make(chan struct{}), release: make(chan struct{})}
	base, _ := logging.New(w, logging.Options{Level: "debug"})
	logger, output := logging.Async(base)
	defer func() {
		close(w.release)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := output.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	logger.Info("stall fixture", "event", "fixture")
	<-w.entered
	c := testVoiceController(t)
	c.logger = logger
	c.recordDiagnosticLocked(slog.LevelError, "connection failed", "event", "voice.failed")
	closed := make(chan struct{})
	go func() { c.Close(context.Background()); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("connection close waited for stalled log output")
	}
}

func TestDiagnosticEventAttributes(t *testing.T) {
	for _, tc := range []struct {
		attrs []any
		want  string
	}{
		{nil, "voice.diagnostics.unclassified"},
		{[]any{"event"}, "voice.diagnostics.unclassified"},
		{[]any{"event", 12}, "voice.diagnostics.unclassified"},
		{[]any{"stage", "dave", "event", "voice.handshake.failed"}, "voice.handshake.failed"},
		{[]any{slog.String("event", "voice.dave.event")}, "voice.dave.event"},
	} {
		if got := diagnosticEvent(tc.attrs); got != tc.want {
			t.Fatalf("event=%q want %q", got, tc.want)
		}
	}
}

func TestDiagnosticOverflowBoundsUnknownEventCategories(t *testing.T) {
	c := newVoiceConnection(1, 2)
	const count = 1000
	for i := range count {
		c.recordDiagnosticLocked(slog.LevelDebug, "fixture", "stage", "dave", "event", fmt.Sprintf("fixture.%d", i))
	}
	if len(c.diagnostics) != voiceDiagnosticCapacity || len(c.diagnosticOverflow) > voiceDiagnosticOverflowCapacity+1 {
		t.Fatal("diagnostics exceeded bounded capacity")
	}
	total := len(c.diagnostics)
	for _, record := range c.diagnosticOverflow {
		total += record.count
	}
	if total != count {
		t.Fatalf("lost overflow count: %d", total)
	}
}

func TestTransportDiagnosticCaptureDoesNotWaitForWriter(t *testing.T) {
	w := blockingLogWriter{make(chan struct{}), make(chan struct{})}
	l, _ := logging.New(w, logging.Options{Level: "debug"})
	c := newVoiceConnection(1, 2)
	c.logger = l
	c.recordDiagnosticLocked(slog.LevelInfo, "fixture", "event", "fixture")
	flushed := make(chan struct{})
	go func() { c.flushDiagnostics(); close(flushed) }()
	defer func() { close(w.release); <-flushed }()
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not block")
	}
	ctx := c.diagnosticContext(context.Background())
	attrs := []any{"event", "voice.dave.event"}
	done := make(chan struct{})
	go func() { recordVoiceDiagnostic(ctx, l, slog.LevelDebug, "DAVE", attrs...); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("protocol diagnostic waited for log writer")
	}
	attrs[1] = "changed"
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.diagnostics) != 1 || diagnosticEvent(c.diagnostics[0].attrs) != "voice.dave.event" || c.diagnostics[0].ctx != ctx {
		t.Fatal("diagnostic snapshot or originating context lost")
	}
}

func TestRecoveryTimeoutPreservesHandshakeStage(t *testing.T) {
	c := testVoiceController(t)
	c.phase = voiceConnecting
	c.traceAttempt = &voiceTraceAttempt{name: "dave", ended: true}
	f := &voiceRecoveryFlow{stage: "rejoin_notifications", cancel: func() {}}
	c.recovery = f
	c.recoveryFailure(f, context.DeadlineExceeded)
	if c.failureStage != "dave" {
		t.Fatalf("handshake misclassified as %s", c.failureStage)
	}
}

func TestHandshakeFailureSummaryWithoutTracingOrDebug(t *testing.T) {
	var out logBuffer
	logger, err := logging.New(&out, logging.Options{Format: "json", Level: "info"})
	if err != nil {
		t.Fatal(err)
	}
	c := testVoiceController(t)
	c.logger = logger
	c.factory = func(ctx context.Context, _ voice.State, _ func(error, bool)) (voiceTransport, error) {
		a := ctx.Value(voiceTraceKey{}).(*voiceTraceAttempt)
		deadline, _ := ctx.Deadline()
		d := &voiceHandshakeDeadline{record: a, overall: deadline, fail: func(string, error) {}}
		defer d.stop()
		d.advance("session_description")
		d.advance("transport_key")
		return nil, &voiceStageError{"transport_key", errors.New("PRIVATE_KEY")}
	}
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("PRIVATE_TOKEN"))
	until := time.Now().Add(time.Second)
	for {
		c.mu.Lock()
		failed := c.phase == voiceFailed
		c.mu.Unlock()
		if failed {
			break
		}
		if time.Now().After(until) {
			t.Fatal("factory failure was not applied")
		}
		time.Sleep(time.Millisecond)
	}
	c.flushDiagnostics()
	text := out.String()
	if strings.Contains(text, "PRIVATE_") {
		t.Fatal("secret in failure summary")
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["event"] != "voice.recovery.failed" {
			continue
		}
		for _, key := range []string{"connection_flow_id", "trigger", "recovery_id", "attempt_id", "attempt_generation", "generation", "stage_duration_ms", "stage_deadline", "recovery_deadline", "reason"} {
			if value, ok := record[key]; !ok || value == "" {
				t.Errorf("missing %s: %v", key, record)
			}
		}
		for key, want := range map[string]any{"level": "ERROR", "stage": "transport_key", "handshake_stage": "transport_key", "last_completed_stage": "session_description", "deadline_scope": "stage", "membership_received": true, "server_received": true, "action": "hold_playback"} {
			if record[key] != want {
				t.Errorf("%s=%v want=%v", key, record[key], want)
			}
		}
		return
	}
	t.Fatalf("no failure summary at INFO: %s", text)
}

func TestTerminalVoiceFailureRetainsSafeCause(t *testing.T) {
	var out logBuffer
	l, _ := logging.New(&out, logging.Options{Format: "json"})
	c := testVoiceController(t)
	c.logger = l
	c.phase = voiceReady
	c.channel = 3
	c.transportClosed(c.generation, &websocket.CloseError{Code: 4021, Text: "PRIVATE_TOKEN"}, false)
	c.flushDiagnostics()
	text := out.String()
	for _, value := range []string{`"stage":"transport"`, `"reason":"websocket_closed"`, `"close_code":4021`, `"action":"hold_playback"`} {
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
