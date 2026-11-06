package discordbot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"asterinmusic/internal/audio"
	"asterinmusic/internal/authorization"
	"asterinmusic/internal/history"
	"asterinmusic/internal/logging"
	"asterinmusic/internal/music"
	"asterinmusic/internal/runcontrol"
	"asterinmusic/internal/source"
	"crypto/rand"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

type session struct {
	restore                            *restoreTrace
	playbackLog                        *playbackLog
	logger                             *slog.Logger
	historySessionID                   string
	playbackHistory                    *history.Record
	failureNotice                      failureNotice
	playbackSequence                   uint64
	queueViewMu                        sync.Mutex
	checkpointMu                       sync.Mutex
	prepareCheckpoint                  func(string, []byte) (string, error)
	mu                                 sync.Mutex
	app                                *App
	guild, channel, text               snowflake.ID
	accessVoice                        snowflake.ID
	queue                              *music.Queue
	volume                             *audio.Volume
	conn                               voice.Conn
	running, stopped, skipping         bool
	shuttingDown                       bool
	epoch                              uint64
	trackCancel                        context.CancelFunc
	done                               chan struct{}
	closing                            chan struct{}
	queueSaving                        chan struct{}
	connecting                         bool
	connectCancel                      context.CancelFunc
	createConn                         func() voice.Conn
	idleSince, emptySince              time.Time
	recoveryHeld                       bool
	replaying                          bool
	position                           time.Duration
	auto                               autoplay
	current                            *music.Entry
	resumeAuto                         *music.Entry
	historyResume                      *recordedPlayback
	currentAuto, started               bool
	selectionRevision, currentRevision uint64
	selectionSeed                      uint64
	fetchMix                           mixFetcher
	playAudio                          func(context.Context, voice.Conn, audio.Producer) error
}

func newSession(a *App, guild snowflake.ID) *session {
	s := &session{historySessionID: rand.Text(), app: a, guild: guild, queue: music.NewQueue(a.config.QueueLimit), volume: audio.NewVolume(48000)}
	s.logger = logging.Component(a.logger, "playback").With("guild_id", guild.String())
	if a.config.QueueLimit > 0 {
		_ = s.volume.Set(a.config.DefaultVolume)
	}
	return s
}

func (s *session) authorize(user snowflake.ID) (snowflake.ID, error) {
	state, ok := s.app.client.Caches.VoiceState(s.guild, user)
	if !ok || state.ChannelID == nil {
		return 0, errJoinVoice
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		s.syncVoiceLocked()
	}
	if s.channel != 0 && s.channel != *state.ChannelID {
		return 0, errSameVoice
	}
	return *state.ChannelID, nil
}

func (s *session) version() uint64 { s.mu.Lock(); defer s.mu.Unlock(); return s.epoch }

func (s *session) accept(user snowflake.ID, who music.Requester, tracks []music.Track, playlist, importID string, text snowflake.ID, epoch uint64) (int, error) {
	n, _, err := s.acceptAt(user, who, tracks, playlist, importID, text, epoch, music.Insertion{})
	return n, err
}
func (s *session) acceptAt(user snowflake.ID, who music.Requester, tracks []music.Track, playlist, importID string, text snowflake.ID, epoch uint64, position music.Insertion) (int, string, error) {
	action := authorization.QueueAdd
	if position.Front {
		action = authorization.QueueAddNext
	}
	if len(tracks) == 0 {
		action = authorization.PlaybackResume
	}
	channel, err := s.authorize(user)
	if err != nil {
		return 0, "", err
	}
	if err := s.lockAuthorized(user, action); err != nil {
		return 0, "", err
	}
	defer s.mu.Unlock()
	if err := s.mutableLocked(); err != nil {
		return 0, "", err
	}
	if s.epoch != epoch {
		return 0, "", errQueueChanged
	}
	if s.channel != 0 && s.channel != channel {
		return 0, "", errVoiceChanged
	}
	if len(tracks) == 0 && s.connecting {
		return 0, "", errConnecting
	}
	if len(tracks) == 0 && (s.closing != nil || (s.stopped && s.running)) {
		return 0, "", errStopping
	}
	// An addition accepted during an existing start/recovery belongs to that
	// owner, even if it fails while queue persistence releases the lock.
	startOwned := s.running || s.connecting || s.closing != nil
	var n int
	var entryID string
	var saveErr error
	s.queueIOLocked(func() {
		if scopeErr := s.queue.SetAccessScope(channel.String()); scopeErr != nil {
			err = errQueueSave
			saveErr = scopeErr
			return
		}
		n, entryID, err = s.queue.AddAt(who, tracks, playlist, importID, position)
		saveErr = s.queue.PersistenceError()
	})
	if err != nil {
		return 0, "", err
	}
	if len(tracks) > 0 && n == 0 {
		return 0, "", errQueueFull
	}
	if saveErr != nil {
		return n, entryID, errQueueSave
	}
	if s.epoch != epoch || s.shuttingDown {
		return n, entryID, errQueueChanged
	}
	s.text = text
	s.accessVoice = channel
	if n > 0 {
		if s.auto.fetching {
			s.auto.invalidate()
		}
		if s.currentAuto && !s.started && s.trackCancel != nil {
			s.trackCancel()
		}
	}
	if !startOwned && !s.running && !s.connecting && s.closing == nil {
		if err := s.startLocked(channel); err != nil {
			return n, entryID, err
		}
	}
	return n, entryID, nil
}

