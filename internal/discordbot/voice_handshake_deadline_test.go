package discordbot

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestVoiceStageDeadlineBoundaries(t *testing.T) {
	for _, stage := range []string{"websocket_connect", "hello", "identify", "ready", "udp_discovery", "select_protocol", "session_description", "transport_key", "dave"} {
		t.Run(stage, func(t *testing.T) {
			overall := time.Now().Add(time.Second)
			d := &voiceHandshakeDeadline{overall: overall, fail: func(string, error) {}}
			defer d.stop()
			d.advance(stage)
			if !d.deadline.Equal(overall) {
				t.Fatal("stage exceeded remaining overall budget")
			}
			previous := d.deadline
			d.advance(stage)
			if !d.deadline.Equal(previous) {
				t.Fatal("duplicate stage extended deadline")
			}
		})
	}
}

func TestVoiceStageDeadlineUsesDiagnosticProgressRecord(t *testing.T) {
	tracer := tracerOrNoop(nil)
	ctx, span := tracer.Start(context.Background(), "voice.connect")
	r := &voiceTraceAttempt{ctx: ctx, span: span, tracer: tracer}
	defer r.end(nil)
	d := &voiceHandshakeDeadline{record: r, overall: time.Now().Add(20 * time.Second), fail: func(string, error) {}}
	defer d.stop()
	if !d.advance("identify") || r.current() != "identify" {
		t.Fatal("deadline and diagnostic stage differ")
	}
	r.mu.Lock()
	started, deadline, scope := r.stageStarted, r.stageDeadline, r.deadlineScope
	r.mu.Unlock()
	if !deadline.Equal(d.deadline) || scope != "stage" {
		t.Fatal("diagnostic does not describe the enforced deadline")
	}
	d.advance("identify")
	r.mu.Lock()
	unchanged := r.stageStarted.Equal(started) && r.stageDeadline.Equal(deadline)
	r.mu.Unlock()
	if !unchanged {
		t.Fatal("duplicate progress reset diagnostic timing")
	}
	d.advance("ready")
	r.mu.Lock()
	clipped := r.stageDeadline.Equal(d.overall) && r.deadlineScope == "recovery" && r.lastCompleted == "identify"
	r.mu.Unlock()
	if !clipped {
		t.Fatal("response wait lost recovery deadline or prior completion")
	}
	d.expire("ready", d.deadline)
	if d.advance("dave") || r.current() != "ready" {
		t.Fatal("late progress changed the timed-out diagnostic")
	}
}

func TestVoiceStageDeadlineIgnoresRetiredTimers(t *testing.T) {
	failed := make(chan string, 2)
	d := &voiceHandshakeDeadline{overall: time.Now().Add(time.Minute), fail: func(stage string, err error) {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Error(err)
		}
		failed <- stage
	}}
	defer d.stop()
	d.advance("identify")
	first := d.deadline
	d.advance("ready")
	d.expire("identify", first)
	select {
	case <-failed:
		t.Fatal("retired stage failed current stage")
	default:
	}
	d.expire("ready", d.deadline)
	if got := <-failed; got != "ready" {
		t.Fatal(got)
	}
	if d.advance("dave") || d.current() != "ready" {
		t.Fatal("late progress overwrote terminal stage")
	}
}

func TestVoiceStageDeadlineActuallyExpiresAndStops(t *testing.T) {
	failed := make(chan string, 1)
	d := &voiceHandshakeDeadline{overall: time.Now().Add(20 * time.Millisecond), fail: func(stage string, _ error) { failed <- stage }}
	defer d.stop()
	d.advance("dave")
	select {
	case stage := <-failed:
		if stage != "dave" {
			t.Fatal(stage)
		}
	case <-time.After(time.Second):
		t.Fatal("deadline did not expire")
	}
	d.stop()
	d.expire("dave", d.deadline)
	select {
	case <-failed:
		t.Fatal("duplicate terminal outcome")
	default:
	}
}

func TestVoiceStageDeadlineSuccessRetiresTimer(t *testing.T) {
	failed := false
	d := &voiceHandshakeDeadline{overall: time.Now().Add(time.Minute), fail: func(string, error) { failed = true }}
	d.advance("dave")
	if !d.finish() {
		t.Fatal("valid completion rejected")
	}
	d.expire("dave", d.deadline)
	if failed {
		t.Fatal("completed handshake failed late")
	}
}
