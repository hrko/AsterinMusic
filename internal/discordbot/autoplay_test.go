package discordbot

import (
	"context"
	"errors"
	"fmt"
	"github.com/disgoorg/disgo/voice"
	"rainmusic/internal/audio"
	"rainmusic/internal/music"
	"rainmusic/internal/source"
	"testing"
	"time"
)

func autoTrack(id string) music.Track {
	return music.Track{URL: "https://www.youtube.com/watch?v=" + id, Title: id, Site: "youtube"}
}
func autoSession() *session {
	s := newSession(&App{ctx: context.Background()}, 1)
	s.auto.on(music.Requester{ID: "owner", Name: "owner"}, nil, false, false)
	s.auto.started(autoTrack("abcdefghijk"), false)
	s.running = true
	return s
}

func TestAutoplaySeedAndHistory(t *testing.T) {
	var a autoplay
	seed := autoTrack("abcdefghijk")
	entry := music.Entry{Track: seed}
	a.on(music.Requester{ID: "one"}, &entry, false, false)
	if a.seed != nil {
		t.Fatal("unstarted seed")
	}
	a.started(seed, false)
	if a.seed == nil || a.seed.URL != seed.URL {
		t.Fatal("manual seed missing")
	}
	revision := a.revision
	a.on(music.Requester{ID: "two"}, nil, false, false)
	if a.revision != revision || a.owner.ID != "one" {
		t.Fatal("repeated on reset state")
	}
	a.started(autoTrack("bcdefghijkl"), true)
	a.started(music.Track{URL: "https://www.nicovideo.jp/watch/sm9", Site: "niconico"}, false)
	if a.seed.URL != seed.URL {
		t.Fatal("automatic/nico moved seed")
	}
	a.candidates = []music.Track{seed}
	a.started(autoTrack("cdefghijklm"), false)
	if len(a.candidates) != 0 || a.seed.Title != "cdefghijklm" {
		t.Fatal("old candidates retained")
	}
	for i := 0; i < 60; i++ {
		a.started(autoTrack(fmt.Sprint(i)), true)
	}
	if len(a.history) != 50 || a.history[0] != "10" {
		t.Fatal(a.history)
	}
	a.off()
	if a.enabled || a.seed != nil || len(a.history) != 50 {
		t.Fatal("off clears history")
	}
	a.on(music.Requester{}, &entry, false, true)
	if a.seed == nil {
		t.Fatal("current manual not used")
	}
}

func TestAutoplayCandidatesDeduplicateAndExclude(t *testing.T) {
	s := autoSession()
	b, c := autoTrack("bcdefghijkl"), autoTrack("cdefghijklm")
	alias := b
	alias.URL = "https://music.youtube.com/watch?v=bcdefghijkl"
	s.auto.store([]music.Track{autoTrack("abcdefghijk"), b, alias, c})
	if len(s.auto.candidates) != 3 {
		t.Fatal("duplicate retained")
	}
	got, ok := s.auto.take([]music.Entry{{Track: b}})
	if !ok || got.URL != c.URL {
		t.Fatal(got, ok)
	}
}

func TestAutoplayManualPriorityAndRetryBound(t *testing.T) {
	s := autoSession()
	manual := autoTrack("bcdefghijkl")
	s.queue.Add(music.Requester{ID: "person"}, []music.Track{manual}, "", "")
	s.auto.store([]music.Track{autoTrack("cdefghijklm")})
	e, automatic, ok := s.nextEntry()
	if !ok || automatic || e.Track.URL != manual.URL {
		t.Fatal(e, automatic, ok)
	}
	s.queue.Finish(e.ID)
	e, automatic, ok = s.nextEntry()
	if !ok || !automatic || e.Requester.ID != "owner" {
		t.Fatal(e, automatic, ok)
	}
	calls := 0
	s.fetchMix = func(context.Context, string) (source.Result, error) {
		calls++
		return source.Result{}, errors.New("unavailable")
	}
	_, _, ok = s.nextEntry()
	if ok || calls != 2 || s.auto.enabled || s.running {
		t.Fatal(ok, calls, s.auto)
	}
}