func (s *session) startLocked(channel snowflake.ID) error {
	if err := s.mutableLocked(); err != nil {
		return err
	}
	if s.connecting {
		return errConnecting
	}
	if s.closing != nil {
		return errStopping
	}
	if err := s.waitPreviousPlaybackLocked(); err != nil {
		return err
	}
	_, pending := s.queue.Snapshot()
	if len(pending) == 0 && !s.recoveryHeld && s.resumeAuto == nil && (!s.auto.enabled || s.auto.seed == nil) {
		return errQueueEmpty
	}
	needsOpen := s.conn == nil
	if c, ok := s.conn.(*voiceConnection); ok {
		needsOpen = c.Failed()
	}
	if needsOpen {
		ctx, cancel := context.WithTimeout(s.app.ctx, voiceRecoveryTimeout)
		ctx = s.restoreContext(ctx)
		s.connectCancel = cancel
		s.connecting = true
		s.stopped = false
		s.channel = channel
		done := make(chan struct{})
		s.closing = done
		epoch := s.epoch
		create := s.createConn
		if create == nil {
			create = func() voice.Conn { return s.app.client.VoiceManager.CreateConn(s.guild) }
		}
		conn := s.conn
		s.mu.Unlock()
		if conn == nil {
			conn = create()
		}
		_, managed := conn.(*voiceConnection)
		s.mu.Lock()
		if managed && !s.stopped && !s.shuttingDown {
			s.conn = conn
		}
		s.mu.Unlock()
		err := conn.Open(ctx, channel, false, true)
		cancel()
		s.mu.Lock()
		stale := (s.stopped && !s.recoveryHeld) || s.shuttingDown || (!managed && s.epoch != epoch) || s.app.ctx.Err() != nil
		s.connecting = false
		s.connectCancel = nil
		if err != nil || stale {
			if managed && !stale {
				s.stopped = true
				s.closing = nil
				close(done)
				return errVoiceConnection
			}
			s.conn = nil
			s.channel = 0
			s.mu.Unlock()
			s.logOperationFailure("voice.connect", err)
			closeCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
			conn.Close(closeCtx)
			s.app.closeDave(conn)
			c()
			s.mu.Lock()
			if s.closing == done {
				s.closing = nil
			}
			close(done)
			if stale {
				return errQueueChanged
			}
			return errVoiceConnection
		}
		if s.closing == done {
			s.closing = nil
		}
		close(done)
		s.conn = conn
		if managed {
			if id := conn.ChannelID(); id != nil {
				s.moveVoiceLocked(*id)
			}
		} else {
			s.channel = channel
		}
	}
	s.stopped = false
	s.running = true
	s.idleSince = time.Time{}
	s.done = make(chan struct{})
	s.restore.prepare()
	go s.loop(s.done)
	return nil
}

