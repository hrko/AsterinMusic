package audio

import (
	"context"
	"log/slog"
)

type StreamOptions struct {
	Bitrate func() int
	Logger  *slog.Logger
}

// StreamOpus is the diagnostic convenience entry point without logging.
// Application playback uses StreamOpusWithOptions with an owner-scoped logger.
func StreamOpus(ctx context.Context, ffmpeg string, produce Producer, volume *Volume, sink func([]byte) error, bitrate ...func() int) error {
	options := StreamOptions{}
	if len(bitrate) > 0 {
		options.Bitrate = bitrate[0]
	}
	return StreamOpusWithOptions(ctx, ffmpeg, produce, volume, sink, options)
}
