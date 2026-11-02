package discordbot

import (
	"asterinmusic/internal/process"
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// VoiceCheck sends a quiet three-second sine through the production pipeline.
func (a *App) VoiceCheck() error {
	if a.config.VoiceChannelID == 0 {
		return errors.New("voice-check requires --voice-channel ID")
	}
	if !daveEnabled {
		return errors.New("use mise run voice-check")
	}
	if err := a.Check(); err != nil {
		return err
	}
	if err := a.connect(); err != nil {
		return err
	}
	s := a.getSession(a.config.GuildID)
	conn := a.client.VoiceManager.CreateConn(a.config.GuildID)
	ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
	defer cancel()
	if err := conn.Open(ctx, a.config.VoiceChannelID, false, true); err != nil {
		a.closeDave(conn)
		return errors.New("voice connection failed")
	}
	defer func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		conn.Close(c)
		a.closeDave(conn)
	}()
	err := s.playTrack(ctx, conn, func(ctx context.Context, w io.Writer) error {
		return process.Run(ctx, a.config.FFmpeg, []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=3", "-f", "wav", "pipe:1"}, nil, w)
	})
	if err != nil {
		return errors.New("voice audio pipeline failed")
	}
	fmt.Println("voice-check: connected and sent 3 seconds of audio; listening confirmation is separate")
	return nil
}
