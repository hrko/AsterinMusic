package discordbot

import (
	"asterinmusic/internal/music"
	"asterinmusic/internal/source"
	"context"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"time"
)

// All autoplay state is protected by session.mu. History survives off/on.
type autoplay struct {
	enabled        bool
	owner          music.Requester
	seed           *music.Track
	candidates     []music.Track
	history        []string
	revision       uint64
	seedGeneration uint64
	fetching       bool
	cancel         context.CancelFunc
	failures       int
	done           chan struct{}
	attempts       int
}

func (a *autoplay) invalidate() {
	if a.done != nil {
		close(a.done)
		a.done = nil
	}
	a.attempts = 0
	a.revision++
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	a.fetching = false
}
func (a *autoplay) off() {
	a.seedGeneration++
	a.invalidate()
	a.enabled = false
	a.seed = nil
	a.candidates = nil
	a.failures = 0
}
func (a *autoplay) on(owner music.Requester, current *music.Entry, automatic, started bool) {
	if a.enabled {
		return
	}
	a.enabled = true
	a.owner = owner
	a.failures = 0
	if current != nil && !automatic && started {
		a.setSeed(current.Track)
	}
}
func (a *autoplay) setSeed(t music.Track) {
	if t.Site != "youtube" && t.Site != "youtube_music" {
		return
	}
	a.invalidate()
	a.seedGeneration++
	a.candidates = nil
	copy := t
	a.seed = &copy
}
func (a *autoplay) started(t music.Track, automatic bool) {
	a.history = append(a.history, trackIdentity(t.URL))
	if len(a.history) > 50 {
		a.history = append([]string(nil), a.history[len(a.history)-50:]...)
	}
	if a.enabled && !automatic {
		a.setSeed(t)
	}
}
func (a *autoplay) take(pending []music.Entry) (music.Track, bool) {
	a.candidates = a.available(pending)
	if len(a.candidates) == 0 {
		return music.Track{}, false
	}
	t := a.candidates[0]
	a.candidates = a.candidates[1:]
	a.attempts = 0
	return t, true
}

// Return a selection displaced by manual work only while its seed still owns
// the candidate batch. Fetch cancellation alone does not change that ownership.
func (a *autoplay) putBack(t music.Track, generation uint64) {
	if a.enabled && a.seedGeneration == generation {
		a.candidates = append([]music.Track{t}, a.candidates...)
	}
}

// available returns a filtered copy without consuming scheduling state.
func (a *autoplay) available(pending []music.Entry) []music.Track {
	blocked := make(map[string]bool)
	for _, id := range a.history {
		blocked[id] = true
	}
	for _, e := range pending {
		blocked[trackIdentity(e.Track.URL)] = true
	}
	var result []music.Track
	for _, t := range a.candidates {
		if !blocked[trackIdentity(t.URL)] {
			result = append(result, t)
			blocked[trackIdentity(t.URL)] = true
		}
	}
	return result
}
func (a *autoplay) store(tracks []music.Track) {
	seen := make(map[string]bool)
	for _, t := range tracks {
		key := trackIdentity(t.URL)
		if !seen[key] {
			seen[key] = true
			a.candidates = append(a.candidates, t)
		}
	}
}
func (a *App) autoplayCommand(e *events.ApplicationCommandInteractionCreate, s *session) {
	action := "status"
	if sub := e.SlashCommandInteractionData().SubCommandName; sub != nil {
		action = *sub
	}
	if action != "status" {
		if _, err := s.authorize(e.User().ID); err != nil {
			a.edit(e.Token(), err.Error(), nil)
			return
		}
	}
	s.mu.Lock()
	if action != "status" {
		if err := s.mutableLocked(); err != nil {
			s.mu.Unlock()
			a.edit(e.Token(), userError(err), nil)
			return
		}
	}
	switch action {
	case "on":
		s.auto.on(music.Requester{ID: e.User().ID.String(), Name: e.User().Username}, s.current, s.currentAuto, s.started)
		s.prefetchLocked()
	case "off":
		s.auto.off()
		s.resumeAuto = nil
		if s.currentAuto && !s.started && s.trackCancel != nil {
			s.trackCancel()
		}
	}
	text := "自動再生をOFFにしました。"
	if action == "off" && s.current != nil && s.started {
		text += " 現在の曲はそのまま再生します。"
	}
	if action == "on" {
		text = "自動再生をONにしました。"
		if s.auto.seed == nil {
			text += " 起点となるYouTube曲の再生待ちです。"
		}
	}
	embed := discord.Embed{Title: "自動再生 OFF", Color: colorInfo}
	if s.auto.enabled {
		embed.Title = "自動再生 ON"
		if s.auto.seed == nil {
			embed.Description = "起点となるYouTube曲の再生待ちです。"
		} else {
			embed.Description = "起点：" + singleLink(*s.auto.seed)
			if s.auto.fetching {
				embed.Description += "\n候補を取得中です。"
			} else {
				embed.Description += "\n手動キューが空になると候補から再生します。"
			}
		}
	}
	s.mu.Unlock()
	if action == "status" {
		a.editEmbed(e.Token(), embed, nil)
	} else {
		a.edit(e.Token(), text, nil)
	}
}

