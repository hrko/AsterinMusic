package discordbot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"asterinmusic/internal/config"
	"asterinmusic/internal/history"
	"asterinmusic/internal/logging"
	"asterinmusic/internal/music"
	"asterinmusic/internal/runcontrol"
	"asterinmusic/internal/source"
	"asterinmusic/internal/updater"
	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

type App struct {
	logger           *slog.Logger
	historyViews     map[string]*historyView
	historyStore     *history.Store
	historyWriter    *history.Writer
	presenceRevision atomic.Uint64
	client           *bot.Client
	config           config.Config
	source           *source.Client
	ctx              context.Context
	mu               sync.Mutex
	sessions         map[snowflake.ID]*session
	offers           *offers
	ready            chan struct{}
	readyOnce        sync.Once
	daveClosers      sync.Map
}

func New(ctx context.Context, c config.Config, logger *slog.Logger) (*App, error) {
	executable := c.YTDLP
	if executable == "yt-dlp" {
		var err error
		executable, err = updater.New().Executable()
		if err != nil {
			return nil, errors.New("cannot select yt-dlp executable")
		}
	}
	if _, err := exec.LookPath(executable); err != nil {
		return nil, errors.New("yt-dlp executable unavailable")
	}
	if _, err := exec.LookPath(c.FFmpeg); err != nil {
		return nil, errors.New("ffmpeg executable unavailable")
	}
	a := &App{ctx: ctx, config: c, source: source.New(executable), sessions: make(map[snowflake.ID]*session), offers: newOffers(), ready: make(chan struct{})}
	a.logger = logging.Component(logger, "app")
	if c.YouTubeMetadataLanguage != "" {
		a.source.MetadataLanguage = c.YouTubeMetadataLanguage
	}
	if c.YouTubeDataEnabled {
		a.source.YouTubeData = source.NewYouTubeData(c.YouTubeDataAPIKey)
		a.source.YouTubeData.Logger = logging.Component(logger, "source")
	}
	if err := a.source.ConfigureAuth(c.NiconicoAuth); err != nil {
		return nil, err
	}
	opts := []bot.ConfigOpt{
		bot.WithLogger(logging.Dependency(logger, "discord")),
		// Voice Open waits for subsequent Gateway events; listeners must not
		// block the Gateway event dispatcher while resolving or connecting.
		bot.WithEventManagerConfigOpts(eventOptions()...),
		bot.WithGatewayConfigOpts(gateway.WithIntents(gateway.IntentGuilds | gateway.IntentGuildVoiceStates)),
		bot.WithCacheConfigOpts(cache.WithCaches(cache.FlagGuilds, cache.FlagChannels, cache.FlagVoiceStates, cache.FlagMembers)),
		bot.WithEventListenerFunc(func(e *events.Ready) { a.presenceRevision.Add(1); a.readyOnce.Do(func() { close(a.ready) }) }),
		bot.WithEventListenerFunc(func(e *events.Resumed) { a.presenceRevision.Add(1) }),
		bot.WithEventListenerFunc(a.onCommand), bot.WithEventListenerFunc(a.onComponent),
		bot.WithEventListenerFunc(a.onHistoryModal),
		bot.WithEventListenerFunc(a.onVoiceState),
	}
	opts = append(opts, daveOptions(a)...)
	opts = append(opts, a.voiceConnectionOptions())
	client, err := disgo.New(c.Token, opts...)
	if err != nil {
		return nil, errors.New("could not initialize Discord client")
	}
	a.client = client
	authCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	application, err := client.Rest.GetCurrentApplication(rest.WithCtx(authCtx))
	if err != nil {
		client.Close(authCtx)
		return nil, errors.New("application authentication failed")
	}
	if c.ApplicationID != 0 && c.ApplicationID != application.ID {
		client.Close(authCtx)
		return nil, errors.New("token belongs to a different application")
	}
	a.config.ApplicationID = application.ID
	a.logger = a.logger.With("bot_id", application.ID.String())
	return a, nil
}

