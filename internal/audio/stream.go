//go:build !opus

package audio

import (
	"context"
	"errors"
	"os"
)

const StreamPaced = false

// StreamOpus keeps subprocess backpressure in native pipes. A Go stdout-copy
// goroutine can otherwise hit exec.WaitDelay during real-time packet pacing.
func StreamOpus(ctx context.Context, ffmpeg string, produce Producer, volume *Volume, sink func([]byte) error, bitrate ...func() int) error {
	// Diagnostic FFmpeg path has no live bitrate control. Live bots use opus.
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	encoded := make(chan error, 1)
	go func() { err := Encode(ctx, ffmpeg, produce, volume, w); w.Close(); encoded <- err }()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			r.Close()
		case <-done:
		}
	}()
	parseErr := Packets(r, sink)
	if parseErr != nil {
		cancel()
	}
	r.Close()
	encodeErr := <-encoded
	if parent.Err() != nil {
		return parent.Err()
	}
	if parseErr != nil {
		return errors.Join(parseErr, encodeErr)
	}
	return encodeErr
}
