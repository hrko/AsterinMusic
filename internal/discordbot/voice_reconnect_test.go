package discordbot

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestMissingServerNotificationRejoinsAfterConfirmedLeave(t *testing.T) {
	c := testVoiceController(t)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	c.tracer = provider.Tracer("test")
	c.canRejoin = func(context.Context, snowflake.ID) bool { return true }
	var joins, leaves atomic.Int32
	c.update = func(_ context.Context, _ snowflake.ID, id *snowflake.ID, _, _ bool) error {
		if id == nil {
			leaves.Add(1)
			e := stateEvent(0)
			e.ChannelID = nil
			c.HandleVoiceStateUpdate(e)
		} else {
			n := joins.Add(1)
			c.HandleVoiceStateUpdate(stateEvent(*id))
			if n == 2 {
				c.HandleVoiceServerUpdate(serverEvent("fresh"))
			}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := c.Open(ctx, 3, false, true); err != nil {
		t.Fatal(err)
	}
	if joins.Load() != 2 || leaves.Load() != 1 {
		t.Fatalf("joins=%d leaves=%d", joins.Load(), leaves.Load())
	}
	if c.ChannelID() == nil || *c.ChannelID() != 3 {
		t.Fatal("target lost during reset")
	}
	deadline := time.Now().Add(time.Second)
	for len(recorder.Started()) != len(recorder.Ended()) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(recorder.Started()) != len(recorder.Ended()) {
		t.Fatal("rejoin trace did not finish")
	}
	var recovered, failedWait, permission, leave bool
	for _, span := range recorder.Ended() {
		switch span.Name() {
		case "voice.recovery":
			recovered = span.Status().Code == codes.Unset && spanString(span, "asterinmusic.result") == "ready"
		case "voice.wait":
			failedWait = failedWait || span.Status().Code == codes.Error
			leave = leave || spanString(span, "asterinmusic.stage") == "leave_confirmation"
		case "voice.rejoin_permission":
			permission = span.Status().Code == codes.Unset
		}
	}
	if !recovered || !failedWait || !permission || !leave {
		t.Fatalf("rejoin trace lost outcome: recovered=%v failedWait=%v permission=%v leave=%v", recovered, failedWait, permission, leave)
	}
}

func recoveryFixture(t *testing.T, c *voiceConnection) *voiceRecoveryFlow {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.channel = 3
	c.beginLocked(true, voiceWaiting)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	f := &voiceRecoveryFlow{id: 1, ctx: ctx, cancel: cancel}
	c.recovery = f
	return f
}

func TestRejoinPermissionRaceStopAndMove(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "move", true: "stop"}[stop], func(t *testing.T) {
			c := testVoiceController(t)
			f := recoveryFixture(t, c)
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			c.canRejoin = func(context.Context, snowflake.ID) bool { close(entered); <-release; return true }
			var joins atomic.Int32
			c.update = func(_ context.Context, _ snowflake.ID, id *snowflake.ID, _, _ bool) error {
				if id != nil {
					joins.Add(1)
				}
				return nil
			}
			go func() { c.rejoinRecovery(f); close(done) }()
			<-entered
			if stop {
				c.Close(context.Background())
			} else {
				c.HandleVoiceStateUpdate(stateEvent(4))
				c.HandleVoiceServerUpdate(serverEvent("moved"))
				waitVoice(t, c)
			}
			close(release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("recovery stuck")
			}
			if joins.Load() != 0 {
				t.Fatal("old recovery rejoined")
			}
		})
	}
}

func TestRejoinBudgetAndPermissionRejectBeforeLeave(t *testing.T) {
	for _, mode := range []string{"permission", "attempt", "recent"} {
		t.Run(mode, func(t *testing.T) {
			c := testVoiceController(t)
			f := recoveryFixture(t, c)
			c.canRejoin = func(context.Context, snowflake.ID) bool { return mode != "permission" }
			c.mu.Lock()
			if mode == "attempt" {
				c.rejoins = 1
			}
			if mode == "recent" {
				c.rejoinTimes = []time.Time{time.Now(), time.Now()}
			}
			c.mu.Unlock()
			var calls atomic.Int32
			c.update = func(context.Context, snowflake.ID, *snowflake.ID, bool, bool) error { calls.Add(1); return nil }
			c.rejoinRecovery(f)
			if calls.Load() != 0 || !c.Failed() {
				t.Fatal("budget/permission did not stop rejoin")
			}
		})
	}
}

type rejectedResume struct {
	*fakeVoiceTransport
	code  int
	calls atomic.Int32
}

func (t *rejectedResume) Resume(context.Context) error {
	t.calls.Add(1)
	return &websocket.CloseError{Code: t.code}
}