func (s *session) loop(done chan struct{}) {
	defer close(done)
	s.mu.Lock()
	restore := s.restore
	s.mu.Unlock()
	defer restore.finish("stopped_before_audio", context.Canceled)
	failures := 0
	for {
		entry, automatic, ok := s.nextEntry()
		if !ok {
			return
		}
		s.mu.Lock()
		s.waitQueueLocked()
		if s.stopped || s.app.ctx.Err() != nil {
			if automatic && s.recoveryHeld {
				copy := entry
				s.resumeAuto = &copy
			}
			if !automatic && !s.recoveryHeld && !s.shuttingDown && s.app.ctx.Err() == nil {
				s.queueIOLocked(func() { s.queue.Finish(entry.ID) })
			}
			s.running = false
			s.mu.Unlock()
			return
		}
		if automatic && !s.replaying {
			_, pending := s.queue.Snapshot()
			if !s.auto.enabled || s.auto.revision != s.selectionRevision || len(pending) > 0 {
				if len(pending) > 0 {
					s.auto.putBack(entry.Track, s.selectionSeed)
				}
				s.mu.Unlock()
				continue
			}
		}
		s.current = &entry
		s.currentAuto = automatic
		s.started = false
		s.currentRevision = s.auto.revision
		ctx, cancel := context.WithCancel(s.app.ctx)
		ctx = s.restoreContext(ctx)
		playLog := newPlaybackLog(ctx, s.logger, rand.Text(), automatic, s.restore)
		s.playbackLog = playLog
		s.trackCancel = cancel
		s.position = 0
		s.skipping = false
		conn := s.conn
		s.mu.Unlock()
		play := s.playAudio
		if play == nil {
			play = s.playTrack
		}
		err := play(ctx, conn, func(ctx context.Context, w io.Writer) error {
			if err := s.app.source.BufferedStream(ctx, entry.Track.URL, w); err != nil {
				return &playbackStageError{stage: "source", err: err}
			}
			return nil
		})
		playLog.join()
		if restore != nil {
			if err != nil {
				restore.finish("preparation_failed", err)
			} else {
				restore.finish("no_audio", errors.New("restored playback produced no audio"))
			}
		}
		cancel()
		s.mu.Lock()
		s.waitQueueLocked()
		text := s.text
		position := s.position
		s.playbackLog = nil
		if !s.skipping && !s.shuttingDown && s.app.ctx.Err() == nil && (s.recoveryHeld || errors.Is(err, errVoiceRecovery) || errors.Is(err, errVoiceRemoved)) {
			s.suspendVoiceLocked()
			s.current = nil
			s.started = false
			s.trackCancel = nil
			s.running = false
			s.mu.Unlock()
			playLog.finish(s.app.ctx, slog.LevelError, "voice_connection_failed", "hold_playback", position)
			s.notifyFailure(text, &entry.Track, "音声接続を復旧できなかったため、再生を停止しました。曲とキューは保持しています。/playで再開できます。", true)
			return
		}
		if automatic && !s.started && !s.skipping && !s.stopped && !s.shuttingDown && s.app.ctx.Err() == nil &&
			(errors.Is(err, context.Canceled) || errors.Is(err, errSuperseded)) {
			_, pending := s.queue.Snapshot()
			if len(pending) > 0 {
				s.auto.putBack(entry.Track, s.selectionSeed)
			}
		}
		if !automatic && !s.shuttingDown && s.app.ctx.Err() == nil {
			s.queueIOLocked(func() { s.queue.Finish(entry.ID) })
		}
		s.current = nil
		s.started = false
		s.trackCancel = nil
		stopped, skipped := s.stopped || s.shuttingDown || s.app.ctx.Err() != nil, s.skipping || errors.Is(err, errSuperseded) || (automatic && errors.Is(err, context.Canceled))
		if stopped {
			s.running = false
			s.mu.Unlock()
			playLog.finish(s.app.ctx, slog.LevelInfo, "stopped", "stop", position)
			return
		}
		if err != nil && !skipped {
			failures++
		} else {
			failures = 0
		}
		if automatic {
			failures = 0
			if s.auto.enabled && s.currentRevision == s.auto.revision && !skipped {
				if err != nil {
					s.auto.failures++
				} else {
					s.auto.failures = 0
				}
			}
			if s.auto.enabled && s.auto.failures >= 3 {
				s.auto.off()
				s.mu.Unlock()
				playLog.finish(s.app.ctx, slog.LevelError, failureClass(err), "disable_autoplay", position, err)
				s.notifyFailure(text, &entry.Track, "自動追加曲が3曲続けて失敗したため、自動再生をOFFにしました。", true)
				continue
			}
		}
		if !automatic && err == nil {
			s.auto.failures = 0
		}
		if failures >= 3 {
			s.haltPlaybackLocked()
			s.running = false
			s.mu.Unlock()
			playLog.finish(s.app.ctx, slog.LevelError, failureClass(err), "stop_retain_queue", position, err)
			s.notifyFailure(text, &entry.Track, "3曲続けて再生に失敗したため、再生を停止しました。\n残りのキューは保持しています。/playで再開できます。", true)
			return
		}
		s.mu.Unlock()
		if skipped {
			playLog.finish(s.app.ctx, slog.LevelInfo, "skipped", "advance", position)
		} else if err != nil {
			playLog.finish(s.app.ctx, slog.LevelWarn, failureClass(err), "advance", position, err)
		} else {
			playLog.finish(s.app.ctx, slog.LevelInfo, "completed", "advance", position)
		}
		if err != nil && !skipped {
			s.notifyFailure(text, &entry.Track, source.AuthMessage(err, "曲の取得または再生に失敗しました。")+"\n次の曲へ進みます。", false)
		}
	}
}

