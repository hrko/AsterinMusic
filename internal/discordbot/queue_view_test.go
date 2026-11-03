package discordbot

import (
	"asterinmusic/internal/music"
	"asterinmusic/internal/source"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"
)

func TestQueueViewTotalsModesAndNoConsumption(t *testing.T) {
	s := autoSession()
	sec := 65.0
	tracks := make([]music.Track, 12)
	for i := range tracks {
		tracks[i] = autoTrack(fmt.Sprintf("%011d", i))
		tracks[i].Duration = &sec
	}
	tracks[0].Duration = nil
	s.queue.Add(music.Requester{ID: "123", Name: "must not display"}, tracks, "", "")
	e, _ := s.queueView(123, 2, "")
	if !strings.Contains(e.Footer.Text, "待機12曲\n既知分11分55秒／時間不明1曲") || !strings.Contains(e.Description, "11. [") || !strings.Contains(e.Description, "<@123>") {
		t.Fatalf("unexpected view: %+v", e)
	}
	s.auto.store(tracks)
	s.queue.Clear()
	e, _ = s.queueView(123, 2, "manual")
	if !strings.HasPrefix(e.Footer.Text, "1ページ目") || strings.Contains(e.Footer.Text, "合計") || !strings.Contains(e.Description, "次の候補 · YouTube") {
		t.Fatal("mode transition failed")
	}
	if len(s.auto.candidates) != 12 {
		t.Fatal("view consumed candidates")
	}
	e, _ = s.queueView(123, 99, "auto")
	if !strings.HasPrefix(e.Footer.Text, "2ページ目") {
		t.Fatal("page not clamped")
	}
	s.auto.off()
	e, _ = s.queueView(123, 2, "auto")
	if !strings.HasPrefix(e.Footer.Text, "1 / 1ページ") {
		t.Fatal("empty view")
	}
}

func TestQueueViewLimitsAndEscaping(t *testing.T) {
	s := autoSession()
	tracks := make([]music.Track, 10)
	for i := range tracks {
		tracks[i] = autoTrack(fmt.Sprintf("%011d", i))
		tracks[i].Title = strings.Repeat("😀[*_`@everyone", 80)
		tracks[i].ChannelTitle = strings.Repeat("😀[*_`@everyone", 80)
		tracks[i].ChannelID = "UCabcdefghijklmnopqrstuv"
		tracks[i].ThumbnailURL = "https://i.ytimg.com/example.jpg"
	}
	s.queue.Add(music.Requester{ID: "123"}, tracks, "", "")
	s.current = &music.Entry{Track: tracks[0]}
	s.auto.seed = &tracks[0]
	e, _ := s.queueView(123, 1, "")
	if e.Thumbnail == nil || e.Thumbnail.URL != tracks[0].ThumbnailURL || !strings.Contains(e.Description, "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv") || strings.Contains(e.Description, "投稿：") || !strings.Contains(e.Description, "<@123>") || strings.Contains(e.Description, "追加：") {
		t.Fatal("channel/thumbnail layout")
	}
	size := func(v string) int { return len(utf16.Encode([]rune(v))) }
	total := size(e.Title) + size(e.Footer.Text) + size(e.Description)
	if size(e.Description) > 4096 || strings.Contains(e.Description, "@everyone") {
		t.Fatal("description limit or escaping")
	}
	for _, f := range e.Fields {
		if size(f.Name) > 256 || size(f.Value) > 1024 {
			t.Fatal("field limit")
		}
		if strings.Contains(f.Value, "@everyone") {
			t.Fatal("unescaped mention")
		}
		total += size(f.Name) + size(f.Value)
	}
	if total > 6000 {
		t.Fatalf("embed too large: %d", total)
	}
	if queueMention("invalid") != "不明なユーザー" {
		t.Fatal("unsafe requester")
	}
}

func TestPrefetchSharedWithScheduler(t *testing.T) {
	s := autoSession()
	s.started = true
	s.current = &music.Entry{Track: autoTrack("abcdefghijk")}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.fetchMix = func(ctx context.Context, _ string) (source.Result, error) {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return source.Result{}, ctx.Err()
		}
		return source.Result{Tracks: []music.Track{autoTrack("bcdefghijkl")}}, nil
	}
	s.mu.Lock()
	s.prefetchLocked()
	s.prefetchLocked()
	s.mu.Unlock()
	<-entered
	selected := make(chan bool, 1)
	go func() { _, automatic, ok := s.nextEntry(); selected <- automatic && ok }()
	s.queueView(123, 1, "")
	close(release)
	select {
	case ok := <-selected:
		if !ok {
			t.Fatal("candidate not played")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked scheduler")
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate fetch")
	}
}

func TestPrefetchFailureUsesSharedRetryBudget(t *testing.T) {
	s := autoSession()
	s.started = true
	var calls atomic.Int32
	s.fetchMix = func(context.Context, string) (source.Result, error) {
		calls.Add(1)
		return source.Result{}, errors.New("unavailable")
	}
	s.mu.Lock()
	s.prefetchLocked()
	done := s.auto.done
	s.mu.Unlock()
	<-done
	s.mu.Lock()
	s.prefetchLocked()
	enabled := s.auto.enabled
	s.mu.Unlock()
	if !enabled || calls.Load() != 1 {
		t.Fatal("prefetch stopped current playback or retried")
	}
	_, _, ok := s.nextEntry()
	if ok || calls.Load() != 2 {
		t.Fatal("retry budget exceeded")
	}
}

func TestPrefetchDoesNotRunWithManualWaitingOrFromView(t *testing.T) {
	s := autoSession()
	s.started = true
	s.fetchMix = func(context.Context, string) (source.Result, error) {
		t.Error("unexpected fetch")
		return source.Result{}, nil
	}
	s.queue.Add(music.Requester{ID: "123"}, []music.Track{autoTrack("bcdefghijkl")}, "", "")
	s.mu.Lock()
	s.prefetchLocked()
	s.mu.Unlock()
	s.queue.Clear()
	s.queueView(123, 1, "")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auto.fetching || s.auto.attempts != 0 {
		t.Fatal("view fetched candidates")
	}
}
