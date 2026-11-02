package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valid = `version: 1
bots:
  main:
    token: '${TOKEN}'
servers:
  home:
    id: "123456789012345678"
    bots: [main]
    command_bot: main
`

func setup(t *testing.T, text string) Options {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return Options{ConfigFile: p}
}
func lookup(k string) (string, bool) { return "secret-value", k == "TOKEN" }
func TestLoadDefaultsAndPaths(t *testing.T) {
	o := setup(t, valid+"runtime:\n  state_dir: data\ntools:\n  ffmpeg:\n    path: ./bin/ffmpeg\n")
	c, err := Read(o, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "secret-value" || c.DefaultVolume != 30 || c.QueueLimit != 500 || !c.ActivityEnabled || c.ApplicationID != 0 {
		t.Fatal("wrong defaults")
	}
	if c.StateDir != filepath.Join(filepath.Dir(o.ConfigFile), "data") || c.FFmpeg != filepath.Join(filepath.Dir(o.ConfigFile), "bin", "ffmpeg") || c.YTDLP != "yt-dlp" {
		t.Fatal("wrong path resolution")
	}
}
func TestRejectInvalidConfigWithoutSecrets(t *testing.T) {
	for _, text := range []string{
		strings.Replace(valid, "version: 1", "version: 2", 1),
		valid + "unexpected: secret-value\n",
		valid + "version: 1\n",
		strings.Replace(valid, `id: "123456789012345678"`, `id: 123456789012345678`, 1),
		valid + "playback:\n  queue_limit: '4'\n",
		valid + "playback:\n  queue_limit: 4.5\n",
		valid + "playback:\n  queue_limit: 0\n",
		strings.Replace(valid, "[main]", "[missing]", 1),
		strings.Replace(valid, "[main]", "[main, main]", 1),
		strings.Replace(valid, "main:", "bad.name:", 1),
		strings.Replace(valid, "${TOKEN}", "${MISSING}", 1),
		strings.Replace(valid, "${TOKEN}", "${TOKEN:-secret-value}", 1),
		valid + "---\nsecret-value: test\n",
		strings.Replace(valid, "${TOKEN}", "", 1),
		valid + "playback:\n  default_volume: null\n",
	} {
		_, err := Read(setup(t, text), lookup)
		if err == nil {
			t.Errorf("accepted invalid case: %s", text)
		} else if strings.Contains(err.Error(), "secret-value") {
			t.Fatal("secret leaked")
		}
	}
}
func TestEnvPriorityAndExpansion(t *testing.T) {
	o := setup(t, valid)
	o.EnvFile = filepath.Join(filepath.Dir(o.ConfigFile), "secrets.env")
	os.WriteFile(o.EnvFile, []byte("TOKEN='file-secret'\n"), 0600)
	for _, tc := range []struct {
		value   string
		present bool
		want    string
		bad     bool
	}{{"process", true, "process", false}, {"", false, "file-secret", false}, {"", true, "", true}} {
		c, err := Read(o, func(string) (string, bool) { return tc.value, tc.present })
		if (err != nil) != tc.bad || (!tc.bad && c.Token != tc.want) {
			t.Fatal("priority mismatch")
		}
	}
	os.WriteFile(o.EnvFile, []byte("TOKEN='secret-value"), 0600)
	if _, err := Read(o, lookup); err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatal("dotenv error not sanitized")
	}
	for _, tc := range []struct{ in, want string }{{"$${TOKEN}", "${TOKEN}"}, {"a${TOKEN}b", "a${OTHER}: #b"}} {
		got, err := expand(tc.in, func(string) (string, bool) { return "${OTHER}: #", true })
		if err != nil || got != tc.want {
			t.Fatal(got, err)
		}
	}
}
func TestCLIOptions(t *testing.T) {
	o, rest, err := ExtractOptions([]string{"--config", "a.yaml", "serve", "--env-file=x.env"})
	if err != nil || len(rest) != 1 || rest[0] != "serve" || !filepath.IsAbs(o.ConfigFile) || !filepath.IsAbs(o.EnvFile) {
		t.Fatal(o, rest, err)
	}
	for _, args := range [][]string{{"--config"}, {"--config="}, {"--config", "a", "--config", "b"}} {
		if _, _, err := ExtractOptions(args); err == nil {
			t.Fatal("invalid flags accepted")
		}
	}
}
