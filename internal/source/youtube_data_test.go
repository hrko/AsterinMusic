package source

import (
	"asterinmusic/internal/music"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDataCacheHitDoesNotWaitForNetwork(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		fmt.Fprint(w, `{"items":[]}`)
	}))
	defer s.Close()
	d := NewYouTubeData("secret")
	d.endpoint = s.URL
	d.cache["ja:TcLLpZBWsck"] = titleCache{metadata: apiMetadata{Title: "cached"}, expires: time.Now().Add(time.Hour)}
	missDone := make(chan struct{})
	defer func() { close(release); <-missDone }()
	go func() {
		d.Enrich(context.Background(), []music.Track{{URL: "https://www.youtube.com/watch?v=abcdefghijk", Site: "youtube"}}, "ja")
		close(missDone)
	}()
	<-entered
	hit := []music.Track{{URL: "https://www.youtube.com/watch?v=TcLLpZBWsck", Site: "youtube"}}
	done := make(chan struct{})
	go func() { d.Enrich(context.Background(), hit, "ja"); close(done) }()
	select {
	case <-done:
		if hit[0].Title != "cached" {
			t.Fatal("cache hit not applied")
		}
	case <-time.After(time.Second):
		t.Fatal("cache hit waited for unrelated HTTP request")
	}
}

func TestDataInspectPaths(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"id":"TcLLpZBWsck","snippet":{"localized":{"title":"きらり"}}}]}`)
	}))
	defer s.Close()
	c := New("unused")
	c.YouTubeData = NewYouTubeData("secret")
	c.YouTubeData.endpoint = s.URL
	c.Run = func(ctx context.Context, exe string, args []string, in io.Reader, out io.Writer) error {
		_, err := io.WriteString(out, `{"id":"TcLLpZBWsck","title":"Kirari"}`)
		return err
	}
	for _, raw := range []string{"https://www.youtube.com/watch?v=TcLLpZBWsck", "https://music.youtube.com/watch?v=TcLLpZBWsck", "https://www.youtube.com/playlist?list=PLtest"} {
		r, err := c.Inspect(context.Background(), raw)
		if err != nil || len(r.Tracks) != 1 || r.Tracks[0].Title != "きらり" {
			t.Fatalf("inspect enrichment: %v", err)
		}
	}
	r, err := c.InspectMix(context.Background(), "https://www.youtube.com/watch?v=TcLLpZBWsck")
	if err != nil || len(r.Tracks) != 1 || r.Tracks[0].Title != "きらり" {
		t.Fatalf("mix enrichment: %v", err)
	}
}

func TestDataBatchCacheAndConcurrent(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Goog-Api-Key") != "secret" || r.URL.Query().Get("key") != "" {
			t.Error("credential transport")
		}
		var items []any
		ids := r.URL.Query().Get("id")
		for _, id := range strings.Split(ids, ",") {
			items = append(items, map[string]any{"id": id, "snippet": map[string]any{"localized": map[string]string{"title": r.URL.Query().Get("hl") + id}}})
		}
		if len(items) > 50 {
			t.Error("batch exceeds 50")
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items})
	}))
	defer s.Close()
	d := NewYouTubeData("secret")
	d.endpoint = s.URL
	makeTracks := func() []music.Track {
		var ts []music.Track
		for i := 0; i < 51; i++ {
			ts = append(ts, music.Track{URL: fmt.Sprintf("https://www.youtube.com/watch?v=%011d", i), Title: "original", Site: "youtube"})
		}
		return append(ts, ts[0])
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ts := makeTracks()
			d.Enrich(context.Background(), ts, "ja")
			for _, v := range ts {
				if v.TitleSource != "youtube_data_api" || v.TitleLanguage != "ja" {
					t.Error("missing enrichment")
				}
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("requests %d", calls.Load())
	}
	d.Enrich(context.Background(), makeTracks(), "en")
	if calls.Load() != 4 {
		t.Fatal("language cache collision")
	}
	d.Enrich(context.Background(), makeTracks(), "auto")
	if calls.Load() != 4 {
		t.Fatal("auto called API")
	}
}

func TestDataFallback(t *testing.T) {
	for _, status := range []int{200, 400, 401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				fmt.Fprint(w, `{"items":[{"id":"other000000","snippet":{"localized":{"title":"wrong"}}}]}`)
			}))
			defer s.Close()
			d := NewYouTubeData("secret")
			d.endpoint = s.URL
			ts := []music.Track{{URL: "https://www.youtube.com/watch?v=TcLLpZBWsck", Title: "original", Site: "youtube"}}
			d.Enrich(context.Background(), ts, "ja")
			d.Enrich(context.Background(), ts, "ja")
			if ts[0].Title != "original" || calls != 1 {
				t.Fatal("fallback/cache/backoff failed")
			}
		})
	}
}

func TestDataDeadline(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	d := NewYouTubeData("secret")
	d.endpoint = s.URL
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	ts := []music.Track{{URL: "https://www.youtube.com/watch?v=TcLLpZBWsck", Title: "original", Site: "youtube"}}
	d.Enrich(ctx, ts, "ja")
	if ts[0].Title != "original" || ctx.Err() == nil {
		t.Fatal("deadline fallback")
	}
}
