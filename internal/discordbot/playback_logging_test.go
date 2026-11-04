package discordbot

import (
	"asterinmusic/internal/logging"
	"asterinmusic/internal/music"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestPlaybackLogsStartOnlyAfterSuccessfulPacket(t *testing.T) {
	var out logBuffer
	l, _ := logging.New(&out, logging.Options{Format: "json"})
	s := newSession(&App{ctx: t.Context(), logger: l}, 1)
	s.current = &music.Entry{Track: autoTrack("abcdefghijk")}
	s.channel = 3
	p := newPlaybackLog(t.Context(), s.logger, "occurrence", false)
	s.playbackLog = p
	if err := s.sendPacket(t.Context(), func() error { return errors.New("fail") }); err == nil {
		t.Fatal("expected packet failure")
	}
	if len(p.started) != 0 || out.String() != "" {
		t.Fatal("failed packet logged a start")
	}
	for range 3 {
		if err := s.sendPacket(t.Context(), func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	p.join()
	p.finish(t.Context(), slog.LevelInfo, "completed", "advance", s.position)
	text := out.String()
	if strings.Count(text, `"event":"playback.started"`) != 1 || strings.Count(text, `"playback_id":"occurrence"`) != 2 || !strings.Contains(text, `"channel_id":"3"`) {
		t.Fatalf("incorrect playback correlation: %s", text)
	}
}

type blockingLogWriter struct{ entered, release chan struct{} }

func (w blockingLogWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return len(p), nil
}

func TestPlaybackLoggingDoesNotBlockAudioFrames(t *testing.T) {
	w := blockingLogWriter{make(chan struct{}), make(chan struct{})}
	l, _ := logging.New(w, logging.Options{})
	s := newSession(&App{ctx: context.Background(), logger: l}, 1)
	s.current = &music.Entry{Track: autoTrack("abcdefghijk")}
	p := newPlaybackLog(t.Context(), s.logger, "occurrence", false)
	s.playbackLog = p
	defer p.join()
	defer close(w.release)
	if err := s.sendPacket(t.Context(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("missing start log")
	}
	sent := make(chan error, 1)
	go func() { sent <- s.sendPacket(t.Context(), func() error { return nil }) }()
	select {
	case err := <-sent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("log writer blocked frame transmission")
	}
}