func TestAutoplayFetchOffDiscardsLateResult(t *testing.T) {
	s := autoSession()
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan bool, 1)
	s.fetchMix = func(ctx context.Context, _ string) (source.Result, error) {
		close(entered)
		<-release
		return source.Result{Tracks: []music.Track{autoTrack("bcdefghijkl")}}, nil
	}
	go func() { _, _, ok := s.nextEntry(); done <- ok }()
	<-entered
	s.mu.Lock()
	s.auto.off()
	s.mu.Unlock()
	close(release)
	select {
	case ok := <-done:
		if ok {
			t.Fatal("late result played")
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler blocked")
	}
}

func TestAutoplayFetchManualArrivalAndSeedChange(t *testing.T) {
	s := autoSession()
	entered := make(chan struct{})
	done := make(chan music.Entry, 1)
	s.fetchMix = func(ctx context.Context, _ string) (source.Result, error) {
		close(entered)
		<-ctx.Done()
		return source.Result{Tracks: []music.Track{autoTrack("cdefghijklm")}}, nil
	}
	go func() {
		e, automatic, _ := s.nextEntry()
		if automatic {
			e.ID = "wrong-auto"
		}
		done <- e
	}()
	<-entered
	manual := autoTrack("bcdefghijkl")
	s.mu.Lock()
	s.queue.Add(music.Requester{ID: "manual"}, []music.Track{manual}, "", "")
	s.auto.invalidate()
	s.mu.Unlock()
	select {
	case e := <-done:
		if e.Track.URL != manual.URL || e.ID == "wrong-auto" {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("manual addition blocked")
	}
	s.mu.Lock()
	s.auto.started(manual, false)
	if len(s.auto.candidates) != 0 {
		t.Fatal("stale result retained")
	}
	s.mu.Unlock()
}

func TestAutoplayPartialFetchAndWaiting(t *testing.T) {
	s := autoSession()
	s.fetchMix = func(context.Context, string) (source.Result, error) {
		return source.Result{Tracks: []music.Track{autoTrack("bcdefghijkl")}, Partial: true}, errors.New("partial")
	}
	_, automatic, ok := s.nextEntry()
	if !ok || !automatic {
		t.Fatal("partial rejected")
	}
	s = autoSession()
	s.auto.seed = nil
	_, _, ok = s.nextEntry()
	if ok || s.idleSince.IsZero() {
		t.Fatal("seed waiting must become idle")
	}
}

func TestAutoplayThreeFailuresPreserveManualWork(t *testing.T) {
	s := autoSession()
	s.auto.store([]music.Track{autoTrack("bcdefghijkl"), autoTrack("cdefghijklm"), autoTrack("defghijklmn")})
	calls := 0
	manual := autoTrack("efghijklmno")
	s.playAudio = func(context.Context, voice.Conn, audio.Producer) error {
		calls++
		if calls <= 3 {
			if calls == 3 {
				s.queue.Add(music.Requester{ID: "person"}, []music.Track{manual}, "", "")
			}
			return errors.New("failed")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.currentAuto || s.auto.enabled || s.current.Track.URL != manual.URL {
			t.Error("manual work lost after automatic failures")
		}
		s.stopLocked()
		return nil
	}
	s.loop(make(chan struct{}))
	if calls != 4 {
		t.Fatal(calls)
	}
}

func TestAutoplayStopCancelsFetch(t *testing.T) {
	s := autoSession()
	entered := make(chan struct{})
	done := make(chan bool, 1)
	s.fetchMix = func(ctx context.Context, _ string) (source.Result, error) {
		close(entered)
		<-ctx.Done()
		return source.Result{Tracks: []music.Track{autoTrack("bcdefghijkl")}}, nil
	}
	go func() { _, _, ok := s.nextEntry(); done <- ok }()
	<-entered
	s.stop()
	select {
	case ok := <-done:
		if ok || s.auto.enabled {
			t.Fatal("stopped result accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel")
	}
}
