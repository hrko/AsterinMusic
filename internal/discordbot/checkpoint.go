package discordbot

import (
	"asterinmusic/internal/music"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/disgoorg/snowflake/v2"
	"os"
	"path/filepath"
)

type playbackCheckpoint struct {
	Version          int
	Guild            snowflake.ID
	Channel, Text    snowflake.ID
	Playing          bool
	Volume           int
	AutoEnabled      bool
	AutoOwner        music.Requester
	Seed             *music.Track
	History          []string
	AutomaticTrack   *music.Entry
	RecordedPlayback *recordedPlayback `json:",omitempty"`
}

// Identify a playback occurrence, not just a video. Manual re-additions have
// distinct entry IDs; automatic entries are distinguished by AddedAt.
type recordedPlayback struct {
	Entry     music.Entry
	Automatic bool
}

func (s *session) recordStartedLocked() {
	r := s.historyResume
	s.historyResume = nil
	if r != nil && r.Automatic == s.currentAuto && r.Entry.ID == s.current.ID &&
		r.Entry.Track.URL == s.current.Track.URL && r.Entry.AddedAt.Equal(s.current.AddedAt) {
		return
	}
	s.auto.started(s.current.Track, s.currentAuto)
}

// Called by the authenticated lifecycle endpoint before app cancellation.
func (s *session) checkpoint(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waitQueueLocked()
	if s.shuttingDown {
		return nil
	}
	// Explicit user termination wins over a later managed shutdown, even if
	// the playback goroutine has not finished its cancellation cleanup yet.
	if (s.skipping || s.stopped) && s.current != nil && !s.currentAuto {
		id := s.current.ID
		s.queueIOLocked(func() { s.queue.Finish(id) })
	}
	if err := s.queue.PersistenceError(); err != nil {
		return err
	}
	v := playbackCheckpoint{Version: 1, Guild: s.guild, Channel: s.channel, Text: s.text,
		Playing: !s.stopped && (s.running || s.connecting), Volume: s.volume.Percent(),
		AutoEnabled: s.auto.enabled, AutoOwner: s.auto.owner, Seed: s.auto.seed, History: s.auto.history}
	if s.currentAuto && s.auto.enabled && !s.skipping && !s.stopped {
		v.AutomaticTrack = s.current
	}
	if !s.skipping && !s.stopped {
		v.RecordedPlayback = s.historyResume
		if s.current != nil && s.started {
			v.RecordedPlayback = &recordedPlayback{Entry: *s.current, Automatic: s.currentAuto}
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".playback-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	// Freeze queue selection and additions only after the checkpoint succeeds.
	s.shuttingDown = true
	if s.connectCancel != nil {
		s.connectCancel()
	}
	s.stopped = true
	s.epoch++
	if s.trackCancel != nil {
		s.trackCancel()
	}
	s.auto.invalidate()
	return nil
}

func readPlaybackCheckpoint(path string, guild snowflake.ID) (*playbackCheckpoint, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v playbackCheckpoint
	if err = json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	if v.Version != 1 || v.Guild != guild || v.Volume < 0 || v.Volume > 100 || len(v.History) > 50 {
		return nil, fmt.Errorf("invalid playback checkpoint; keep file for recovery")
	}
	return &v, nil
}

// Apply before Gateway dispatch starts; reconnect only after READY.
func (s *session) restoreSettings(v *playbackCheckpoint) {
	s.volume.Set(v.Volume)
	s.text = v.Text
	s.auto.enabled = v.AutoEnabled
	s.auto.owner = v.AutoOwner
	s.auto.seed = v.Seed
	s.auto.history = append([]string(nil), v.History...)
	s.historyResume = v.RecordedPlayback
	if v.AutoEnabled {
		s.resumeAuto = v.AutomaticTrack
	}
}

func (s *session) resumeCheckpoint(v *playbackCheckpoint) error {
	if v == nil || !v.Playing || v.Channel == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Do not override a user action received after READY.
	if s.running || s.stopped || s.epoch != 0 {
		return nil
	}
	return s.startLocked(v.Channel)
}
