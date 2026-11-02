// rainmusic exposes Discord playback, local diagnostics, and updater commands.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/disgoorg/snowflake/v2"
	"io"
	"os"
	"os/signal"
	"time"

	"rainmusic/internal/audio"
	"rainmusic/internal/config"
	"rainmusic/internal/discordbot"
	"rainmusic/internal/process"
	"rainmusic/internal/runcontrol"
	"rainmusic/internal/source"
	"rainmusic/internal/updater"
)

func main() {
	o, args, err := config.ExtractOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Args = append([]string{os.Args[0]}, append(args, o.Args()...)...)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		root, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		controlled, control, err := runcontrol.Start(ctx, root, cancel)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer control.Close()
		ctx = controlled
	}
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	opts, remaining, err := config.ExtractOptions(args)
	if err != nil {
		return err
	}
	args = remaining
	if len(args) == 0 {
		return errors.New("usage: rainmusic doctor | inspect URL | capture URL OUTPUT | render INPUT OUTPUT.ogg")
	}
	switch args[0] {
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
		a, err := discordbot.New(ctx, c)
		if err != nil {
			return err
		}
		defer a.Close()
		switch args[0] {
		case "discord-check":
			err = a.Check()
		case "register":
			err = a.Register()
		case "serve":
			return a.Serve()
		case "voice-check":
			return a.VoiceCheck()
		}
		if err == nil {
			fmt.Println(args[0] + ": OK")
		}
		return err
	case "render":
		if len(args) != 3 {
			return errors.New("usage: rainmusic render INPUT OUTPUT.ogg")
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
			return errors.New("usage: rainmusic inspect URL")
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
			return errors.New("usage: rainmusic capture URL OUTPUT (10-second acquisition check)")
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
