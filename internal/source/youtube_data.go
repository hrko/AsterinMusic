package source

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"asterinmusic/internal/logging"
	"asterinmusic/internal/music"
)

// YouTubeData owns a bounded cache and serializes cache misses, including
// overlapping requests. Waiting for another caller counts toward the budget.
type YouTubeData struct {
	// Logger must be set before Enrich is called; nil disables diagnostics.
	Logger       *slog.Logger
	key          string
	http         *http.Client
	endpoint     string
	gate         chan struct{}
	cacheMu      sync.Mutex
	cache        map[string]titleCache
	blockedUntil time.Time
}
type titleCache struct {
	metadata apiMetadata
	expires  time.Time
}

type apiMetadata struct {
	Title, ChannelID, ChannelTitle, Thumbnail string
	Duration                                  *float64
}

var isoDuration = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

func apiDuration(s string) *float64 {
	m := isoDuration.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	var seconds float64
	for i, factor := range []float64{86400, 3600, 60, 1} {
		if m[i+1] != "" {
			n, err := strconv.ParseFloat(m[i+1], 64)
			if err != nil {
				return nil
			}
			seconds += n * factor
		}
	}
	if !validDuration(&seconds) {
		return nil
	}
	return &seconds
}

func validDuration(v *float64) bool {
	return v != nil && *v > 0 && *v <= 1e9 && !math.IsNaN(*v) && !math.IsInf(*v, 0)
}

func NewYouTubeData(key string) *YouTubeData {
	return &YouTubeData{key: key, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint: "https://www.googleapis.com/youtube/v3/videos", gate: make(chan struct{}, 1), cache: make(map[string]titleCache)}
}

func (d *YouTubeData) Enrich(parent context.Context, tracks []music.Track, lang string) {
	if d == nil || lang == "" || lang == "auto" || lang == "provider_default" {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	ids := []string{}
	positions := map[string][]int{}
	for i, t := range tracks {
		if t.Site != "youtube" && t.Site != "youtube_music" {
			continue
		}
		u, err := url.Parse(t.URL)
		if err != nil {
			continue
		}
		id := u.Query().Get("v")
		if !videoID.MatchString(id) {
			continue
		}
		if _, ok := positions[id]; !ok {
			ids = append(ids, id)
		}
		positions[id] = append(positions[id], i)
	}
	apply := func(id string, metadata apiMetadata) {
		for _, i := range positions[id] {
			if metadata.Title != "" {
				tracks[i].Title = metadata.Title
				tracks[i].TitleSource = "youtube_data_api"
				tracks[i].TitleLanguage = lang
			}
			if !validDuration(tracks[i].Duration) && metadata.Duration != nil {
				v := *metadata.Duration
				tracks[i].Duration = &v
			}
			if metadata.ChannelID != "" {
				tracks[i].ChannelID = metadata.ChannelID
			}
			if metadata.ChannelTitle != "" {
				tracks[i].ChannelTitle = metadata.ChannelTitle
			}
			if metadata.Thumbnail != "" {
				tracks[i].ThumbnailURL = metadata.Thumbnail
			}
		}
	}
	fromCache := func(ids []string) []string {
		d.cacheMu.Lock()
		defer d.cacheMu.Unlock()
		var missing []string
		for _, id := range ids {
			if v, ok := d.cache[lang+":"+id]; ok && time.Now().Before(v.expires) {
				apply(id, v.metadata)
			} else {
				missing = append(missing, id)
			}
		}
		return missing
	}
	missing := fromCache(ids)
	if len(missing) == 0 {
		return
	}
	select {
	case d.gate <- struct{}{}:
		defer func() { <-d.gate }()
	case <-ctx.Done():
		return
	}
	// Another caller may have filled overlapping misses while we waited.
	missing = fromCache(missing)
	if time.Now().Before(d.blockedUntil) {
		return
	}
	for start := 0; start < len(missing); start += 50 {
		if ctx.Err() != nil {
			return
		}
		end := min(start+50, len(missing))
		batch := missing[start:end]
		q := url.Values{"part": {"snippet,contentDetails"}, "id": {strings.Join(batch, ",")}, "hl": {lang}, "fields": {"items(id,snippet(localized/title,channelId,channelTitle,thumbnails),contentDetails/duration)"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.endpoint+"?"+q.Encode(), nil)
		if err != nil {
			return
		}
		req.Header.Set("X-Goog-Api-Key", d.key)
		resp, err := d.http.Do(req)
		if err != nil {
			d.blockedUntil = time.Now().Add(30 * time.Second)
			level, reason := slog.LevelWarn, "transport"
			if errors.Is(err, context.Canceled) {
				level, reason = slog.LevelDebug, "canceled"
			} else if errors.Is(err, context.DeadlineExceeded) {
				reason = "timeout"
			}
			logging.OrDiscard(d.Logger).Log(ctx, level, "YouTube metadata API unavailable", "event", "source.metadata.fallback", "reason", reason, "action", "use_extractor_metadata")
			return
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			delay := 30 * time.Second
			if resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 429 {
				delay = 15 * time.Minute
			}
			d.blockedUntil = time.Now().Add(delay)
			logging.OrDiscard(d.Logger).WarnContext(ctx, "YouTube metadata API unavailable", "event", "source.metadata.fallback", "reason", "http_status", "http_status", resp.StatusCode, "action", "use_extractor_metadata")
			return
		}
		var body struct {
			Items []struct {
				ID             string `json:"id"`
				ContentDetails struct {
					Duration string `json:"duration"`
				} `json:"contentDetails"`
				Snippet struct {
					ChannelID    string `json:"channelId"`
					ChannelTitle string `json:"channelTitle"`
					Thumbnails   map[string]struct {
						URL string `json:"url"`
					} `json:"thumbnails"`
					Localized struct {
						Title string `json:"title"`
					} `json:"localized"`
				} `json:"snippet"`
			} `json:"items"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
		resp.Body.Close()
		if err != nil {
			d.blockedUntil = time.Now().Add(30 * time.Second)
			logging.OrDiscard(d.Logger).WarnContext(ctx, "YouTube metadata API unavailable", "event", "source.metadata.fallback", "reason", "invalid_response", "action", "use_extractor_metadata")
			return
		}
		titles := map[string]apiMetadata{}
		for _, item := range body.Items {
			m := apiMetadata{Title: strings.TrimSpace(item.Snippet.Localized.Title), ChannelID: item.Snippet.ChannelID, ChannelTitle: item.Snippet.ChannelTitle, Duration: apiDuration(item.ContentDetails.Duration)}
			for _, size := range []string{"medium", "default", "high", "standard", "maxres"} {
				raw := item.Snippet.Thumbnails[size].URL
				u, e := url.Parse(raw)
				if e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil {
					m.Thumbnail = raw
					break
				}
			}
			titles[item.ID] = m
		}
		d.cacheMu.Lock()
		for _, id := range batch {
			metadata, found := titles[id]
			ttl := 24 * time.Hour
			if !found {
				ttl = 5 * time.Minute
			}
			if len(d.cache) >= 10000 {
				for k, v := range d.cache {
					if time.Now().After(v.expires) {
						delete(d.cache, k)
					}
				}
				if len(d.cache) >= 10000 {
					for k := range d.cache {
						delete(d.cache, k)
						break
					}
				}
			}
			d.cache[lang+":"+id] = titleCache{metadata, time.Now().Add(ttl)}
			apply(id, metadata)
		}
		d.cacheMu.Unlock()
		logging.OrDiscard(d.Logger).DebugContext(ctx, "YouTube metadata API request completed", "event", "source.metadata.completed", "videos", len(batch))
	}
}
