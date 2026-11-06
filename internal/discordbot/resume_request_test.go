package discordbot

import (
	"asterinmusic/internal/audio"
	"asterinmusic/internal/config"
	"asterinmusic/internal/music"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

func resumeSession(t *testing.T) *session {
	t.Helper()
	channel := snowflake.ID(3)
	caches := cache.New(cache.WithCaches(cache.FlagVoiceStates))
	caches.AddVoiceState(discord.VoiceState{GuildID: 2, UserID: 1, ChannelID: &channel})
	a := &App{ctx: t.Context(), config: config.Config{GuildID: 2}, client: &bot.Client{Caches: caches}}
	authorizationFixture(t, a)
	s := newSession(a, 2)
	c := newVoiceConnection(2, 8)
	c.channel, c.phase = 3, voiceReady
	s.conn, s.channel = c, 3
	s.queue.Add(music.Requester{ID: "1"}, []music.Track{autoTrack("aaaaaaaaaaa")}, "", "")
	return s
}

func awaitResume(t *testing.T, r *resumeRequest, want resumeState) {
	t.Helper()
	select {
	case result := <-r.result:
		if result.state != want {
			t.Fatalf("result=%v want=%s", result, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("missing result %s", want)
	}
}

func TestResumeReportsSuccessOnlyAfterAudioAndSharesPreparation(t *testing.T) {
	s := resumeSession(t)
	entered, send, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.playAudio = func(ctx context.Context, _ voice.Conn, _ audio.Producer) error {
		close(entered)
		<-send
		if err := s.sendPacket(ctx, func() error { return nil }); err != nil {
			return err
		}
		<-release
		return nil
	}
	state, request, err := s.requestResume(1, 0)
	if err != nil || state.state != resumeAccepted || request == nil {
		t.Fatalf("accept: %v %v", state, err)
	}
	if s.running {
		t.Fatal("playback started before acceptance response")
	}
	repeated, duplicate, err := s.requestResume(1, 0)
	if err != nil || repeated.state != resumePreparing || duplicate != nil {
		t.Fatal("duplicate request created")
	}
	s.launchResume(request)
	s.launchResume(request)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("preparation not started")
	}
	select {
	case <-request.result:
		t.Fatal("reported success before audio")
	default:
	}
	close(send)
	awaitResume(t, request, resumeStarted)
	state, duplicate, err = s.requestResume(1, 0)
	if err != nil || state.state != resumePlaying || duplicate != nil {
		t.Fatal("playing status incorrect")
	}
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not finish")
	}
}

func TestResumeTerminalOutcomes(t *testing.T) {
	for _, mode := range []string{"stop", "empty", "source", "connection", "next_source"} {
		t.Run(mode, func(t *testing.T) {
			s := resumeSession(t)
			if mode == "next_source" {
				s.queue.Add(music.Requester{ID: "1"}, []music.Track{autoTrack("bbbbbbbbbbb")}, "", "")
			}
			calls := 0
			s.playAudio = func(ctx context.Context, _ voice.Conn, _ audio.Producer) error {
				calls++
				if mode == "connection" {
					return errVoiceRecovery
				}
				if mode == "next_source" && calls == 2 {
					return s.sendPacket(ctx, func() error { return nil })
				}
				return errors.New("source failed")
			}
			_, request, err := s.requestResume(1, 0)
			if err != nil {
				t.Fatal(err)
			}
			want := resumeSourceFailed
			if mode == "stop" {
				s.mu.Lock()
				s.haltPlaybackLocked()
				s.mu.Unlock()
				want = resumeCanceled
			}
			if mode == "empty" {
				s.queue.Clear()
				want = resumeEmpty
			}
			if mode == "connection" {
				want = resumeFailed
			}
			if mode == "next_source" {
				want = resumeStarted
			}
			s.launchResume(request)
			awaitResume(t, request, want)
			s.mu.Lock()
			done := s.done
			s.mu.Unlock()
			if done != nil {
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("loop did not finish")
				}
			}
			if mode == "stop" && calls != 0 {
				t.Fatal("canceled request started playback")
			}
		})
	}
}

