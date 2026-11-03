package discordbot

import (
	"asterinmusic/internal/music"
	"strings"
	"testing"
	"time"
)

func TestOfferRetainsTrackMetadata(t *testing.T) {
	o := newOffers()
	sec := 123.0
	track := music.Track{URL: "https://www.youtube.com/watch?v=abcdefghijk", Title: "曲名", Duration: &sec, ChannelTitle: "channel", ThumbnailURL: "https://i.ytimg.com/test.jpg"}
	v := o.add(1, 2, "https://www.youtube.com/playlist?list=PLtest", track.URL)
	o.rememberTrack(v.ID, track)
	o.bind(v.ID, 3)
	sec = 1
	got, err := o.claim(v.ID, 1, 2, 3, time.Now())
	if err != nil || got.Track.Title != track.Title || got.Track.ThumbnailURL != track.ThumbnailURL || *got.Track.Duration != 123 {
		t.Fatal("lost original metadata")
	}
	body := originBody("起点", got.Track, "2")
	if !strings.HasPrefix(body, "起点：**[") || !strings.Contains(body, "2:03") {
		t.Fatal(body)
	}
}
func TestCompactQueueLayout(t *testing.T) {
	s := autoSession()
	tracks := []music.Track{autoTrack("abcdefghijk"), autoTrack("bcdefghijkl")}
	s.queue.Add(music.Requester{ID: "123"}, tracks, "", "")
	e, _ := s.queueView(123, 1, "")
	if !strings.Contains(e.Description, "1. **[") || !strings.Contains(e.Description, "\n\n   時間不明") || strings.Contains(e.Description, "\n\n2.") {
		t.Fatal(e.Description)
	}
	if !strings.HasPrefix(e.Footer.Text, "待機2曲 · 既知分") || !strings.Contains(e.Footer.Text, "\n1 / 1ページ") {
		t.Fatal(e.Footer.Text)
	}
	s.queue.Clear()
	s.auto.store(tracks)
	e, _ = s.queueView(123, 1, "")
	if !strings.Contains(e.Description, "1. **[") || strings.Contains(e.Footer.Text, "合計") || strings.Contains(e.Footer.Text, "待機") {
		t.Fatal("autoplay layout")
	}
}
