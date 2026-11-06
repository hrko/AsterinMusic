package discordbot

import (
	"asterinmusic/internal/audio"
	"asterinmusic/internal/music"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
)

func TestPlaybackRestartWaitsForPreviousLoop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "restart", true: "stop"}[stop], func(t *testing.T) {
			s := newSession(&App{ctx: context.Background()}, 1)
			s.conn = &retainedVoiceConn{}
			s.queue.Add(music.Requester{ID: "a"}, []music.Track{autoTrack("aaaaaaaaaaa")}, "", "")
			previous := make(chan struct{})
			s.done = previous
			started := make(chan struct{})
			s.playAudio = func(ctx context.Context, _ voice.Conn, _ audio.Producer) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}
			result := make(chan error, 1)
			go func() { s.mu.Lock(); defer s.mu.Unlock(); result <- s.startLocked(3) }()
			deadline := time.Now().Add(time.Second)
			for {
				s.mu.Lock()
				waiting := s.connecting
				s.mu.Unlock()
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("restart did not wait for old owner")
				}
				time.Sleep(time.Millisecond)
			}
			select {
			case <-started:
				t.Fatal("overlapping playback")
			default:
			}
			s.mu.Lock()
			if s.done != previous {
				t.Fatal("old completion replaced early")
			}
			if stop {
				s.haltPlaybackLocked()
			}
			s.mu.Unlock()
			close(previous)
			select {
			case err := <-result:
				if stop && !errors.Is(err, errQueueChanged) {
					t.Fatalf("stop ignored: %v", err)
				}
				if !stop && err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("restart stuck")
			}
			if stop {
				select {
				case <-started:
					t.Fatal("stopped request played")
				default:
				}
				return
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("new playback did not start")
			}
			s.mu.Lock()
			done := s.done
			s.haltPlaybackLocked()
			s.mu.Unlock()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("new loop did not finish")
			}
		})
	}
}