func TestResumeMoveClosesResponseButKeepsPlayback(t *testing.T) {
	s := resumeSession(t)
	played := make(chan struct{})
	s.playAudio = func(ctx context.Context, _ voice.Conn, _ audio.Producer) error {
		if err := s.sendPacket(ctx, func() error { return nil }); err != nil {
			return err
		}
		close(played)
		return nil
	}
	_, request, err := s.requestResume(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := s.conn.(*voiceConnection)
	c.mu.Lock()
	c.channel = 4
	c.mu.Unlock()
	s.mu.Lock()
	s.moveVoiceLocked(4)
	s.mu.Unlock()
	awaitResume(t, request, resumeMoved)
	s.launchResume(request)
	select {
	case <-played:
	case <-time.After(time.Second):
		t.Fatal("move canceled accepted playback")
	}
	s.mu.Lock()
	done := s.done
	channel := s.channel
	s.mu.Unlock()
	<-done
	if channel != 4 {
		t.Fatal("playback moved back to original VC")
	}
	select {
	case <-request.result:
		t.Fatal("moved response received later playback details")
	default:
	}
}

func TestResumeConsecutiveSourceFailuresFinishResponseAndRetainRemainder(t *testing.T) {
	s := resumeSession(t)
	conn := s.conn
	s.queue.Add(music.Requester{ID: "1"}, []music.Track{
		autoTrack("bbbbbbbbbbb"), autoTrack("ccccccccccc"), autoTrack("ddddddddddd"),
	}, "", "")
	calls := 0
	s.playAudio = func(context.Context, voice.Conn, audio.Producer) error {
		calls++
		return errors.New("source failed before first audio")
	}
	_, request, err := s.requestResume(1, 0)
	if err != nil || request == nil {
		t.Fatalf("resume not accepted: %v", err)
	}
	s.launchResume(request)
	awaitResume(t, request, resumeSourceFailed)
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("source failure response did not terminate playback")
	}
	_, pending := s.queue.Snapshot()
	if calls != 3 || len(pending) != 1 || pending[0].Track.URL != autoTrack("ddddddddddd").URL {
		t.Fatalf("failure limit changed queue: calls=%d pending=%v", calls, pending)
	}
	if s.conn != conn || !s.stopped || s.running || s.resumeRequest != nil {
		t.Fatal("source failures changed connection or left preparation pending")
	}
	select {
	case <-request.result:
		t.Fatal("failure response was completed more than once")
	default:
	}
	// A fresh explicit request must use the retained connection and remaining
	// track, rather than requiring /stop or inheriting the failure counter.
	s.playAudio = func(ctx context.Context, _ voice.Conn, _ audio.Producer) error {
		return s.sendPacket(ctx, func() error { return nil })
	}
	_, retry, err := s.requestResume(1, 0)
	if err != nil || retry == nil {
		t.Fatalf("retry not accepted: %v", err)
	}
	s.launchResume(retry)
	awaitResume(t, retry, resumeStarted)
	s.mu.Lock()
	done = s.done
	s.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retry playback did not finish")
	}
}

