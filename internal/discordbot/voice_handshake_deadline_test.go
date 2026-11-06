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
