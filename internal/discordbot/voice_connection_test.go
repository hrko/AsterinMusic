package discordbot

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
)

type fakeVoiceTransport struct {
	invalid atomic.Bool
	writes  atomic.Int32
	closed  chan struct{}
	ready   atomic.Bool
}

func newFakeVoiceTransport() *fakeVoiceTransport {
	t := &fakeVoiceTransport{closed: make(chan struct{})}
	t.ready.Store(true)
	return t
}
func (t *fakeVoiceTransport) Invalidate() { t.invalid.Store(true) }
func (t *fakeVoiceTransport) Ready() bool { return t.ready.Load() && !t.invalid.Load() }
func (t *fakeVoiceTransport) Write(context.Context, []byte) error {
	if t.invalid.Load() {
		return context.Canceled
	}
	if !t.ready.Load() {
		return errDaveNotReady
	}
	t.writes.Add(1)
	return nil
}

func TestExplicitVoiceOpenRequiresFreshMembership(t *testing.T) {
	c := testVoiceController(t)
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("old"))
	waitVoice(t, c)
	var opened atomic.Int32
	c.factory = func(_ context.Context, state voice.State, _ func(error, bool)) (voiceTransport, error) {
		if *state.ChannelID != 4 {
			t.Error("new credentials paired with old membership")
		}
		opened.Add(1)
		return newFakeVoiceTransport(), nil
	}
	c.update = func(context.Context, snowflake.ID, *snowflake.ID, bool, bool) error {
		c.HandleVoiceServerUpdate(serverEvent("new"))
		c.mu.Lock()
		connecting := c.phase == voiceConnecting
		c.mu.Unlock()
		if connecting {
			t.Error("connected before membership notification")
		}
		c.HandleVoiceStateUpdate(stateEvent(4))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Open(ctx, 4, false, true); err != nil {
		t.Fatal(err)
	}
	if opened.Load() != 1 {
		t.Fatal("unexpected connection count")
	}
	c.update = func(context.Context, snowflake.ID, *snowflake.ID, bool, bool) error { return nil }
}

func TestVoiceKeyWaitRetainsPacketAndHonorsCancellation(t *testing.T) {
	c := testVoiceController(t)
	transport := newFakeVoiceTransport()
	c.factory = func(context.Context, voice.State, func(error, bool)) (voiceTransport, error) { return transport, nil }
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("key"))
	waitVoice(t, c)
	transport.ready.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	if err := c.WriteAudio(ctx, []byte{1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait: %v", err)
	}
	cancel()
	if transport.writes.Load() != 0 || transport.invalid.Load() {
		t.Fatal("held frame escaped or connection discarded")
	}
	transport.ready.Store(true)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.WriteAudio(ctx, []byte{1}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if transport.writes.Load() != 1 || c.phase != voiceReady || !c.deadline.IsZero() {
		t.Fatal("keys did not restore sending")
	}
}
func (t *fakeVoiceTransport) Finish(context.Context) {}
func (t *fakeVoiceTransport) Close() {
	t.Invalidate()
	select {
	case <-t.closed:
	default:
		close(t.closed)
	}
}
func stateEvent(id snowflake.ID) gateway.EventVoiceStateUpdate {
	return gateway.EventVoiceStateUpdate{VoiceState: discord.VoiceState{GuildID: 1, UserID: 2, ChannelID: &id, SessionID: "session"}}
}
func serverEvent(token string) gateway.EventVoiceServerUpdate {
	endpoint := "endpoint"
	return gateway.EventVoiceServerUpdate{GuildID: 1, Token: token, Endpoint: &endpoint}
}
func testVoiceController(t *testing.T) *voiceConnection {
	t.Helper()
	c := newVoiceConnection(1, 2)
	c.update = func(context.Context, snowflake.ID, *snowflake.ID, bool, bool) error { return nil }
	c.remove = func() {}
	c.factory = func(context.Context, voice.State, func(error, bool)) (voiceTransport, error) {
		return newFakeVoiceTransport(), nil
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}
func waitVoice(t *testing.T, c *voiceConnection) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.waitReady(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceMoveEventPermutationsAndRoundTrips(t *testing.T) {
	orders := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	for _, code := range []int{4014, 4022} {
		for _, order := range orders {
			t.Run(fmt.Sprint(code, order), func(t *testing.T) {
				c := testVoiceController(t)
				c.HandleVoiceStateUpdate(stateEvent(3))
				c.HandleVoiceServerUpdate(serverEvent("initial"))
				waitVoice(t, c)
				for step, channel := range []snowflake.ID{4, 3, 4} {
					c.mu.Lock()
					old, gen := c.active.(*fakeVoiceTransport), c.generation
					c.mu.Unlock()
					for _, event := range order {
						switch event {
						case 0:
							c.HandleVoiceStateUpdate(stateEvent(channel))
						case 1:
							c.HandleVoiceServerUpdate(serverEvent(fmt.Sprint(step)))
						case 2:
							c.transportClosed(gen, &websocket.CloseError{Code: code}, false)
						}
					}
					waitVoice(t, c)
					if !old.invalid.Load() {
						t.Fatal("old transport still admits audio")
					}
					if id := c.ChannelID(); id == nil || *id != channel {
						t.Fatal("wrong destination")
					}
					if err := c.WriteAudio(context.Background(), []byte{1}); err != nil {
						t.Fatal(err)
					}
					c.transportClosed(gen, errors.New("late old close"), false)
					c.HandleVoiceServerUpdate(serverEvent("initial"))
					c.mu.Lock()
					phase := c.phase
					c.mu.Unlock()
					if phase != voiceReady {
						t.Fatal("late close damaged new generation")
					}
				}
			})
		}
	}
}

func TestVoiceDeadlineDuplicatesAndLateCompletion(t *testing.T) {
	c := testVoiceController(t)
	now := time.Now()
	c.now = func() time.Time { return now }
	started := make(chan struct{})
	release := make(chan struct{})
	transport := newFakeVoiceTransport()
	c.factory = func(context.Context, voice.State, func(error, bool)) (voiceTransport, error) {
		close(started)
		<-release
		return transport, nil
	}
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("a"))
	<-started
	c.mu.Lock()
	deadline := c.deadline
	c.mu.Unlock()
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("a"))
	c.mu.Lock()
	if c.deadline != deadline {
		t.Fatal("duplicate extended deadline")
	}
	now = deadline
	c.expireLocked()
	c.mu.Unlock()
	close(release)
	select {
	case <-transport.closed:
	case <-time.After(time.Second):
		t.Fatal("late completed transport leaked")
	}
	if err := c.waitReady(context.Background()); !errors.Is(err, errVoiceRecovery) {
		t.Fatal(err)
	}
}

func TestVoiceRemovalAndStopCannotResurrect(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			c := testVoiceController(t)
			started := make(chan struct{})
			release := make(chan struct{})
			transport := newFakeVoiceTransport()
			leaves := 0
			c.update = func(_ context.Context, _ snowflake.ID, id *snowflake.ID, _, _ bool) error {
				if id == nil {
					leaves++
				}
				return nil
			}
			c.factory = func(context.Context, voice.State, func(error, bool)) (voiceTransport, error) {
				close(started)
				<-release
				return transport, nil
			}
			c.HandleVoiceStateUpdate(stateEvent(3))
			c.HandleVoiceServerUpdate(serverEvent("a"))
			<-started
			if stop {
				c.Close(context.Background())
			} else {
				event := stateEvent(0)
				event.ChannelID = nil
				c.HandleVoiceStateUpdate(event)
			}
			close(release)
			select {
			case <-transport.closed:
			case <-time.After(time.Second):
				t.Fatal("stale attempt leaked")
			}
			c.HandleVoiceStateUpdate(stateEvent(4))
			c.HandleVoiceServerUpdate(serverEvent("late"))
			c.mu.Lock()
			phase := c.phase
			c.mu.Unlock()
			if phase != voiceFailed && phase != voiceClosed {
				t.Fatal("resurrected", phase)
			}
			if !stop && leaves != 0 {
				t.Fatal("external removal sent leave")
			}
		})
	}
}

