package discordbot

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/disgoorg/disgo/voice"
	"rainmusic/internal/audio"
	"rainmusic/internal/music"
)

func TestShutdownRetainsManualTrackButStopConsumesIt(t *testing.T) {
	for _, shutdown := range []bool{true, false} {
		t.Run(map[bool]string{true: "shutdown", false: "stop"}[shutdown], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := newSession(&App{ctx: ctx}, 1)
			path := filepath.Join(t.TempDir(), "queue.json")
			var err error
			s.queue, err = music.OpenQueue(500, path)
			if err != nil {
				t.Fatal(err)
			}
			s.queue.Add(music.Requester{ID: "a"}, []music.Track{{URL: "first"}, {URL: "second"}}, "", "")
			s.playAudio = func(context.Context, voice.Conn, audio.Producer) error {
				if shutdown {
					cancel()
				} else {
					s.stop()
				}
				return context.Canceled
			}
			s.running = true
			s.loop(make(chan struct{}))
			restored, err := music.OpenQueue(500, path)
			if err != nil {
				t.Fatal(err)
			}
			next, ok := restored.Start()
			want := "second"
			if shutdown {
				want = "first"
			}
			if !ok || next.Track.URL != want {
				t.Fatalf("got %v, want %s", next, want)
			}
		})
	}
}