func eventOptions() []bot.EventManagerConfigOpt {
	return []bot.EventManagerConfigOpt{bot.WithAsyncEventsEnabled()}
}

func (a *App) Close() {
	a.mu.Lock()
	sessions := make([]*session, 0, len(a.sessions))
	for _, s := range a.sessions {
		sessions = append(sessions, s)
	}
	a.mu.Unlock()
	for _, s := range sessions {
		s.mu.Lock()
		s.shuttingDown = true
		s.stopLocked()
		s.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, s := range sessions {
		s.mu.Lock()
		done, closing := s.done, s.closing
		s.mu.Unlock()
		for _, wait := range []chan struct{}{done, closing} {
			if wait == nil {
				continue
			}
			select {
			case <-wait:
			case <-ctx.Done():
				logging.OrDiscard(a.logger).WarnContext(ctx, "playback shutdown timed out", "event", "app.shutdown.timeout")
			}
		}
	}
	a.client.Close(ctx)
	a.closeAllDave()
	if a.historyWriter != nil {
		flushCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		a.logOperationFailure("history.shutdown", a.historyWriter.Shutdown(flushCtx))
		c()
	}
	if a.historyStore != nil {
		a.logOperationFailure("history.close", a.historyStore.Close())
	}
}

// Check only reads configured resources. Discord errors are deliberately not
// printed verbatim: REST errors may contain request details or user content.
func (a *App) Check() error {
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
	defer cancel()
	if a.client.ApplicationID != a.config.ApplicationID {
		return errors.New("token belongs to a different application")
	}
	if _, err := a.client.Rest.GetGuild(a.config.GuildID, false, rest.WithCtx(ctx)); err != nil {
		return errors.New("cannot access configured guild; check bot installation and guild ID")
	}
	// Read-only validation also runs in restart preflight, before the old Bot stops.
	if err := history.Check(filepath.Join(a.config.StateDir, "history.sqlite")); err != nil {
		return errors.New("cannot restore history database")
	}
	base := filepath.Join(a.config.StateDir, a.config.ApplicationID.String()+"-"+a.config.GuildID.String())
	if _, err := music.OpenQueue(a.config.QueueLimit, base+"-queue.json"); err != nil {
		return errors.New("cannot restore queue checkpoint")
	}
	if _, err := readPlaybackCheckpoint(base+"-playback.json", a.config.GuildID); err != nil {
		return errors.New("cannot restore playback checkpoint")
	}
	for _, target := range []struct {
		id    snowflake.ID
		voice bool
	}{{a.config.TextChannelID, false}, {a.config.VoiceChannelID, true}} {
		if target.id == 0 {
			continue
		}
		channel, err := a.client.Rest.GetChannel(target.id, rest.WithCtx(ctx))
		if err != nil {
			return errors.New("cannot access configured channel")
		}
		guildChannel, ok := channel.(discord.GuildChannel)
		if !ok || guildChannel.GuildID() != a.config.GuildID {
			return errors.New("configured channel does not belong to the configured guild")
		}
		if target.voice && channel.Type() != discord.ChannelTypeGuildVoice {
			return errors.New("voice test requires a normal guild voice channel")
		}
		if !target.voice && channel.Type() != discord.ChannelTypeGuildText {
			return errors.New("text test requires a guild text channel")
		}
	}
	return nil
}

func (a *App) Register() error {
	if err := a.Check(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
	defer cancel()
	// Upsert only AsterinMusic's commands; do not delete unrelated registrations.
	for _, cmd := range Commands() {
		if _, err := a.client.Rest.CreateGuildCommand(a.config.ApplicationID, a.config.GuildID, cmd, rest.WithCtx(ctx)); err != nil {
			return fmt.Errorf("could not register /%s: %s", cmd.CommandName(), failureClass(err))
		}
	}
	return nil
}

func (a *App) connect() error {
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	if err := a.client.OpenGateway(ctx); err != nil {
		return errors.New("Discord Gateway connection failed")
	}
	select {
	case <-a.ready:
		return nil
	case <-ctx.Done():
		return errors.New("Discord READY timeout")
	}
}

func (a *App) Serve() error {
	if !daveEnabled {
		return errors.New("voice build required: use mise run serve")
	}
	stateDir := a.config.StateDir
	store, err := history.Open(filepath.Join(stateDir, "history.sqlite"), a.config.HistoryLimit)
	if err != nil {
		return fmt.Errorf("open history: %w", err)
	}
	a.historyStore = store
	a.historyWriter = history.NewWriter(store.Append, func(err error) { a.logOperationFailure("history.write", err) })
	queue, err := music.OpenQueue(a.config.QueueLimit, filepath.Join(stateDir, a.config.ApplicationID.String()+"-"+a.config.GuildID.String()+"-queue.json"))
	if err != nil {
		return fmt.Errorf("restore queue: %w", err)
	}
	s := a.getSession(a.config.GuildID)
	s.queue = queue // Before the Gateway can dispatch commands.
	queue.SetLogger(s.logger)
	checkpointPath := filepath.Join(stateDir, a.config.ApplicationID.String()+"-"+a.config.GuildID.String()+"-playback.json")
	restored, err := readPlaybackCheckpoint(checkpointPath, s.guild)
	if err != nil {
		return fmt.Errorf("restore playback: %w", err)
	}
	if restored != nil {
		s.restoreSettings(restored)
	}
	runcontrol.ObservePlayback(a.ctx, s.playbackStatus)
	runcontrol.BeforeStop(a.ctx, func() error {
		err := s.checkpoint(checkpointPath)
		if err != nil {
			logging.OrDiscard(a.logger).ErrorContext(a.ctx, "playback checkpoint failed; refusing stop", "event", "state.checkpoint.failed", "reason", failureClass(err), "action", "retain_playback")
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			a.logOperationFailure("history.flush", a.historyWriter.Flush(ctx))
			cancel()
		}
		return err
	})
	_, pending := queue.Snapshot()
	logging.OrDiscard(a.logger).InfoContext(a.ctx, "manual queue restored", "event", "state.queue.restored", "tracks", len(pending), "guild_id", a.config.GuildID.String())
	if err := a.Check(); err != nil {
		return err
	}
	if err := a.connect(); err != nil {
		return err
	}
	if restored != nil {
		if err := os.Remove(checkpointPath); err != nil {
			return fmt.Errorf("consume playback checkpoint: %w", err)
		}
		if os.Getenv("ASTERINMUSIC_RESUME") == "true" {
			if err := s.resumeCheckpoint(restored); err != nil {
				logging.OrDiscard(a.logger).ErrorContext(a.ctx, "playback restore could not reconnect", "event", "state.playback.restore_failed", "reason", failureClass(err), "queue_retained", true)
			} else {
				logging.OrDiscard(a.logger).InfoContext(a.ctx, "playback state restored", "event", "state.playback.restored", "playing", restored.Playing, "autoplay", restored.AutoEnabled)
			}
		}
	}
	logging.OrDiscard(a.logger).InfoContext(a.ctx, "AsterinMusic connected; waiting for commands", "event", "app.ready", "dave", daveBackend)
	runcontrol.Ready(a.ctx)
	go a.checkUpdates()
	go a.runPresence()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return nil
		case <-ticker.C:
			a.tick()
		}
	}
}

func (a *App) getSession(guild snowflake.ID) *session {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.sessions[guild]; s != nil {
		return s
	}
	s := newSession(a, guild)
	a.sessions[guild] = s
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func (a *App) tick() {
	a.offers.prune(time.Now())
	a.mu.Lock()
	list := make([]*session, 0, len(a.sessions))
	for _, s := range a.sessions {
		list = append(list, s)
	}
	a.mu.Unlock()
	for _, s := range list {
		s.tick()
	}
}