func TestVoiceSupersededAttemptAndBlockedSend(t *testing.T) {
	c := testVoiceController(t)
	started := make(chan struct{})
	release := make(chan struct{})
	old := newFakeVoiceTransport()
	c.factory = func(_ context.Context, state voice.State, _ func(error, bool)) (voiceTransport, error) {
		if *state.ChannelID == 3 {
			close(started)
			<-release
			return old, nil
		}
		return newFakeVoiceTransport(), nil
	}
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("a"))
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.WriteAudio(ctx, []byte{1}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	c.HandleVoiceStateUpdate(stateEvent(4))
	c.HandleVoiceServerUpdate(serverEvent("b"))
	waitVoice(t, c)
	close(release)
	select {
	case <-old.closed:
	case <-time.After(time.Second):
		t.Fatal("superseded attempt leaked")
	}
	if old.writes.Load() != 0 {
		t.Fatal("sent to superseded transport")
	}
}

type resumableFakeTransport struct {
	*fakeVoiceTransport
	called  chan struct{}
	release chan struct{}
}

func (t *resumableFakeTransport) Resume(ctx context.Context) error {
	close(t.called)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.release:
		return nil
	}
}
func TestVoiceCrashResumesWithoutRejoining(t *testing.T) {
	c := testVoiceController(t)
	transport := &resumableFakeTransport{newFakeVoiceTransport(), make(chan struct{}), make(chan struct{})}
	c.factory = func(context.Context, voice.State, func(error, bool)) (voiceTransport, error) { return transport, nil }
	c.update = func(context.Context, snowflake.ID, *snowflake.ID, bool, bool) error {
		t.Error("resume requested guild rejoin")
		return nil
	}
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("a"))
	waitVoice(t, c)
	c.mu.Lock()
	gen := c.generation
	c.mu.Unlock()
	c.transportClosed(gen, &websocket.CloseError{Code: 4015}, true)
	<-transport.called
	c.mu.Lock()
	if c.phase != voiceRecovering || !c.resuming {
		t.Fatal("resume not tracked")
	}
	c.mu.Unlock()
	close(transport.release)
	waitVoice(t, c)
	c.mu.Lock()
	if c.generation != gen {
		t.Fatal("resume replaced transport")
	}
	c.mu.Unlock()
	c.update = func(context.Context, snowflake.ID, *snowflake.ID, bool, bool) error { return nil }
}

func TestVoiceTerminalFailureDoesNotRejoin(t *testing.T) {
	c := testVoiceController(t)
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("a"))
	waitVoice(t, c)
	c.mu.Lock()
	gen := c.generation
	c.mu.Unlock()
	c.transportClosed(gen, &websocket.CloseError{Code: 4021}, false)
	if !c.Failed() {
		t.Fatal("rate limit retried")
	}
	c.HandleVoiceServerUpdate(serverEvent("late"))
	if !c.Failed() {
		t.Fatal("late server update restarted failed connection")
	}
}
