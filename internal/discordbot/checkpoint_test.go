package discordbot

import (
	"asterinmusic/internal/music"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPlaybackCheckpointRoundTrip(t *testing.T) {
	for _, playing := range []bool{true, false} {
		s := newSession(&App{ctx: context.Background()}, 1)
		s.channel = 2
		s.text = 3
		s.running = playing
		s.stopped = !playing
		s.volume.Set(42)
		s.auto.enabled = true
		s.auto.owner = music.Requester{ID: "a"}
		s.auto.seed = &music.Track{URL: "seed", Site: "youtube"}
		s.auto.history = []string{"previous"}
		s.currentAuto = true
		s.current = &music.Entry{Track: music.Track{URL: "automatic"}}
		path := filepath.Join(t.TempDir(), "state.json")
		if err := s.checkpoint(path); err != nil {
			t.Fatal(err)
		}
		s.auto.off()
		if err := s.checkpoint(path); err != nil {
			t.Fatal(err)
		} // idempotent
		v, err := readPlaybackCheckpoint(path, 1)
		if err != nil {
			t.Fatal(err)
		}
		if v.Playing != playing || v.Channel != 2 || v.Text != 3 {
			t.Fatal(v)
		}
		restored := newSession(&App{ctx: context.Background()}, 1)
		restored.restoreSettings(v)
		if restored.volume.Percent() != 42 || !restored.auto.enabled || restored.auto.seed.URL != "seed" || len(restored.auto.history) != 1 || restored.resumeAuto.Track.URL != "automatic" {
			t.Fatal("lost runtime state")
		}
		if restored.running || restored.channel != 0 {
			t.Fatal("restore settings connected before READY")
		}
		if _, err := readPlaybackCheckpoint(path, 99); err == nil {
			t.Fatal("wrong guild accepted")
		}
	}
}

func TestCheckpointFailureDoesNotStopPlayback(t *testing.T) {
	s := newSession(&App{ctx: context.Background()}, 1)
	s.running = true
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.checkpoint(filepath.Join(path, "state.json")); err == nil {
		t.Fatal("expected failure")
	}
	if s.shuttingDown || s.stopped || !s.running {
		t.Fatal("failed checkpoint stopped playback")
	}
}

func TestRestoredAutomaticTrackYieldsToManualQueue(t *testing.T) {
	for _, manual := range []bool{true, false} {
		s := newSession(&App{ctx: context.Background()}, 1)
		s.auto.enabled = true
		s.resumeAuto = &music.Entry{Track: music.Track{URL: "automatic"}}
		if manual {
			s.queue.Add(music.Requester{ID: "a"}, []music.Track{{URL: "manual"}}, "", "")
		}
		e, automatic, ok := s.nextEntry()
		if !ok || automatic == manual || (manual && e.Track.URL != "manual") || (!manual && e.Track.URL != "automatic") {
			t.Fatal(e, automatic, ok)
		}
	}
}
