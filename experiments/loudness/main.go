// Isolated source metadata / full-track loudness comparison. Never connects to Discord.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"asterinmusic/internal/config"
)

type result struct {
	URL         string          `json:"url"`
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Format      string          `json:"format_id"`
	Duration    float64         `json:"duration"`
	Probe       json.RawMessage `json:"normalization_probe"`
	Measurement map[string]any  `json:"measurement,omitempty"`
	Error       string          `json:"error,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	root := filepath.Join(".dev", "loudness-probe")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	plugin, _ := filepath.Abs("experiments/loudness/plugins")
	urls := os.Args[1:]
	if len(urls) == 0 {
		urls = []string{"https://www.youtube.com/watch?v=TcLLpZBWsck", "https://music.youtube.com/watch?v=QPLviaYkuSs", "https://www.youtube.com/watch?v=PE_iyDsY5xM", "https://www.nicovideo.jp/watch/sm9", "https://www.nicovideo.jp/watch/sm2057168", "https://www.nicovideo.jp/watch/sm46647529"}
	}
	// Only use a private copy of existing cookies; never print credentials/config.
	c, err := config.Load(config.Options{ConfigFile: "asterinmusic.yaml"})
	if err != nil {
		return fmt.Errorf("cannot load local config")
	}
	var all []result
	for i, url := range urls {
		r := result{URL: url}
		dir, err := os.MkdirTemp(root, "sample-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir) // Also clean copied credentials on an early return.
		media, _ := filepath.Abs(filepath.Join(dir, "audio.media"))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		args := []string{"--ignore-config", "--no-cache-dir", "--no-progress", "--no-colors", "--no-playlist", "--plugin-dirs", plugin, "--socket-timeout", "20", "--retries", "1", "--max-filesize", "256M", "-f", "bestaudio/best", "-o", media, "--print-json"}
		if strings.Contains(url, "nicovideo.jp") && c.NiconicoAuth.CookiesFile != "" {
			cookie := filepath.Join(dir, "cookies.txt")
			b, e := os.ReadFile(c.NiconicoAuth.CookiesFile)
			if e != nil {
				return fmt.Errorf("cannot read configured cookies")
			}
			if e = os.WriteFile(cookie, b, 0600); e != nil {
				return e
			}
			args = append(args, "--cookies", cookie)
		}
		args = append(args, "--", url)
		cmd := exec.CommandContext(ctx, "yt-dlp", args...)
		var out, stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		err = cmd.Run()
		cancel()
		if err != nil {
			r.Error = "source extraction/download failed"
		} else if json.Unmarshal(bytes.TrimSpace(out.Bytes()), &r) != nil {
			r.Error = "invalid extractor output"
		}
		r.URL = url
		if r.Error == "" {
			ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
			cmd = exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-nostdin", "-i", media, "-vn", "-af", "aformat=sample_rates=48000:channel_layouts=stereo,loudnorm=I=-18:TP=-1:LRA=50:print_format=json", "-f", "null", "-")
			stderr.Reset()
			cmd.Stderr = &stderr
			err = cmd.Run()
			cancel()
			data := stderr.Bytes()
			start := bytes.LastIndex(data, []byte("{"))
			end := bytes.LastIndex(data, []byte("}"))
			if err != nil || start < 0 || end < start {
				r.Error = "measurement failed"
			} else if json.Unmarshal(bytes.TrimSpace(data[start:end+1]), &r.Measurement) != nil {
				r.Error = "invalid measurement"
			}
		}
		all = append(all, r)
		b, _ := json.MarshalIndent(all, "", "  ")
		if err = os.WriteFile(filepath.Join(root, "results.json"), b, 0600); err != nil {
			return err
		}
		// This generated directory is confined to the experiment root.
		if err = os.RemoveAll(dir); err != nil {
			return err
		}
		fmt.Printf("sample %d/%d id=%s format=%s error=%s\n", i+1, len(urls), r.ID, r.Format, r.Error)
	}
	fmt.Println("sanitized results: .dev/loudness-probe/results.json")
	return nil
}
