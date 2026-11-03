package config

import (
	"asterinmusic/internal/source"
	"errors"
	"github.com/disgoorg/snowflake/v2"
	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/dotenv"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

// Config contains resolved secrets; never log it.
type Config struct {
	YouTubeDataEnabled                                    bool
	YouTubeDataAPIKey                                     string
	DisconnectWhenIdle                                    bool
	YouTubeMetadataLanguage                               string
	NiconicoAuth                                          source.AuthConfig
	Token                                                 string
	ApplicationID, GuildID, TextChannelID, VoiceChannelID snowflake.ID
	ActivityEnabled                                       bool
	StateDir, YTDLP, FFmpeg                               string
	DefaultVolume, QueueLimit                             int
}
type Document struct {
	Sources struct {
		YouTube struct {
			DataAPI struct {
				Enabled bool   `koanf:"enabled"`
				APIKey  string `koanf:"api_key"`
			} `koanf:"data_api"`
			MetadataLanguage *string `koanf:"metadata_language"`
		} `koanf:"youtube"`
		Niconico struct {
			Auth struct {
				Method      string  `koanf:"method"`
				CookiesFile *string `koanf:"cookies_file"`
				Username    *string `koanf:"username"`
				Password    *string `koanf:"password"`
			} `koanf:"auth"`
		} `koanf:"niconico"`
	} `koanf:"sources"`
	Version int `koanf:"version"`
	Runtime struct {
		StateDir string `koanf:"state_dir"`
	} `koanf:"runtime"`
	Tools struct {
		YTDLP  Tool `koanf:"yt_dlp"`
		FFmpeg Tool `koanf:"ffmpeg"`
	} `koanf:"tools"`
	Playback struct {
		DisconnectWhenIdle bool `koanf:"disconnect_when_idle"`
		DefaultVolume      int  `koanf:"default_volume"`
		QueueLimit         int  `koanf:"queue_limit"`
	} `koanf:"playback"`
	Bots    map[string]Bot    `koanf:"bots"`
	Servers map[string]Server `koanf:"servers"`
}
type Tool struct {
	Path string `koanf:"path"`
}
type Bot struct {
	ApplicationID string `koanf:"application_id"`
	Token         string `koanf:"token"`
	Activity      struct {
		Enabled *bool `koanf:"enabled"`
	} `koanf:"activity"`
}
type Server struct {
	ID         string   `koanf:"id"`
	Bots       []string `koanf:"bots"`
	CommandBot string   `koanf:"command_bot"`
}
type Options struct{ ConfigFile, EnvFile string }

func Load(o Options) (Config, error) { return Read(o, os.LookupEnv) }
func Read(o Options, lookup func(string) (string, bool)) (Config, error) {
	if o.ConfigFile == "" {
		o.ConfigFile = "asterinmusic.yaml"
	}
	path, err := filepath.Abs(o.ConfigFile)
	if err != nil {
		return Config{}, errors.New("invalid config path")
	}
	vars := koanf.New(".")
	if o.EnvFile != "" {
		if err := vars.Load(file.Provider(o.EnvFile), dotenv.Parser()); err != nil {
			return Config{}, errors.New("cannot read or parse env file")
		}
	}
	resolve := func(name string) (string, bool) {
		if v, ok := lookup(name); ok {
			return v, true
		}
		if vars.Exists(name) {
			return vars.String(name), true
		}
		return "", false
	}
	k := koanf.New(".")
	defaults := map[string]any{"runtime.state_dir": "./.asterinmusic/state", "tools.yt_dlp.path": "yt-dlp", "tools.ffmpeg.path": "ffmpeg", "playback.default_volume": 30, "playback.queue_limit": 500}
	if err := k.Load(confmap.Provider(defaults, "."), nil); err != nil {
		return Config{}, errors.New("cannot load defaults")
	}
	// Expand in the parser, before koanf flattens paths. Keys are never expanded.
	if err := k.Load(file.Provider(path), yamlParser{lookup: resolve}); err != nil {
		return Config{}, errors.New("cannot read config or invalid YAML/variable reference")
	}
	var d Document
	err = k.UnmarshalWithConf("", &d, koanf.UnmarshalConf{DecoderConfig: &mapstructure.DecoderConfig{TagName: "koanf", Result: &d, ErrorUnused: true, WeaklyTypedInput: false, MatchName: func(a, b string) bool { return a == b }, DecodeHook: mapstructure.DecodeHookFuncType(func(from, to reflect.Type, data any) (any, error) {
		if to.Kind() == reflect.Int && from.Kind() != reflect.Int {
			return nil, errors.New("expected integer")
		}
		return data, nil
	})}})
	if err != nil {
		return Config{}, errors.New("configuration contains unknown keys or invalid types")
	}
	return d.resolve(filepath.Dir(path))
}

var namePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)
var idPattern = regexp.MustCompile(`^[0-9]+$`)

