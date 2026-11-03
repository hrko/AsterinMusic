// Run through mise: isolated live metadata probe; never connects to Discord.
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
)

func main() {
	plugin, err := filepath.Abs("experiments/titleplugin/plugins")
	if err != nil {
		panic(err)
	}
	cases := []struct {
		name, url, lang string
		plugin, flat    bool
	}{
		{"baseline-ja", "https://music.youtube.com/watch?v=QPLviaYkuSs", "ja", false, false},
		{"plugin-ja", "https://music.youtube.com/watch?v=QPLviaYkuSs", "ja", true, false},
		{"plugin-en", "https://www.youtube.com/watch?v=QPLviaYkuSs", "en", true, false},
		{"plugin-default", "https://www.youtube.com/watch?v=QPLviaYkuSs", "", true, false},
		{"other-video-ja", "https://www.youtube.com/watch?v=PE_iyDsY5xM", "ja", true, false},
		{"mix-flat-ja", "https://www.youtube.com/watch?v=QPLviaYkuSs&list=RDQPLviaYkuSs", "ja", true, true},
		{"mix-flat-baseline-ja", "https://www.youtube.com/watch?v=QPLviaYkuSs&list=RDQPLviaYkuSs", "ja", false, true},
		{"search-kirari-ja", "https://www.youtube.com/watch?v=TcLLpZBWsck", "ja", true, false},
		{"search-kirari-en", "https://www.youtube.com/watch?v=TcLLpZBWsck", "en", true, false},
		{"search-english-ja", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "ja", true, false},
	}
	failed := false
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		args := []string{"--ignore-config", "--no-plugin-dirs", "--encoding", "utf-8", "--no-cache-dir", "--no-progress", "--skip-download", "--socket-timeout", "15", "--print", "%(id)s %(title)s"}
		if c.plugin {
			args = append(args, "--plugin-dirs", plugin)
		}
		if c.lang != "" {
			extractorArgs := "youtube:lang=" + c.lang
			if strings.HasPrefix(c.name, "search-") {
				extractorArgs += ";asterin_title_search=true"
			}
			args = append(args, "--extractor-args", extractorArgs)
		}
		if c.flat {
			args = append(args, "--yes-playlist", "--flat-playlist", "--playlist-end", "3")
		} else {
			args = append(args, "--no-playlist")
		}
		args = append(args, "--", c.url)
		cmd := exec.CommandContext(ctx, "yt-dlp", args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		cancel()
		if err == nil && c.name == "plugin-ja" && !strings.Contains(stdout.String(), "青春病") {
			err = fmt.Errorf("localized title was not selected")
		}
		if err == nil && c.name == "search-kirari-ja" && !strings.Contains(stdout.String(), "きらり") {
			err = fmt.Errorf("search localized title was not selected")
		}
		result := map[string]any{"case": c.name, "output": stdout.String(), "ok": err == nil}
		if err != nil {
			result["error"] = err.Error()
			result["diagnostic"] = stderr.String()
			failed = true
		}
		b, _ := json.Marshal(result)
		fmt.Println(string(b))
	}
	if failed {
		os.Exit(1)
	}
}
