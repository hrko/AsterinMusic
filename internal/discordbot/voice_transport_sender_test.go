package discordbot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
)

type stalledSpeakingConn struct {
	frameTraceConn
	entered, release chan struct{}
}

func (c *stalledSpeakingConn) SetSpeaking(context.Context, voice.SpeakingFlags) error {
	close(c.entered)
	<-c.release
	return nil
}

func TestVoiceTransportStalledSenderDoesNotBlockStateOrCanceledResume(t *testing.T) {
	conn := &stalledSpeakingConn{frameTraceConn: frameTraceConn{udp: &protocolTestUDP{}}, entered: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &discordVoiceTransport{conn: conn, dave: readyTraceDave{}, sendGate: make(chan struct{}, 1), done: ctx.Done(), cancelEvents: cancel}
	done := make(chan error, 1)
	go func() { done <- transport.Write(context.Background(), []byte{1}) }()
	released := false
	defer func() {
		if !released {
			close(conn.release)
		}
	}()
	select {
	case <-conn.entered:
	case <-time.After(time.Second):
		t.Fatal("Speaking request did not start")
	}
	if !transport.mu.TryLock() {
		t.Fatal("network operation holds transport state lock")
	}
	transport.mu.Unlock()
	resumeCtx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	resumed := make(chan error, 1)
	go func() { resumed <- transport.Resume(resumeCtx) }()
	select {
	case err := <-resumed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Resume waiting for sender: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Resume exceeded its deadline waiting for sender")
	}
	waiting := make(chan error, 1)
	go func() { waiting <- transport.Write(context.Background(), []byte{2}) }()
	transport.Invalidate()
	select {
	case err := <-waiting:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("invalidated sender: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("invalidation did not release waiting sender")
	}
	close(conn.release)
	released = true
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("late Speaking completion: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("sender did not finish")
	}
	if conn.udp.written.Load() != 0 {
		t.Fatal("late Speaking completion sent audio after invalidation")
	}
}
