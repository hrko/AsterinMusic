package discordbot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"asterinmusic/internal/audio"
	"asterinmusic/internal/music"
	"asterinmusic/internal/runcontrol"
	"asterinmusic/internal/source"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

type session struct {
	failureNotice                      failureNotice
	playbackSequence                   uint64
	queueViewMu                        sync.Mutex
	mu                                 sync.Mutex
	app                                *App
	guild, channel, text               snowflake.ID
	queue                              *music.Queue
	volume                             *audio.Volume
	conn                               voice.Conn
	running, stopped, skipping         bool
	shuttingDown                       bool
	epoch                              uint64
	trackCancel                        context.CancelFunc
	done                               chan struct{}
	idleSince, emptySince              time.Time
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
	s := &session{app: a, guild: guild, queue: music.NewQueue(a.config.QueueLimit), volume: audio.NewVolume(48000)}
	if a.config.QueueLimit > 0 {
		_ = s.volume.Set(a.config.DefaultVolume)
	}
	return s
}

func (s *session) authorize(user snowflake.ID) (snowflake.ID, error) {
	state, ok := s.app.client.Caches.VoiceState(s.guild, user)
	if !ok || state.ChannelID == nil {
		return 0, errors.New("音声チャンネルへ参加してください。")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channel != 0 && s.channel != *state.ChannelID {
		return 0, errors.New("Botと同じ音声チャンネルで操作してください。")
	}
	return *state.ChannelID, nil
}

func (s *session) version() uint64 { s.mu.Lock(); defer s.mu.Unlock(); return s.epoch }

func (s *session) accept(user snowflake.ID, who music.Requester, tracks []music.Track, playlist, importID string, text snowflake.ID, epoch uint64) (int, error) {
	n, _, err := s.acceptAt(user, who, tracks, playlist, importID, text, epoch, music.Insertion{})
	return n, err
}
func (s *session) acceptAt(user snowflake.ID, who music.Requester, tracks []music.Track, playlist, importID string, text snowflake.ID, epoch uint64, position music.Insertion) (int, string, error) {
	channel, err := s.authorize(user)
	if err != nil {
		return 0, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shuttingDown {
		return 0, "", errors.New("再起動処理中です。")
	}
	if s.epoch != epoch {
		return 0, "", errors.New("取得中に停止・キュー消去が行われました。もう一度追加してください。")
	}
	if s.channel != 0 && s.channel != channel {
		return 0, "", errors.New("音声チャンネルが変更されました。")
	}
	if s.stopped && s.running {
		return 0, "", errors.New("停止処理中です。少し待って再実行してください。")
	}
	n, entryID, err := s.queue.AddAt(who, tracks, playlist, importID, position)
	if err != nil {
		return 0, "", err
	}
	if len(tracks) > 0 && n == 0 {
		return 0, "", errors.New("キューが満杯です。")
	}
	if err := s.queue.PersistenceError(); err != nil {
		return n, entryID, errors.New("キューはメモリ上に保持していますが、保存に失敗しました。再起動前にログと保存先を確認してください。")
	}
	s.text = text
	if n > 0 {
		if s.auto.fetching {
			s.auto.invalidate()
		}
		if s.currentAuto && !s.started && s.trackCancel != nil {
			s.trackCancel()
		}
	}
	if !s.running {
		if err := s.startLocked(channel); err != nil {
			return n, entryID, err
		}
	}
	return n, entryID, nil
}

func (s *session) startLocked(channel snowflake.ID) error {
	_, pending := s.queue.Snapshot()
	if len(pending) == 0 && s.resumeAuto == nil && (!s.auto.enabled || s.auto.seed == nil) {
		return errors.New("キューは空です。")
	}
	if s.conn == nil {
		conn := s.app.client.VoiceManager.CreateConn(s.guild)
		ctx, cancel := context.WithTimeout(s.app.ctx, 25*time.Second)
		err := conn.Open(ctx, channel, false, true)
		cancel()
		if err != nil {
			slog.Warn("voice connection failed", "error", s.app.safeError(err))
			closeCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
			conn.Close(closeCtx)
			s.app.closeDave(conn)
			c()
			return errors.New("音声接続に失敗しました。追加した曲はキューに保持しています。")
		}
		s.conn = conn
		s.channel = channel
	}
	s.stopped = false
	s.running = true
	s.idleSince = time.Time{}
	s.done = make(chan struct{})
	go s.loop(s.done)
	return nil
}

func (s *session) loop(done chan struct{}) {
	defer close(done)
	failures := 0
	for {
		entry, automatic, ok := s.nextEntry()
		if !ok {
			return
		}
		s.mu.Lock()
		if s.stopped || s.app.ctx.Err() != nil {
			if !automatic && !s.shuttingDown && s.app.ctx.Err() == nil {
				s.queue.Finish(entry.ID)
			}
			s.running = false
			s.mu.Unlock()
			return
		}
		if automatic {
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
		s.trackCancel = cancel
		s.position = 0
		s.skipping = false
		conn, text := s.conn, s.text
		s.mu.Unlock()
		play := s.playAudio
		if play == nil {
			play = s.playTrack
		}
		err := play(ctx, conn, func(ctx context.Context, w io.Writer) error {
			return s.app.source.BufferedStream(ctx, entry.Track.URL, w)
		})
		cancel()
		s.mu.Lock()
		if automatic && !s.started && !s.skipping && !s.stopped && !s.shuttingDown && s.app.ctx.Err() == nil &&
			(errors.Is(err, context.Canceled) || errors.Is(err, errSuperseded)) {
			_, pending := s.queue.Snapshot()
			if len(pending) > 0 {
				s.auto.putBack(entry.Track, s.selectionSeed)
			}
		}
		if !automatic && !s.shuttingDown && s.app.ctx.Err() == nil {
			s.queue.Finish(entry.ID)
		}
		s.current = nil
		s.started = false
		s.trackCancel = nil
		stopped, skipped := s.stopped || s.shuttingDown || s.app.ctx.Err() != nil, s.skipping || errors.Is(err, errSuperseded) || (automatic && errors.Is(err, context.Canceled))
		if stopped {
			s.running = false
			s.mu.Unlock()
			return
		}
		if err != nil && !skipped {
			slog.Warn("playback failed", "position", s.position, "error", s.app.safeError(err))
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
				s.notifyFailure(text, &entry.Track, "自動追加曲が3曲続けて失敗したため、自動再生をOFFにしました。", true)
				continue
			}
		}
		if !automatic && err == nil {
			s.auto.failures = 0
		}
		if failures >= 3 {
			s.stopLocked()
			s.running = false
			s.mu.Unlock()
			s.notifyFailure(text, &entry.Track, "3曲続けて再生に失敗したため停止しました。残りのキューは保持しています。", true)
			return
		}
		s.mu.Unlock()
		if err != nil && !skipped {
			s.notifyFailure(text, &entry.Track, source.AuthMessage(err, "曲の取得または再生に失敗しました。")+"\n次の曲へ進みます。", false)
		}
	}
}

func (s *session) playTrack(ctx context.Context, conn voice.Conn, produce audio.Producer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	first := true
	next := time.Now()
	err := audio.StreamOpus(ctx, s.app.config.FFmpeg, produce, s.volume, func(packet []byte) error {
		if first {
			if err := conn.SetSpeaking(ctx, voice.SpeakingFlagMicrophone); err != nil {
				return err
			}
			first = false
			next = time.Now()
		}
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
		err := waitDave(ctx, func() error {
			s.mu.Lock()
			if s.current != nil && !s.started && s.currentAuto {
				_, pending := s.queue.Snapshot()
				if !s.auto.enabled || s.auto.revision != s.currentRevision || len(pending) > 0 {
					s.mu.Unlock()
					return errSuperseded
				}
			}
			if ctx.Err() != nil {
				s.mu.Unlock()
				return ctx.Err()
			}
			if _, err := conn.UDP().Write(packet); err != nil {
				s.mu.Unlock()
				return err
			}
			if s.current != nil && !s.started {
				s.started = true
				s.playbackSequence++
				s.recordStartedLocked()
				s.prefetchLocked()
			}
			s.position += 20 * time.Millisecond
			s.mu.Unlock()
			return nil
		})
		if err != nil {
			return err
		}
		now := time.Now()
		next = next.Add(20 * time.Millisecond)
		if next.Before(now) {
			next = now.Add(20 * time.Millisecond)
		}
		return nil
	}, s.channelBitrate)
	cancel()
	stopCtx, c := context.WithTimeout(context.Background(), 2*time.Second)
	defer c()
	// Flush the Opus decoder before clearing speaking, as Discord recommends.
	for i := 0; i < 5; i++ {
		if _, e := conn.UDP().Write(voice.SilenceAudioFrame); e != nil {
			break
		}
	}
	_ = conn.SetSpeaking(stopCtx, voice.SpeakingFlagNone)
	return err
}

func (s *session) playbackStatus() runcontrol.Playback {
	s.mu.Lock()
	defer s.mu.Unlock()
	return runcontrol.Playback{Active: !s.stopped && (s.running || s.current != nil), Started: s.started, Sequence: s.playbackSequence}
}

var diagnosticURL = regexp.MustCompile(`https?://[^\s]+`)
var errSuperseded = errors.New("automatic candidate superseded")
var errDaveNotReady = errors.New("DAVE key exchange in progress")

// Retry the same encoded frame without holding the session mutex. Epoch
// changes must not advance the track or send unencrypted audio.
func waitDave(ctx context.Context, send func() error) error {
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := send()
		if !errors.Is(err, errDaveNotReady) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return errors.New("DAVE key exchange timed out")
		case <-timer.C:
		}
	}
}

func (a *App) safeError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if a.config.Token != "" {
		s = strings.ReplaceAll(s, a.config.Token, "[token]")
	}
	return truncate(diagnosticURL.ReplaceAllString(s, "[URL]"), 1200)
}

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
	s.auto.off()
	s.resumeAuto = nil
	s.epoch++
	s.stopped = true
	if s.trackCancel != nil {
		s.trackCancel()
	}
	conn := s.conn
	s.conn = nil
	s.channel = 0
	s.emptySince = time.Time{}
	s.idleSince = time.Time{}
	if conn != nil {
		ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		conn.Close(ctx)
		s.app.closeDave(conn)
		c()
	}
}

func (s *session) skip() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mutableLocked(); err != nil {
		return err
	}
	if s.trackCancel == nil {
		return errors.New("再生中の曲はありません。")
	}
	s.skipping = true
	s.trackCancel()
	return nil
}

func (s *session) tick() {
	s.mu.Lock()
	if s.conn == nil {
		s.mu.Unlock()
		return
	}
	if state, ok := s.app.client.Caches.VoiceState(s.guild, s.app.client.ID()); !ok || state.ChannelID == nil || *state.ChannelID != s.channel {
		s.stopLocked()
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
