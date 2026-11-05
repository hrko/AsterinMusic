// asterinmusic exposes Discord playback, local diagnostics, and updater commands.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/disgoorg/snowflake/v2"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"asterinmusic/internal/audio"
	"asterinmusic/internal/config"
	"asterinmusic/internal/discordbot"
	"asterinmusic/internal/logging"
	"asterinmusic/internal/process"
	"asterinmusic/internal/runcontrol"
	"asterinmusic/internal/source"
	"asterinmusic/internal/telemetry"
	"asterinmusic/internal/updater"
)

func main() {
	o, args, err := config.ExtractOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if len(args) > 0 && args[0] == "serve" {
		root, err := os.Getwd()
		if err != nil {
			reportBootstrapFailure(os.Stderr, "working_directory")
			os.Exit(1)
		}
		controlled, control, err := runcontrol.Start(ctx, root, cancel, append(append([]string(nil), args...), o.Args()...)...)
		if err != nil {
			reportBootstrapFailure(os.Stderr, "lifecycle_control")
			os.Exit(1)
		}
		defer control.Close()
		ctx = controlled
	}
	if err := run(ctx, args, o); err != nil {
		if !errors.Is(err, errAlreadyLogged) {
			if len(args) > 0 && args[0] == "serve" {
				reportBootstrapFailure(os.Stderr, "configuration")
			} else {
				fmt.Fprintln(os.Stderr, err)
			}
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, opts config.Options) error {
	if len(args) == 0 {
		return errors.New("usage: asterinmusic doctor | inspect URL | capture URL OUTPUT | render INPUT OUTPUT.ogg")
	}
	switch args[0] {
	case "source-check":
		if len(args) != 2 {
			return errors.New("usage: source-check URL")
		}
		cfg, err := config.Load(opts)
		if err != nil {
			return err
		}
		executable := cfg.YTDLP
		if executable == "yt-dlp" {
			executable, err = updater.New().Executable()
			if err != nil {
				return errors.New("cannot select yt-dlp executable")
			}
		}
		c := source.New(executable)
		c.MetadataLanguage = cfg.YouTubeMetadataLanguage
		if cfg.YouTubeDataEnabled {
			c.YouTubeData = source.NewYouTubeData(cfg.YouTubeDataAPIKey)
			logger, logErr := logging.New(os.Stderr, cfg.Logging)
			if logErr != nil {
				return logErr
			}
			c.YouTubeData.Logger = logging.Component(logger, "source")
		}
		if err = c.ConfigureAuth(cfg.NiconicoAuth); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		r, err := c.Inspect(ctx, args[1])
		if err != nil {
			return err
		}
		if len(r.Tracks) != 1 || r.Input.Playlist {
			return errors.New("source-check requires one track")
		}
		fmt.Println("source-check: metadata OK; checking full audio acquisition (maximum 256 MiB / 5 minutes)")
		if err = c.BufferedStream(ctx, r.Tracks[0].URL, io.Discard); err != nil {
			return err
		}
		fmt.Println("source-check: metadata and audio OK")
		return nil
	case "stream-check":
		if len(args) != 2 {
			return errors.New("usage: stream-check URL")
		}
		c := source.New("yt-dlp")
		r, err := c.Inspect(ctx, args[1])
		if err != nil {
			return err
		}
		if len(r.Tracks) != 1 {
			return errors.New("requires one track")
		}
		fmt.Println("stream-check: metadata acquired; streaming at real-time speed")
		packets := 0
		start := time.Now()
		next := start
		err = audio.StreamOpus(ctx, "ffmpeg", func(ctx context.Context, w io.Writer) error { return c.BufferedStream(ctx, args[1], w) }, audio.NewVolume(48000), func([]byte) error {
			if packets == 0 {
				next = time.Now()
			}
			timer := time.NewTimer(max(0, time.Until(next)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			packets++
			next = next.Add(20 * time.Millisecond)
			return nil
		})
		fmt.Printf("stream-check: %d packets, %.2f seconds of audio, elapsed %s\n", packets, float64(packets)*.02, time.Since(start).Round(time.Second))
		return err
	case "update-check":
		v, err := updater.New().Latest(ctx)
		if err == nil {
			fmt.Println("latest yt-dlp:", v)
		}
		return err
	case "update-apply":
		if len(args) != 3 {
			return errors.New("usage: update-apply VERSION SAMPLE_URL")
		}
		if err := updater.New().Apply(ctx, args[1], args[2]); err != nil {
			return err
		}
		fmt.Println("update verified; restart the bot to use it")
		return nil
	case "update-rollback":
		if err := updater.New().Rollback(); err != nil {
			return err
		}
		fmt.Println("rollback selected; restart the bot to use it")
		return nil
	case "config-check", "discord-check", "register", "serve", "voice-check":
		c, err := config.Load(opts)
		if err != nil {
			return err
		}
		for i := 1; i < len(args); i += 2 {
			if i+1 >= len(args) || (args[i] != "--voice-channel" && args[i] != "--text-channel") || (args[0] != "voice-check" && args[0] != "discord-check") {
				return errors.New("unexpected command argument")
			}
			id, e := snowflake.Parse(args[i+1])
			if e != nil || id == 0 {
				return errors.New("invalid diagnostic channel ID")
			}
			if args[i] == "--voice-channel" {
				c.VoiceChannelID = id
			} else {
				c.TextChannelID = id
			}
		}
		if args[0] == "config-check" {
			fmt.Println("configuration: valid (authentication is checked by discord-check)")
			return nil
		}
		logger, err := logging.New(os.Stderr, c.Logging)
		if err != nil {
			return err
		}
		// dave-go does not forward its injected logger to MLS and also calls
		// slog.Default directly. Disable this unscoped compatibility path:
		// its crypto details are neither safe nor useful as application events.
		// Explicitly injected dependency loggers remain enabled.
		slog.SetDefault(slog.New(slog.DiscardHandler))
		telemetry.ConfigureDiagnostics(logger)
		tracing, err := telemetry.New(ctx, c.Tracing, logging.BuildVersion())
		if err != nil {
			return err
		}
		defer func() {
			if err := tracing.Shutdown(); err != nil {
				logger.Warn("tracing shutdown incomplete", "event", "tracing.shutdown.failed")
			}
		}()
		a, err := discordbot.New(ctx, c, logger, tracing.Provider)
		if err != nil {
			if args[0] == "serve" {
				logging.Component(logger, "app").ErrorContext(ctx, "application initialization failed", "event", "app.startup.failed", "stage", "initialize")
				return errAlreadyLogged
			}
			return err
		}
		defer a.Close()
		switch args[0] {
		case "discord-check":
			err = a.Check()
		case "register":
			err = a.Register()
		case "serve":
			if err := a.Serve(); err != nil {
				logging.Component(logger, "app").ErrorContext(ctx, "application could not continue", "event", "app.run.failed", "stage", "serve")
				return errAlreadyLogged
			}
			return nil
		case "voice-check":
			return a.VoiceCheck()
		}
		if err == nil {
			fmt.Println(args[0] + ": OK")
		}
		return err
	case "render":
		if len(args) != 3 {
			return errors.New("usage: asterinmusic render INPUT OUTPUT.ogg")
		}
		input, err := os.Open(args[1])
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(args[2], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer output.Close()
		return audio.Encode(ctx, "ffmpeg", func(_ context.Context, w io.Writer) error { _, err := io.Copy(w, input); return err }, audio.NewVolume(48000), output)
	case "doctor":
		for _, tool := range []struct {
			name string
			args []string
		}{{"yt-dlp", []string{"--version"}}, {"deno", []string{"--version"}}, {"ffmpeg", []string{"-version"}}} {
			check, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := process.Run(check, tool.name, tool.args, nil, os.Stdout)
			cancel()
			if err != nil {
				return err
			}
		}
		return nil
	case "inspect", "inspect-mix":
		if len(args) != 2 {
			return errors.New("usage: asterinmusic inspect URL")
		}
		client := source.New("yt-dlp")
		var r source.Result
		var err error
		if args[0] == "inspect-mix" {
			r, err = client.InspectMix(ctx, args[1])
		} else {
			r, err = client.Inspect(ctx, args[1])
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if encodeErr := encoder.Encode(r); encodeErr != nil {
			return encodeErr
		}
		return err
	case "capture":
		if len(args) != 3 {
			return errors.New("usage: asterinmusic capture URL OUTPUT (10-second acquisition check)")
		}
		if _, err := source.Parse(args[1]); err != nil {
			return err
		}
		out, err := os.OpenFile(args[2], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer out.Close()
		capture, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		err = source.New("yt-dlp").Stream(capture, args[1], &process.LimitedWriter{Writer: out, Remaining: 32 << 20})
		if errors.Is(err, context.DeadlineExceeded) {
			info, statErr := out.Stat()
			if statErr != nil {
				return statErr
			}
			if info.Size() == 0 {
				return errors.New("no audio received before timeout")
			}
			fmt.Printf("captured %d bytes (partial source container; not a playback test)\n", info.Size())
			return nil
		}
		return err
	default:
		return errors.New("unknown command")
	}
}