func TestResumeQueueClearPreservesOnlyHeldCurrentTrack(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiting_only", true: "held_current"}[held], func(t *testing.T) {
			s := resumeSession(t)
			if held {
				s.queue.Start()
				s.recoveryHeld = true
				s.queue.Add(music.Requester{ID: "1"}, []music.Track{autoTrack("bbbbbbbbbbb")}, "", "")
			}
			conn, revision := s.conn, s.playbackRevision
			calls := 0
			s.playAudio = func(ctx context.Context, _ voice.Conn, _ audio.Producer) error {
				calls++
				if s.current.Track.URL != autoTrack("aaaaaaaaaaa").URL {
					t.Error("cleared pending track was played")
				}
				return s.sendPacket(ctx, func() error { return nil })
			}
			_, request, err := s.requestResume(1, 0)
			if err != nil || request == nil {
				t.Fatalf("resume acceptance: %v", err)
			}
			_, pending := s.queue.Snapshot()
			view := &queueEditor{owner: 1, guild: 2, voice: 3, sessionEpoch: s.epoch, expires: time.Now().Add(time.Minute), entries: pending}
			if err := s.clearConfirmedQueue(view); err != nil {
				t.Fatal(err)
			}
			if s.playbackRevision != revision || s.conn != conn {
				t.Fatal("clearing waiting queue acted as stop")
			}
			s.launchResume(request)
			want := resumeEmpty
			if held {
				want = resumeStarted
			}
			awaitResume(t, request, want)
			s.mu.Lock()
			done := s.done
			s.mu.Unlock()
			if done != nil {
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("resume did not finish")
				}
			}
			if (held && calls != 1) || (!held && calls != 0) {
				t.Fatalf("played %d tracks, held=%v", calls, held)
			}
		})
	}
}

func TestResumeStatusWithoutStartingAnotherOwner(t *testing.T) {
	for _, mode := range []resumeState{resumeEmpty, resumePreparing, resumeRecovering} {
		t.Run(string(mode), func(t *testing.T) {
			s := resumeSession(t)
			switch mode {
			case resumeEmpty:
				s.queue.Clear()
			case resumePreparing:
				s.running = true
			case resumeRecovering:
				s.conn.(*voiceConnection).phase = voiceRecovering
			}
			result, request, err := s.requestResume(1, 0)
			if err != nil || request != nil || result.state != mode {
				t.Fatalf("result=%v request=%v error=%v", result, request, err)
			}
		})
	}
}

func TestExternalLeaveCancelsAcceptedResume(t *testing.T) {
	s := resumeSession(t)
	_, request, err := s.requestResume(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.moveVoiceLocked(0)
	s.suspendVoiceLocked()
	s.mu.Unlock()
	awaitResume(t, request, resumeCanceled)
	s.launchResume(request)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running || s.connecting {
		t.Fatal("external leave resurrected playback")
	}
}

func TestIdleConnectionFailureDoesNotInventHeldTrack(t *testing.T) {
	s := resumeSession(t)
	s.queue.Clear()
	s.mu.Lock()
	s.suspendVoiceLocked()
	held := s.recoveryHeld
	s.mu.Unlock()
	if held {
		t.Fatal("idle failure invented a retained track")
	}
	result, request, err := s.requestResume(1, 0)
	if err != nil || request != nil || result.state != resumeEmpty {
		t.Fatalf("idle resume: %v %v", result, err)
	}
}

func TestResumeAcceptedBeforeFailedOldLoopFinishes(t *testing.T) {
	s := resumeSession(t)
	oldEntered, oldRelease := make(chan struct{}), make(chan struct{})
	calls := 0
	s.playAudio = func(ctx context.Context, _ voice.Conn, _ audio.Producer) error {
		calls++
		if calls == 1 {
			close(oldEntered)
			<-ctx.Done()
			<-oldRelease
			return errVoiceRecovery
		}
		return s.sendPacket(ctx, func() error { return nil })
	}
	s.mu.Lock()
	err := s.startLocked(3)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	<-oldEntered
	s.mu.Lock()
	s.suspendVoiceLocked()
	s.mu.Unlock()
	state, request, err := s.requestResume(1, 0)
	if err != nil || state.state != resumeAccepted {
		t.Fatalf("resume rejected: %v %v", state, err)
	}
	s.launchResume(request)
	close(oldRelease)
	awaitResume(t, request, resumeStarted)
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	<-done
	if calls != 2 {
		t.Fatalf("playback loops=%d", calls)
	}
}