func (s *session) playTrack(ctx context.Context, conn voice.Conn, produce audio.Producer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c, ok := conn.(*voiceConnection)
	if !ok {
		return errors.New("unmanaged voice connection")
	}
	next := time.Now()
	s.mu.Lock()
	logger := s.logger
	if s.playbackLog != nil {
		logger = s.playbackLog.logger
	}
	s.mu.Unlock()
	err := audio.StreamOpusWithOptions(ctx, s.app.config.FFmpeg, produce, s.volume, func(packet []byte) error {
		if wait := time.Until(next); !audio.StreamPaced && wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		err := s.sendPacket(ctx, func() error { return c.WriteAudio(ctx, packet) })
		if err != nil {
			return err
		}
		now := time.Now()
		next = next.Add(20 * time.Millisecond)
		if next.Before(now) {
			next = now.Add(20 * time.Millisecond)
		}
		return nil
	}, audio.StreamOptions{Bitrate: s.channelBitrate, Logger: logger})
	cancel()
	stopCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	// Flush the Opus decoder before clearing speaking, as Discord recommends.
	c.FinishAudio(stopCtx)
	return err
}

func (s *session) playbackStatus() runcontrol.Playback {
	s.mu.Lock()
	defer s.mu.Unlock()
	return runcontrol.Playback{Active: !s.stopped && (s.connecting || s.running || s.current != nil), Started: s.started, Sequence: s.playbackSequence}
}

var errSuperseded = errors.New("automatic candidate superseded")
var errDaveNotReady = errors.New("DAVE key exchange in progress")

func (s *session) stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mutableLocked(); err != nil {
		return err
	}
	s.stopLocked()
	return nil
}

func (s *session) stopLocked() {
	if c, ok := s.conn.(*voiceConnection); ok {
		c.stopRecovery()
	}
	s.haltPlaybackLocked()
	conn := s.conn
	s.conn = nil
	s.channel = 0
	s.emptySince = time.Time{}
	s.idleSince = time.Time{}
	if conn != nil {
		done := make(chan struct{})
		s.closing = done
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn.Close(ctx)
			s.app.closeDave(conn)
			s.mu.Lock()
			s.closing = nil
			close(done)
			s.mu.Unlock()
		}()
	}
}

// Stop scheduling and playback without leaving the voice channel.
func (s *session) haltPlaybackLocked() {
	s.restore.finish("stopped", context.Canceled)
	if s.recoveryHeld && !s.shuttingDown {
		current, _ := s.queue.Snapshot()
		if current != nil {
			s.queueIOLocked(func() { s.queue.Finish(current.ID) })
		}
		s.recoveryHeld = false
	}
	if s.connectCancel != nil {
		s.connectCancel()
	}
	s.auto.off()
	s.resumeAuto = nil
	s.epoch++
	s.stopped = true
	if s.trackCancel != nil {
		s.trackCancel()
	}
	s.idleSince = time.Now()
}

func (s *session) skip() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mutableLocked(); err != nil {
		return err
	}
	if s.trackCancel == nil {
		return errNotPlaying
	}
	s.skipping = true
	s.trackCancel()
	return nil
}

func (s *session) tick() {
	s.mu.Lock()
	connection, _ := s.conn.(*voiceConnection)
	s.mu.Unlock()
	if connection != nil {
		defer connection.flushDiagnostics()
	}
	s.mu.Lock()
	if s.conn == nil {
		s.mu.Unlock()
		return
	}
	if !s.syncVoiceLocked() {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	human := false
	for state := range s.app.client.Caches.VoiceStates(s.guild) {
		if state.ChannelID == nil || *state.ChannelID != s.channel || state.UserID == s.app.client.ID() {
			continue
		}
		member, ok := s.app.client.Caches.Member(s.guild, state.UserID)
		if !ok || !member.User.Bot {
			human = true
			break
		}
	}
	if human {
		s.emptySince = time.Time{}
	} else if s.emptySince.IsZero() {
		s.emptySince = now
	}
	shouldStop := s.shouldDisconnect(now)
	if shouldStop {
		s.stopLocked()
	}
	s.mu.Unlock()
}

func (s *session) shouldDisconnect(now time.Time) bool {
	return (s.app.config.DisconnectWhenIdle && !s.idleSince.IsZero() && now.Sub(s.idleSince) >= time.Minute) ||
		(!s.emptySince.IsZero() && now.Sub(s.emptySince) >= time.Minute)
}
