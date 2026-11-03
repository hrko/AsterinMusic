package discordbot

import (
	"asterinmusic/internal/music"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagedResumeRecordsPlaybackOnlyOnce(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		s := newSession(&App{ctx: context.Background()}, 1)
		s.auto.enabled = true
		s.running = true
		s.currentAuto = automatic
		entry := music.Entry{ID: "1", Track: autoTrack("abcdefghijk"), AddedAt: time.Now()}
		if automatic {
			entry.ID = ""
		}
		s.current = &entry
		s.recordStartedLocked()
		s.started = true
		path := filepath.Join(t.TempDir(), "playback.json")
		if err := s.checkpoint(path); err != nil {
			t.Fatal(err)
		}
		v, err := readPlaybackCheckpoint(path, 1)
		if err != nil {
			t.Fatal(err)
		}
		r := newSession(&App{ctx: context.Background()}, 1)
		r.restoreSettings(v)
		// A second restart before the first packet must keep the marker too.
		if err := r.checkpoint(path); err != nil {
			t.Fatal(err)
		}
		v, err = readPlaybackCheckpoint(path, 1)
		if err != nil {
			t.Fatal(err)
		}
		r = newSession(&App{ctx: context.Background()}, 1)
		r.restoreSettings(v)
		r.current, r.currentAuto = &entry, automatic
		r.recordStartedLocked()
		if len(r.auto.history) != 1 {
			t.Fatal("resume duplicated history", r.auto.history)
		}
		// The same video added again is a new playback occurrence.
		entry.ID = "2"
		entry.AddedAt = entry.AddedAt.Add(time.Second)
		r.current = &entry
		r.recordStartedLocked()
		if len(r.auto.history) != 2 {
			t.Fatal("new playback was suppressed", r.auto.history)
		}
	}
}

func TestCheckpointDiskWaitAllowsMutationAndRejectsStaleState(t *testing.T) {
	s := newSession(&App{ctx: context.Background()}, 1)
	path := filepath.Join(t.TempDir(), "playback.json")
	entered, release := make(chan struct{}), make(chan struct{})
	s.prepareCheckpoint = func(path string, b []byte) (string, error) {
		close(entered)
		<-release
		return preparePlaybackCheckpoint(path, b)
	}
	result := make(chan error, 1)
	go func() { result <- s.checkpoint(path) }()
	<-entered
	changed := make(chan struct{})
	go func() { s.setVolume(55); s.playbackStatus(); close(changed) }()
	select {
	case <-changed:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("checkpoint disk wait held state mutex")
	}
	close(release)
	if err := <-result; err == nil {
		t.Fatal("stale snapshot committed")
	}
	if s.shuttingDown || s.volume.Percent() != 55 {
		t.Fatal("failed checkpoint lost live mutation")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("stale checkpoint file published")
	}
	s.prepareCheckpoint = nil
	if err := s.checkpoint(path); err != nil {
		t.Fatal(err)
	}
	v, err := readPlaybackCheckpoint(path, 1)
	if err != nil || v.Volume != 55 {
		t.Fatal("retry did not preserve new state", err)
	}
}

func TestCheckpointExcludesExplicitlyEndedCurrent(t *testing.T) {
	for _, action := range []string{"skip", "stop"} {
		t.Run(action, func(t *testing.T) {
			s := newSession(&App{ctx: context.Background()}, 1)
			root := t.TempDir()
			qpath := filepath.Join(root, "queue.json")
			var err error
			s.queue, err = music.OpenQueue(500, qpath)
			if err != nil {
				t.Fatal(err)
			}
			s.queue.Add(music.Requester{ID: "a"}, []music.Track{{URL: "first"}, {URL: "second"}}, "", "")
			entry, _ := s.queue.Start()
			s.current = &entry
			s.running = true
			_, s.trackCancel = context.WithCancel(context.Background())
			if action == "skip" {
				if err := s.skip(); err != nil {
					t.Fatal(err)
				}
			} else {
				s.stop()
			}
			// Playback cleanup has not run when the management checkpoint arrives.
			if err := s.checkpoint(filepath.Join(root, "playback.json")); err != nil {
				t.Fatal(err)
			}
			q, err := music.OpenQueue(500, qpath)
			if err != nil {
				t.Fatal(err)
			}
			next, ok := q.Start()
			if !ok || next.Track.URL != "second" {
				t.Fatal("explicitly ended track restored", next)
			}
		})
	}
}

func TestCheckpointStopBetweenSelectionAndPlayback(t *testing.T) {
	s := newSession(&App{ctx: context.Background()}, 1)
	root := t.TempDir()
	qpath := filepath.Join(root, "queue.json")
	var err error
	s.queue, err = music.OpenQueue(500, qpath)
	if err != nil {
		t.Fatal(err)
	}
	s.queue.Add(music.Requester{ID: "1"}, []music.Track{autoTrack("abcdefghijk"), autoTrack("lmnopqrstuv")}, "", "")
	selected, automatic, ok := s.nextEntry()
	if !ok || automatic || s.current != nil {
		t.Fatal("expected manual selection before playback handoff")
	}
	if err := s.stop(); err != nil {
		t.Fatal(err)
	}
	if err := s.checkpoint(filepath.Join(root, "playback.json")); err != nil {
		t.Fatal(err)
	}
	restored, err := music.OpenQueue(500, qpath)
	if err != nil {
		t.Fatal(err)
	}
	current, pending := restored.Snapshot()
	if current != nil || len(pending) != 1 || pending[0].ID == selected.ID {
		t.Fatal("stopped selection returned after restore", current, pending)
	}
}

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
		if restored.volume.Percent() != 42 || !restored.auto.enabled || restored.auto.seed.URL != "seed" || len(restored.auto.history) != 1 {
			t.Fatal("lost runtime state")
		}
		if playing && (restored.resumeAuto == nil || restored.resumeAuto.Track.URL != "automatic") {
			t.Fatal("lost active automatic track")
		}
		if !playing && restored.resumeAuto != nil {
			t.Fatal("restored explicitly stopped automatic track")
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
