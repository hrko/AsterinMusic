package source

import (
	"asterinmusic/internal/music"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIDuration(t *testing.T) {
	for raw, want := range map[string]float64{"PT3M52S": 232, "PT1H2M3S": 3723, "P1DT1S": 86401, "PT0S": 0, "bad": 0, "P": 0, "PT": 0, "PT2.5S": 2.5} {
		v := apiDuration(raw)
		if want == 0 {
			if v != nil {
				t.Fatal(raw)
			}
		} else if v == nil || *v != want {
			t.Fatal(raw)
		}
	}
}
func TestMetadataIndependentAndCache(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("part") != "snippet,contentDetails" {
			t.Error("parts")
		}
		fmt.Fprint(w, `{"items":[{"id":"TcLLpZBWsck","snippet":{"channelId":"UCabcdefghijklmnopqrstuv","channelTitle":"Channel","thumbnails":{"medium":{"url":"https://i.ytimg.com/medium.jpg"},"default":{"url":"https://i.ytimg.com/default.jpg"}}},"contentDetails":{"duration":"PT3M52S"}}]}`)
	}))
	defer s.Close()
	d := NewYouTubeData("secret")
	d.endpoint = s.URL
	for _, known := range []bool{false, true} {
		track := music.Track{URL: "https://www.youtube.com/watch?v=TcLLpZBWsck", Title: "original", Site: "youtube"}
		v := 200.0
		if known {
			track.Duration = &v
		}
		ts := []music.Track{track}
		d.Enrich(context.Background(), ts, "ja")
		got := ts[0]
		want := 232.0
		if known {
			want = 200
		}
		if got.Duration == nil || *got.Duration != want || got.Title != "original" || got.ChannelTitle != "Channel" || got.ThumbnailURL != "https://i.ytimg.com/medium.jpg" {
			t.Fatalf("metadata %+v", got)
		}
	}
	if calls != 1 {
		t.Fatal("cache missed")
	}
}