// nextEntry is the sole scheduler. It releases the lock during network work,
// then rechecks manual priority and the generation before using any result.
func (s *session) nextEntry() (music.Entry, bool, bool) {
	for {
		s.mu.Lock()
		s.waitQueueLocked()
		if s.stopped || s.app.ctx.Err() != nil {
			s.running = false
			s.mu.Unlock()
			return music.Entry{}, false, false
		}
		var entry music.Entry
		var ok bool
		s.queueIOLocked(func() { entry, ok = s.queue.Start() })
		if ok {
			s.resumeAuto = nil
			s.mu.Unlock()
			return entry, false, true
		}
		if s.stopped || s.shuttingDown || s.app.ctx.Err() != nil {
			s.running = false
			s.mu.Unlock()
			return music.Entry{}, false, false
		}
		if s.resumeAuto != nil && s.auto.enabled {
			entry := *s.resumeAuto
			s.resumeAuto = nil
			s.selectionRevision = s.auto.revision
			s.selectionSeed = s.auto.seedGeneration
			s.mu.Unlock()
			return entry, true, true
		}
		if !s.auto.enabled || s.auto.seed == nil {
			s.running = false
			s.idleSince = time.Now()
			s.mu.Unlock()
			return music.Entry{}, false, false
		}
		_, pending := s.queue.Snapshot()
		if t, ok := s.auto.take(pending); ok {
			s.selectionRevision = s.auto.revision
			s.selectionSeed = s.auto.seedGeneration
			e := music.Entry{Track: t, Requester: s.auto.owner, AddedAt: time.Now()}
			s.mu.Unlock()
			return e, true, true
		}
		if s.auto.attempts >= 2 && !s.auto.fetching {
			s.auto.off()
			s.running = false
			s.idleSince = time.Now()
			text := s.text
			s.mu.Unlock()
			s.notifyFailure(text, nil, "自動再生の候補を取得できなかったためOFFにしました。", true)
			return music.Entry{}, false, false
		}
		s.fetchCandidatesLocked()
		done := s.auto.done
		s.idleSince = time.Time{}
		s.mu.Unlock()
		select {
		case <-done:
		case <-s.app.ctx.Done():
		}
	}
}

// Called with mu held; one speculative attempt per replenishment cycle.
func (s *session) prefetchLocked() {
	if s.stopped || s.shuttingDown || !s.started || !s.auto.enabled || s.auto.seed == nil || s.auto.attempts > 0 || s.auto.fetching {
		return
	}
	_, pending := s.queue.Snapshot()
	if len(pending) > 0 || len(s.auto.available(pending)) > 0 {
		return
	}
	s.fetchCandidatesLocked()
}

func (s *session) fetchCandidatesLocked() {
	if s.auto.fetching {
		return
	}
	ctx, cancel := context.WithTimeout(s.app.ctx, 45*time.Second)
	s.auto.fetching = true
	s.auto.cancel = cancel
	s.auto.done = make(chan struct{})
	s.auto.attempts++
	revision, url := s.auto.revision, s.auto.seed.URL
	fetch := s.fetchMix
	if fetch == nil {
		fetch = s.app.source.InspectMix
	}
	go func() {
		result, err := fetch(ctx, url)
		logOperationFailure("autoplay.fetch_candidates", err)
		cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if revision != s.auto.revision || !s.auto.enabled {
			return
		}
		s.auto.cancel = nil
		s.auto.fetching = false
		s.auto.store(result.Tracks)
		close(s.auto.done)
		s.auto.done = nil
	}()
}

// Keeps the network boundary injectable for scheduling/cancellation tests.
type mixFetcher func(context.Context, string) (source.Result, error)

func (a *App) onVoiceState(e *events.GuildVoiceStateUpdate) {
	if e.VoiceState.UserID != a.client.ID() {
		return
	}
	a.mu.Lock()
	s := a.sessions[e.VoiceState.GuildID]
	a.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channel == 0 {
		return
	}
	// Read the latest cache: asynchronous event listeners may arrive out of order.
	s.syncVoiceLocked()
}
