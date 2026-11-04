package discordbot

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"asterinmusic/internal/audio"
	"asterinmusic/internal/music"
	"github.com/disgoorg/disgo/voice"
)

func TestConnectionFailureRetainsManualOccurrenceAndQueueOrder(t *testing.T) {
	s := autoSession()
	s.conn = &retainedVoiceConn{}
	s.channel = 3
	s.queue.Add(music.Requester{ID: "a"}, []music.Track{autoTrack("aaaaaaaaaaa"), autoTrack("bbbbbbbbbbb")}, "", "")
	s.queue.Add(music.Requester{ID: "b"}, []music.Track{autoTrack("ccccccccccc")}, "", "")
	s.playAudio = func(context.Context, voice.Conn, audio.Producer) error { return errVoiceRecovery }
	s.loop(make(chan struct{}))
	current, pending := s.queue.Snapshot()
	if current == nil || current.Track.URL != autoTrack("aaaaaaaaaaa").URL || len(pending) != 2 || !s.stopped || !s.recoveryHeld || !s.auto.enabled {
		t.Fatal("lost failed occurrence or settings")
	}
	s.stopped = false
	entry, automatic, ok := s.nextEntry()
	if !ok || automatic || entry.ID != current.ID {
		t.Fatal("did not resume same occurrence")
	}
	_, after := s.queue.Snapshot()
	if len(after) != len(pending) || after[0].ID != pending[0].ID {
		t.Fatal("queue reordered")
	}
}

func TestConnectionFailureRetainsAutomaticOccurrenceAcrossCheckpoint(t *testing.T) {
	s := autoSession()
	s.channel = 3
	expected := autoTrack("aaaaaaaaaaa")
	s.resumeAuto = &music.Entry{Track: expected}
	s.playAudio = func(context.Context, voice.Conn, audio.Producer) error { return errVoiceRemoved }
	s.loop(make(chan struct{}))
	if !s.recoveryHeld || s.resumeAuto == nil || !s.auto.enabled || !s.stopped {
		t.Fatal("automatic state lost")
	}
	b, err := s.checkpointBytesLocked()
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint playbackCheckpoint
	if err = json.Unmarshal(b, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Playing || !checkpoint.RecoveryHeld || checkpoint.AutomaticTrack == nil {
		t.Fatal("checkpoint would autoplay or lose held track")
	}
	restored := autoSession()
	restored.restoreSettings(&checkpoint)
	// A retained current track has priority even if waiting songs exist.
	restored.queue.Add(music.Requester{ID: "b"}, []music.Track{autoTrack("bbbbbbbbbbb")}, "", "")
	entry, automatic, ok := restored.nextEntry()
	if !ok || !automatic || entry.Track.URL != expected.URL {
		t.Fatal("held automatic occurrence lost")
	}
}

func TestExplicitStopDiscardsHeldCurrentButPreservesWaiting(t *testing.T) {
	s := autoSession()
	s.queue.Add(music.Requester{ID: "a"}, []music.Track{autoTrack("aaaaaaaaaaa"), autoTrack("bbbbbbbbbbb")}, "", "")
	entry, _ := s.queue.Start()
	s.current = &entry
	s.mu.Lock()
	defer s.mu.Unlock()
	s.suspendVoiceLocked()
	s.haltPlaybackLocked()
	current, pending := s.queue.Snapshot()
	if current != nil || len(pending) != 1 || s.recoveryHeld || s.auto.enabled {
		t.Fatal("stop did not discard held current")
	}
}

func TestManagedCheckpointDoesNotDiscardHeldManualTrack(t *testing.T) {
	s := autoSession()
	s.queue.Add(music.Requester{ID: "a"}, []music.Track{autoTrack("aaaaaaaaaaa"), autoTrack("bbbbbbbbbbb")}, "", "")
	entry, _ := s.queue.Start()
	s.current = &entry
	s.suspendVoiceLocked()
	if err := s.checkpoint(filepath.Join(t.TempDir(), "playback.json")); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.stopLocked() // App shutdown follows the successful managed checkpoint.
	s.mu.Unlock()
	current, pending := s.queue.Snapshot()
	if current == nil || current.ID != entry.ID || len(pending) != 1 {
		t.Fatal("checkpoint discarded held occurrence")
	}
}