func TestResumeKickNeverRejoins(t *testing.T) {
	c := testVoiceController(t)
	transport := &rejectedResume{fakeVoiceTransport: newFakeVoiceTransport(), code: 4014}
	c.factory = func(context.Context, voice.State, func(error, bool)) (voiceTransport, error) { return transport, nil }
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("a"))
	waitVoice(t, c)
	var joins atomic.Int32
	c.update = func(_ context.Context, _ snowflake.ID, id *snowflake.ID, _, _ bool) error {
		if id != nil {
			joins.Add(1)
		}
		return nil
	}
	c.mu.Lock()
	gen := c.generation
	c.mu.Unlock()
	c.transportClosed(gen, &websocket.CloseError{Code: 1006}, true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.waitReady(ctx); !errors.Is(err, errVoiceRecovery) {
		t.Fatalf("unexpected result: %v", err)
	}
	if joins.Load() != 0 || transport.calls.Load() != 1 {
		t.Fatal("kicked session retried")
	}
}

func TestResumeBudgetFallsBackToFreshConnection(t *testing.T) {
	testResumeFallback(t, 1006, 2)
}

func TestExpiredResumeFallsBackToFreshConnection(t *testing.T) {
	testResumeFallback(t, 4006, 1)
}

func testResumeFallback(t *testing.T, code, attempts int) {
	t.Helper()
	c := testVoiceController(t)
	transport := &rejectedResume{fakeVoiceTransport: newFakeVoiceTransport(), code: code}
	var opens atomic.Int32
	c.factory = func(context.Context, voice.State, func(error, bool)) (voiceTransport, error) {
		if opens.Add(1) == 1 {
			return transport, nil
		}
		return newFakeVoiceTransport(), nil
	}
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("old"))
	waitVoice(t, c)
	var joins atomic.Int32
	c.update = func(_ context.Context, _ snowflake.ID, id *snowflake.ID, _, _ bool) error {
		if id != nil {
			joins.Add(1)
			c.HandleVoiceStateUpdate(stateEvent(*id))
			c.HandleVoiceServerUpdate(serverEvent("new"))
		}
		return nil
	}
	c.mu.Lock()
	gen := c.generation
	c.mu.Unlock()
	c.transportClosed(gen, &websocket.CloseError{Code: 1006}, true)
	waitVoice(t, c)
	if transport.calls.Load() != int32(attempts) || joins.Load() != 1 || opens.Load() != 2 {
		t.Fatal("resume budget or fallback incorrect")
	}
}

func TestStopCancelsRecoveryBeforeAsyncTeardown(t *testing.T) {
	c := testVoiceController(t)
	f := recoveryFixture(t, c)
	var calls atomic.Int32
	c.update = func(context.Context, snowflake.ID, *snowflake.ID, bool, bool) error { calls.Add(1); return nil }
	c.stopRecovery()
	if err := c.recoveryUpdate(f, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("stop result: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("stopped supervisor sent join")
	}
}

func TestRecoveryDoesNotRejoinWithoutLeaveConfirmation(t *testing.T) {
	c := testVoiceController(t)
	c.canRejoin = func(context.Context, snowflake.ID) bool { return true }
	var joins atomic.Int32
	c.update = func(context.Context, snowflake.ID, *snowflake.ID, bool, bool) error { joins.Add(1); return nil }
	c.mu.Lock()
	c.channel = 3
	c.beginLocked(true, voiceWaiting)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	f := &voiceRecoveryFlow{id: 1, ctx: ctx, cancel: cancel}
	c.recovery = f
	c.mu.Unlock()
	c.rejoinRecovery(f)
	if joins.Load() != 1 || !c.Failed() {
		t.Fatal("rejoined without confirmation or did not fail")
	}
}

func TestResumePolicy(t *testing.T) {
	for _, code := range []int{1001, 1006, 4015} {
		if !resumeAllowed(&websocket.CloseError{Code: code}) {
			t.Fatalf("not resumable: %d", code)
		}
	}
	for _, code := range []int{1000, 4006, 4014, 4021, 4022} {
		if resumeAllowed(&websocket.CloseError{Code: code}) {
			t.Fatalf("unexpected resume: %d", code)
		}
	}
}

func TestExpiredSessionGetsFreshMembershipDespiteLibraryFlag(t *testing.T) {
	for _, code := range []int{4006, 4009} {
		c := testVoiceController(t)
		c.HandleVoiceStateUpdate(stateEvent(3))
		c.HandleVoiceServerUpdate(serverEvent("old"))
		waitVoice(t, c)
		var joins atomic.Int32
		c.update = func(_ context.Context, _ snowflake.ID, id *snowflake.ID, _, _ bool) error {
			if id != nil {
				joins.Add(1)
				c.HandleVoiceStateUpdate(stateEvent(*id))
				c.HandleVoiceServerUpdate(serverEvent("fresh"))
			}
			return nil
		}
		c.mu.Lock()
		gen := c.generation
		c.mu.Unlock()
		c.transportClosed(gen, &websocket.CloseError{Code: code}, false)
		waitVoice(t, c)
		if joins.Load() != 1 {
			t.Fatalf("expired session %d did not rejoin", code)
		}
	}
}
