package discordbot

import (
	"asterinmusic/internal/music"
	"context"
	"errors"
	"testing"
	"time"
)

func TestPacketCompletionCannotReviveStoppedTrack(t *testing.T) {
	s := newSession(&App{ctx: context.Background()}, 1)
	s.current = &music.Entry{Track: autoTrack("abcdefghijk")}
	s.running = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.trackCancel = cancel
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- s.sendPacket(ctx, func() error { close(entered); <-release; return nil }) }()
	<-entered
	stopped := make(chan struct{})
	go func() { s.stop(); s.playbackStatus(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("network write held session mutex")
	}
	close(release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s.started || s.position != 0 || s.playbackSequence != 0 || len(s.auto.history) != 0 {
		t.Fatal("late write revived stopped state")
	}
}

func TestPacketFailureDoesNotStartPlayback(t *testing.T) {
	s := newSession(&App{ctx: context.Background()}, 1)
	s.current = &music.Entry{Track: autoTrack("abcdefghijk")}
	if err := s.sendPacket(context.Background(), func() error { return errDaveNotReady }); !errors.Is(err, errDaveNotReady) {
		t.Fatal(err)
	}
	if s.started || s.position != 0 {
		t.Fatal("failed packet started playback")
	}
	for range 2 {
		if err := s.sendPacket(context.Background(), func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if !s.started || s.position != 40*time.Millisecond || s.playbackSequence != 1 || len(s.auto.history) != 1 {
		t.Fatal("successful packet accounting wrong")
	}
}