func parseID(s string) (snowflake.ID, error) {
	if !idPattern.MatchString(s) {
		return 0, errors.New("Discord IDs must be nonzero decimal strings")
	}
	id, err := snowflake.Parse(s)
	if err != nil || id == 0 {
		return 0, errors.New("invalid Discord ID")
	}
	return id, nil
}
func (d Document) resolve(base string) (Config, error) {
	fail := func(message string) (Config, error) { return Config{}, errors.New(message) }
	if d.Version != 1 {
		return fail("unsupported or missing configuration version")
	}
	if len(d.Bots) != 1 || len(d.Servers) != 1 {
		return fail("this version requires exactly one bot and one server")
	}
	if d.Playback.DefaultVolume < 0 || d.Playback.DefaultVolume > 100 || d.Playback.QueueLimit <= 0 {
		return fail("invalid playback limits")
	}
	if strings.TrimSpace(d.Runtime.StateDir) == "" || strings.TrimSpace(d.Tools.YTDLP.Path) == "" || strings.TrimSpace(d.Tools.FFmpeg.Path) == "" {
		return fail("runtime/tool paths must not be empty")
	}
	c := Config{StateDir: resolvePath(base, d.Runtime.StateDir), YTDLP: resolveTool(base, d.Tools.YTDLP.Path), FFmpeg: resolveTool(base, d.Tools.FFmpeg.Path), DefaultVolume: d.Playback.DefaultVolume, QueueLimit: d.Playback.QueueLimit, ActivityEnabled: true}
	c.YouTubeMetadataLanguage = "ja"
	c.YouTubeDataEnabled = d.Sources.YouTube.DataAPI.Enabled
	c.YouTubeDataAPIKey = d.Sources.YouTube.DataAPI.APIKey
	if c.YouTubeDataEnabled && (strings.TrimSpace(c.YouTubeDataAPIKey) == "" || strings.ContainsAny(c.YouTubeDataAPIKey, "\r\n\x00")) {
		return fail("YouTube Data API requires a nonempty valid api_key")
	}
	c.DisconnectWhenIdle = d.Playback.DisconnectWhenIdle
	if value := d.Sources.YouTube.MetadataLanguage; value != nil {
		c.YouTubeMetadataLanguage = *value
	}
	if value := c.YouTubeMetadataLanguage; value != "auto" && value != "provider_default" && !regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`).MatchString(value) {
		return fail("invalid YouTube metadata language code")
	}
	a := d.Sources.Niconico.Auth
	c.NiconicoAuth.Method = a.Method
	if (a.Method != "cookies" && a.CookiesFile != nil) || (a.Method != "password" && (a.Username != nil || a.Password != nil)) {
		return fail("niconico authentication methods cannot be mixed")
	}
	if a.CookiesFile != nil && *a.CookiesFile != "" {
		c.NiconicoAuth.CookiesFile = resolvePath(base, *a.CookiesFile)
	}
	if a.Username != nil {
		c.NiconicoAuth.Username = *a.Username
	}
	if a.Password != nil {
		c.NiconicoAuth.Password = *a.Password
	}
	if err := source.ValidateAuth(c.NiconicoAuth); err != nil {
		return Config{}, err
	}
	for name, b := range d.Bots {
		if !namePattern.MatchString(name) {
			return fail("invalid bot name")
		}
		if strings.TrimSpace(b.Token) == "" {
			return fail("bot token must not be empty")
		}
		c.Token = b.Token
		if b.ApplicationID != "" {
			id, err := parseID(b.ApplicationID)
			if err != nil {
				return fail("invalid application_id")
			}
			c.ApplicationID = id
		}
		if b.Activity.Enabled != nil {
			c.ActivityEnabled = *b.Activity.Enabled
		}
	}
	for name, s := range d.Servers {
		if !namePattern.MatchString(name) {
			return fail("invalid server name")
		}
		id, err := parseID(s.ID)
		if err != nil {
			return fail("invalid server id (use a quoted string)")
		}
		c.GuildID = id
		if len(s.Bots) != 1 || s.CommandBot == "" || s.Bots[0] != s.CommandBot {
			return fail("server requires one bot and matching command_bot")
		}
		if _, ok := d.Bots[s.CommandBot]; !ok {
			return fail("server references an undefined bot")
		}
	}
	return c, nil
}
func resolvePath(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}
func resolveTool(base, p string) string {
	if strings.ContainsAny(p, `/\`) || filepath.IsAbs(p) {
		return resolvePath(base, p)
	}
	return p
}
