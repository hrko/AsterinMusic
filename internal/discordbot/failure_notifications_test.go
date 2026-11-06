package discordbot

import (
	"asterinmusic/internal/audio"
	"asterinmusic/internal/music"
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/disgo/voice"
)

func TestPlaybackCompletionDoesNotWaitForFailureNotification(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	var once sync.Once
	transport := historyTransport(func(r *http.Request) (*http.Response, error) {
		once.Do(func() { close(entered) })
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	a := &App{ctx: ctx, client: &bot.Client{Rest: rest.New(rest.NewClient("test", rest.WithHTTPClient(&http.Client{Transport: transport})))}}
	s := newSession(a, 1)
	s.text, s.channel = 7, 3
	s.queue.Add(music.Requester{ID: "a"}, []music.Track{autoTrack("aaaaaaaaaaa")}, "", "")
	s.playAudio = func(context.Context, voice.Conn, audio.Producer) error { return errVoiceRecovery }
	done := make(chan struct{})
	go s.loop(done)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("notification not attempted")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("playback completion waited for REST")
	}
	for range 1000 {
		s.enqueueFailure(7, nil, "fixture", true)
	}
	s.mu.Lock()
	bounded := len(s.failureQueue) <= failureNotificationCapacity && s.failureNotificationsDropped > 0
	workerDone := s.failureWorkerDone
	s.mu.Unlock()
	if !bounded {
		t.Fatal("notification backlog is not bounded")
	}
	cancel()
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("notification worker did not stop")
	}
}

func TestDeferredFailureNotificationRejectsObsoleteDestinationOrPlayback(t *testing.T) {
	// A REST call would panic: both snapshots must be rejected before sending.
	s := &session{app: &App{ctx: t.Context()}, text: 7, playbackSequence: 2}
	oldSequence := uint64(1)
	s.publishFailure(t.Context(), 7, nil, "old playback", true, &oldSequence)
	currentSequence := uint64(2)
	s.publishFailure(t.Context(), 6, nil, "old VC", true, &currentSequence)
}
