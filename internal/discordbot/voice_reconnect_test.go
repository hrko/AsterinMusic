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
	c.membershipKnown, c.confirmedChannel = true, 3
	c.beginLocked(true, voiceWaiting)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	f := &voiceRecoveryFlow{id: 1, ctx: ctx, cancel: cancel}
	c.recovery = f
	return f
}

func TestHandshakeFailureDoesNotRejoinAfterBothNotifications(t *testing.T) {
	c := testVoiceController(t)
	var joins, leaves atomic.Int32
	c.canRejoin = func(context.Context, snowflake.ID) bool { return true }
	c.factory = func(context.Context, voice.State, func(error, bool)) (voiceTransport, error) {
		return nil, &voiceStageError{stage: "dave", err: errors.New("fixture failure")}
	}
	c.update = func(_ context.Context, _ snowflake.ID, id *snowflake.ID, _, _ bool) error {
		if id == nil {
			leaves.Add(1)
		} else {
			joins.Add(1)
			c.HandleVoiceStateUpdate(stateEvent(*id))
			c.HandleVoiceServerUpdate(serverEvent("fresh"))
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Open(ctx, 3, false, true); err == nil {
		t.Fatal("handshake failure was ignored")
	}
	if joins.Load() != 1 || leaves.Load() != 0 {
		t.Fatalf("unexpected retry: joins=%d leaves=%d", joins.Load(), leaves.Load())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failureStage != "dave" {
		t.Fatalf("failure stage=%q", c.failureStage)
	}
}

func TestNotificationWaitEndsBeforeHandshakeCompletes(t *testing.T) {
	c := testVoiceController(t)
	f := recoveryFixture(t, c)
	entered := make(chan struct{})
	c.factory = func(ctx context.Context, _ voice.State, _ func(error, bool)) (voiceTransport, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("fresh"))
	<-entered
	if err := c.waitRecoveryUntil(f, 50*time.Millisecond, "notifications"); err != nil {
		t.Fatalf("received notifications still waiting: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != voiceConnecting || c.server != nil || !f.handshakeStarted {
		t.Fatal("consumed notification was not retained as a fact")
	}
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

type delayedResumeDave struct {
	*fakeVoiceTransport
	ready   atomic.Bool
	calls   atomic.Int32
	resumed chan context.Context
}

func (t *delayedResumeDave) Ready() bool { return t.ready.Load() }
func (t *delayedResumeDave) Resume(ctx context.Context) error {
	t.calls.Add(1)
	t.resumed <- ctx
	return nil
}

func TestResumedDaveWaitDoesNotRepeatResume(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "failed"}[fail], func(t *testing.T) {
			c := testVoiceController(t)
			f := recoveryFixture(t, c)
			transport := &delayedResumeDave{fakeVoiceTransport: newFakeVoiceTransport(), resumed: make(chan context.Context, 1)}
			c.mu.Lock()
			c.active = transport
			c.phase = voiceRecovering
			c.mu.Unlock()
			done := make(chan struct{})
			go func() { c.runRecovery(f, true, false); close(done) }()
			var attempt context.Context
			select {
			case attempt = <-transport.resumed:
			case <-time.After(time.Second):
				t.Fatal("Resume not called")
			}
			select {
			case <-attempt.Done():
			case <-time.After(time.Second):
				t.Fatal("Resume attempt context retained")
			}
			if fail {
				c.mu.Lock()
				generation := c.generation
				c.mu.Unlock()
				c.transportClosed(generation, &websocket.CloseError{Code: 4006}, true)
			} else {
				transport.ready.Store(true)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("DAVE wait stuck")
			}
			if transport.calls.Load() != 1 {
				t.Fatal("second Resume sent after Resumed")
			}
			if fail != c.Failed() {
				t.Fatalf("failed=%v want %v", c.Failed(), fail)
			}
		})
	}
}

func TestSuccessfulHandshakeDoesNotRemainCurrentDuringRecovery(t *testing.T) {
	c := testVoiceController(t)
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("test"))
	waitVoice(t, c)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.traceAttempt != nil {
		t.Fatal("completed handshake retained as current attempt")
	}
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
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	c.tracer = provider.Tracer("test")
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
	deadline := time.Now().Add(time.Second)
	for len(recorder.Started()) != len(recorder.Ended()) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(recorder.Started()) != len(recorder.Ended()) {
		t.Fatal("Resume trace did not finish")
	}
	failedResumes, recovered := 0, 0
	for _, span := range recorder.Ended() {
		if span.Name() == "voice.resume" {
			if span.Status().Code != codes.Error {
				t.Fatal("rejected Resume not marked failed")
			}
			if !span.Parent().IsValid() {
				t.Fatal("Resume is not attached to recovery")
			}
			foundCode := false
			for _, attr := range span.Attributes() {
				if string(attr.Key) == "asterinmusic.close_code" && attr.Value.AsInt64() == int64(code) {
					foundCode = true
				}
			}
			if !foundCode {
				t.Fatal("Resume close code missing")
			}
			failedResumes++
		}
		if span.Name() == "voice.recovery" && span.Status().Code == codes.Unset && spanString(span, "asterinmusic.result") == "ready" {
			recovered++
		}
	}
	if failedResumes != attempts || recovered != 2 {
		t.Fatalf("lost retry outcome: failures=%d recoveries=%d", failedResumes, recovered)
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
	c.membershipKnown, c.confirmedChannel = true, 3
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
