package source

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"rainmusic/internal/music"
	"rainmusic/internal/process"
)

const PlaylistLimit = 100

type Runner func(context.Context, string, []string, io.Reader, io.Writer) error

type Client struct {
	Executable string
	Run        Runner
	Timeout    time.Duration
	semaphore  chan struct{}
}

func New(executable string) *Client {
	return &Client{Executable: executable, Run: process.Run, Timeout: 45 * time.Second, semaphore: make(chan struct{}, 2)}
}

type Result struct {
	Input        Input         `json:"input"`
	Tracks       []music.Track `json:"tracks"`
	Skipped      int           `json:"skipped"`
	LimitReached bool          `json:"limit_reached"`
	Partial      bool          `json:"partial"`
}

type metadata struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	URL          string   `json:"url"`
	WebpageURL   string   `json:"webpage_url"`
	Duration     *float64 `json:"duration"`
	Availability string   `json:"availability"`
	LiveStatus   string   `json:"live_status"`
	IsLive       bool     `json:"is_live"`
}

var baseArgs = []string{"--ignore-config", "--no-progress", "--no-colors", "--no-cache-dir", "--socket-timeout", "15", "--retries", "2", "--extractor-retries", "2"}

func (c *Client) Inspect(ctx context.Context, raw string) (Result, error) {
	input, err := Parse(raw)
	if err != nil {
		return Result{}, err
	}
	return c.inspect(ctx, input)
}

func (c *Client) inspect(ctx context.Context, input Input) (Result, error) {
	result := Result{Input: input}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	select {
	case c.semaphore <- struct{}{}:
		defer func() { <-c.semaphore }()
	case <-ctx.Done():
		return result, ctx.Err()
	}
	args := append([]string(nil), baseArgs...)
	if input.Playlist {
		args = append(args, "--yes-playlist", "--flat-playlist", "--dump-json", "--skip-download", "--ignore-errors", "--playlist-end", "100")
	} else {
		args = append(args, "--dump-single-json", "--skip-download", "--no-playlist")
	}
	args = append(args, "--", input.URL)
	var out bytes.Buffer
	err := c.Run(ctx, c.Executable, args, nil, &process.LimitedWriter{Writer: &out, Remaining: 16 << 20})
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	scanner := bufio.NewScanner(&out)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var m metadata
		if decodeErr := json.Unmarshal(scanner.Bytes(), &m); decodeErr != nil {
			return result, fmt.Errorf("invalid yt-dlp JSON: %w", decodeErr)
		}
		if m.ID == "" || m.Title == "" || m.IsLive || m.LiveStatus == "is_live" || m.LiveStatus == "is_upcoming" || (m.Availability != "" && m.Availability != "public" && m.Availability != "unlisted") {
			result.Skipped++
			continue
		}
		trackURL := input.URL
		if input.Playlist {
			if !videoID.MatchString(m.ID) {
				result.Skipped++
				continue
			}
			host := "www.youtube.com"
			if input.Site == "youtube_music" {
				host = "music.youtube.com"
			}
			trackURL = "https://" + host + "/watch?v=" + m.ID
		}
		if len(result.Tracks) < PlaylistLimit {
			result.Tracks = append(result.Tracks, music.Track{URL: trackURL, Title: m.Title, Duration: m.Duration, Site: input.Site})
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return result, scanErr
	}
	result.LimitReached = input.Playlist && len(result.Tracks)+result.Skipped >= PlaylistLimit
	result.Partial = err != nil || result.Skipped > 0
	if len(result.Tracks) == 0 {
		if err != nil {
			return result, err
		}
		return result, errors.New("no playable tracks found")
	}
	return result, err // Caller may accept playlist partial results while reporting the error.
}

// Stream fetches one track into a consuming writer. Cancellation is controlled
// by the playback context, not the short metadata timeout. Caller owns output.
func (c *Client) Stream(ctx context.Context, raw string, output io.Writer) error {
	input, err := Parse(raw)
	if err != nil {
		return err
	}
	if input.Playlist {
		return errors.New("stream requires a single track")
	}
	args := append([]string(nil), baseArgs...)
	args = append(args, "--no-playlist", "--match-filters", "!is_live & live_status !=? is_upcoming", "-f", "bestaudio", "-o", "-", "--", input.URL)
	count := &countingWriter{Writer: output}
	err = c.Run(ctx, c.Executable, args, nil, count)
	if err == nil && count.n == 0 {
		return errors.New("no audio received (video may have been filtered)")
	}
	return err
}

type countingWriter struct {
	io.Writer
	n int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.n += int64(n)
	return n, err
}
