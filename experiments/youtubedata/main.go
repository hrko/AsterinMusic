// Probe public title metadata only; never connects to Discord.
package main

import (
	"asterinmusic/internal/music"
	"asterinmusic/internal/source"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	env, err := godotenv.Read(".env.youtube")
	if err != nil {
		return fmt.Errorf("cannot read .env.youtube")
	}
	key := strings.TrimSpace(env["YOUTUBE_DATA_API_KEY"])
	if key == "" {
		return fmt.Errorf("YOUTUBE_DATA_API_KEY is empty")
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, lang := range []string{"ja", "en", ""} {
		q := url.Values{"part": {"snippet"}, "id": {"TcLLpZBWsck,SfPkl7lol7g,QPLviaYkuSs,dQw4w9WgXcQ,ZRtdQ81jPUQ"}, "fields": {"items(id,snippet(title,defaultLanguage,localized(title)))"}}
		if lang != "" {
			q.Set("hl", lang)
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://www.googleapis.com/youtube/v3/videos?"+q.Encode(), nil)
		if err != nil {
			return fmt.Errorf("cannot construct API request")
		}
		req.Header.Set("X-Goog-Api-Key", key)
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("API transport failed (details suppressed to protect credentials)")
		}
		var result struct {
			Items []struct {
				ID      string `json:"id"`
				Snippet struct {
					Title           string `json:"title"`
					DefaultLanguage string `json:"defaultLanguage,omitempty"`
					Localized       struct {
						Title string `json:"title"`
					} `json:"localized"`
				} `json:"snippet"`
			} `json:"items"`
			Error struct {
				Code   int `json:"code"`
				Errors []struct {
					Reason string `json:"reason"`
				} `json:"errors"`
			} `json:"error"`
		}
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("API response decode failed (HTTP %d)", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("API HTTP %d; code %d", resp.StatusCode, result.Error.Code)
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"language": lang, "items": result.Items}); err != nil {
			return err
		}
		if len(result.Items) != 5 {
			return fmt.Errorf("expected 5 videos, received %d", len(result.Items))
		}
	}
	tracks := []music.Track{
		{URL: "https://www.youtube.com/watch?v=TcLLpZBWsck", Title: "original", Site: "youtube"},
		{URL: "https://music.youtube.com/watch?v=SfPkl7lol7g", Title: "original", Site: "youtube_music"},
		{URL: "https://www.youtube.com/watch?v=QPLviaYkuSs", Title: "original", Site: "youtube"},
	}
	source.NewYouTubeData(key).Enrich(context.Background(), tracks, "ja")
	for i, expected := range []string{"きらり", "花", "青春病"} {
		if tracks[i].Duration == nil || *tracks[i].Duration <= 0 || tracks[i].ChannelID == "" || tracks[i].ChannelTitle == "" || tracks[i].ThumbnailURL == "" {
			return fmt.Errorf("production metadata enrichment incomplete for case %d", i)
		}
		if !strings.Contains(tracks[i].Title, expected) || tracks[i].TitleSource != "youtube_data_api" {
			return fmt.Errorf("production title enrichment failed for case %d", i)
		}
	}
	fmt.Println("production title enrichment: OK")
	return nil
}
