package discordbot

import (
	"asterinmusic/internal/music"
	"bytes"
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
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waitQueueLocked()
	if s.shuttingDown {
		return nil
	}
	// Explicit user termination wins over a later managed shutdown, even if
	// the playback goroutine has not finished its cancellation cleanup yet.
	if s.stopped {
		// Queue.Start may have selected a manual track before loop assigns
		// session.current. Stop ends that selection as well.
		if current, _ := s.queue.Snapshot(); current != nil {
			id := current.ID
			s.queueIOLocked(func() { s.queue.Finish(id) })
		}
	} else if s.skipping && s.current != nil && !s.currentAuto {
		id := s.current.ID
		s.queueIOLocked(func() { s.queue.Finish(id) })
	}
	if err := s.queue.PersistenceError(); err != nil {
		return err
	}
	epoch, revision := s.epoch, s.auto.revision
	b, err := s.checkpointBytesLocked()
	if err != nil {
		return err
	}
	prepare := s.prepareCheckpoint
	if prepare == nil {
		prepare = preparePlaybackCheckpoint
	}
	s.mu.Unlock()
	temporary, err := prepare(path, b)
	s.mu.Lock()
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	s.waitQueueLocked()
	if err := s.queue.PersistenceError(); err != nil {
		return err
	}
	latest, err := s.checkpointBytesLocked()
	if err != nil {
		return err
	}
	if s.epoch != epoch || s.auto.revision != revision || !bytes.Equal(latest, b) {
		return errors.New("playback changed during checkpoint; bot retained, retry restart")
	}
	if err := os.Rename(temporary, path); err != nil {
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

func (s *session) checkpointBytesLocked() ([]byte, error) {
	v := playbackCheckpoint{Version: 1, Guild: s.guild, Channel: s.channel, Text: s.text,
		Playing: !s.stopped && (s.running || s.connecting), Volume: s.volume.Percent(),
		AutoEnabled: s.auto.enabled, AutoOwner: s.auto.owner, Seed: s.auto.seed, History: s.auto.history}
	if s.currentAuto && s.auto.enabled && !s.skipping && !s.stopped {
		v.AutomaticTrack = s.current
	}
	if v.AutomaticTrack == nil && s.auto.enabled && !s.stopped {
		v.AutomaticTrack = s.resumeAuto
	}
	if !s.skipping && !s.stopped {
		v.RecordedPlayback = s.historyResume
		if s.current != nil && s.started {
			v.RecordedPlayback = &recordedPlayback{Entry: *s.current, Automatic: s.currentAuto}
		}
	}
	return json.Marshal(v)
}

// Write and sync the private temporary file outside the session mutex. Only
// the final rename is serialized with state validation and shutdown freeze.
func preparePlaybackCheckpoint(path string, b []byte) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".playback-*.tmp")
	if err != nil {
		return "", err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
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
