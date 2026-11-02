//go:build opus

package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/thesyncim/gopus"
	"io"
	"log/slog"
	"math"
	"os"
	"sync"
	"time"

	"asterinmusic/internal/process"
)

const StreamPaced = true

// StreamOpus applies gain only when a frame is due. The encoder retains state
// for the track; there is no queue of gain-adjusted PCM or encoded packets.
// The sink must honor cancellation and consume the packet before returning.
func StreamOpus(parent context.Context, ffmpeg string, produce Producer, volume *Volume, sink func([]byte) error, bitrate ...func() int) error {
	encoder, err := gopus.NewEncoder(gopus.EncoderConfig{SampleRate: 48000, Channels: 2, Application: gopus.ApplicationAudio})
	if err != nil {
		return fmt.Errorf("opus create: %w", err)
	}
	target := bitrateControl{}
	if len(bitrate) > 0 {
		target.read = bitrate[0]
	}
	for _, err := range []error{target.apply(encoder.SetBitrate), encoder.SetComplexity(9), encoder.SetBitrateMode(gopus.BitrateModeCVBR)} {
		if err != nil {
			return fmt.Errorf("opus configure: %w", err)
		}
	}
	tail := encoder.Lookahead()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	sourceR, sourceW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer sourceR.Close()
	defer sourceW.Close()
	pcmR, pcmW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer pcmR.Close()
	defer pcmW.Close()
	var wg sync.WaitGroup
	var stageErr error
	var once sync.Once
	fail := func(err error) {
		if err != nil {
			once.Do(func() { stageErr = err; cancel() })
		}
	}
	finished := make(chan struct{})
	wg.Go(func() {
		select {
		case <-ctx.Done():
			sourceR.Close()
			sourceW.Close()
			pcmR.Close()
			pcmW.Close()
		case <-finished:
		}
	})
	wg.Go(func() { defer sourceW.Close(); fail(produce(ctx, sourceW)) })
	wg.Go(func() {
		defer pcmW.Close()
		fail(process.Run(ctx, ffmpeg, []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-i", "pipe:0", "-vn", "-ar", "48000", "-ac", "2", "-f", "f32le", "pipe:1"}, sourceR, pcmW))
	})
	packet := make([]byte, 4000)
	err = encodeFrames(ctx, pcmR, volume, tail, func(samples []float32) ([]byte, error) {
		if err := target.apply(encoder.SetBitrate); err != nil {
			return nil, fmt.Errorf("opus bitrate: %w", err)
		}
		n, err := encoder.Encode(samples, packet)
		if err != nil {
			return nil, fmt.Errorf("opus encode: %w", err)
		}
		return packet[:n], nil
	}, sink)
	if err != nil {
		cancel()
	}
	// Closing the read ends also unblocks failed or cancelled producers.
	pcmR.Close()
	sourceR.Close()
	close(finished)
	wg.Wait()
	if parent.Err() != nil {
		return parent.Err()
	}
	return errors.Join(err, stageErr)
}

func encodeFrames(ctx context.Context, input io.Reader, volume *Volume, tail int, encode func([]float32) ([]byte, error), sink func([]byte) error) error {
	buffer := make([]byte, 960*2*4)
	samples := make([]float32, 960*2)
	next := time.Now()
	ending := false
	frames := 0
	late := 0
	var maxLate time.Duration
	defer func() { slog.Info("audio pacing", "frames", frames, "late_frames", late, "max_lateness", maxLate) }()
	for {
		clear(samples)
		n := 0
		if !ending {
			var err error
			n, err = io.ReadFull(input, buffer)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return err
			}
			if n%8 != 0 {
				return errors.New("truncated stereo float32 PCM frame")
			}
			ending = err != nil
			for i := 0; i < n/4; i++ {
				samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(buffer[i*4:]))
			}
		}
		if ending {
			if n == 0 && (frames == 0 || tail <= 0) {
				return nil
			}
			tail -= 960 - n/8
		}
		if frames == 0 {
			next = time.Now()
		}
		if wait := time.Until(next); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := volume.Apply(samples, 2); err != nil {
			return err
		}
		packet, err := encode(samples)
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = sink(packet); err != nil {
			return err
		}
		delay := time.Since(next)
		if delay > 20*time.Millisecond {
			late++
			if delay > maxLate {
				maxLate = delay
			}
		}
		frames++
		// Never catch up by bursting after an encode/send stall.
		next = next.Add(20 * time.Millisecond)
		if next.Before(time.Now()) {
			next = time.Now().Add(20 * time.Millisecond)
		}
	}
}
