package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"

	"asterinmusic/internal/process"
)

// Producer writes an encoded audio container and must honor context cancellation.
type Producer func(context.Context, io.Writer) error

// Encode streams through decode -> Go gain/ramp -> encode without buffering a
// whole track. Output is Ogg Opus, NOT raw Discord Opus packets. Output writers
// must not block indefinitely. Each invocation owns its subprocesses and pipes.
func Encode(ctx context.Context, ffmpeg string, produce Producer, volume *Volume, output io.Writer) error {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var pipes []*os.File
	defer func() {
		for _, p := range pipes {
			p.Close()
		}
	}()
	newPipe := func() (*os.File, *os.File, error) {
		r, w, err := os.Pipe()
		if err == nil {
			pipes = append(pipes, r, w)
		}
		return r, w, err
	}
	encodedR, encodedW, err := newPipe()
	if err != nil {
		return err
	}
	pcmR, pcmW, err := newPipe()
	if err != nil {
		return err
	}
	gainR, gainW, err := newPipe()
	if err != nil {
		return err
	}
	finished := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			for _, p := range pipes {
				p.Close()
			}
		case <-finished:
		}
	}()
	var wg sync.WaitGroup
	var firstErr error
	var once sync.Once
	launch := func(stage string, fn func() error) {
		wg.Go(func() {
			if err := fn(); err != nil {
				once.Do(func() { firstErr = fmt.Errorf("%s: %w", stage, err); cancel() })
			}
		})
	}
	launch("source", func() error { defer encodedW.Close(); return produce(ctx, encodedW) })
	launch("decode", func() error {
		defer encodedR.Close()
		defer pcmW.Close()
		return process.Run(ctx, ffmpeg, []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-i", "pipe:0", "-vn", "-ar", "48000", "-ac", "2", "-f", "f32le", "pipe:1"}, encodedR, pcmW)
	})
	launch("gain", func() error { defer pcmR.Close(); defer gainW.Close(); return applyPCM(pcmR, gainW, volume) })
	launch("encode", func() error {
		defer gainR.Close()
		return process.Run(ctx, ffmpeg, []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-f", "f32le", "-ar", "48000", "-ac", "2", "-i", "pipe:0", "-c:a", "libopus", "-b:a", "96k", "-frame_duration", "20", "-f", "ogg", "pipe:1"}, gainR, output)
	})
	wg.Wait()
	close(finished)
	<-watchDone
	if parent.Err() != nil {
		return parent.Err()
	}
	return firstErr
}

func applyPCM(input io.Reader, output io.Writer, volume *Volume) error {
	buffer := make([]byte, 960*2*4)
	samples := make([]float32, 960*2)
	for {
		n, err := io.ReadFull(input, buffer)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return err
		}
		if n%8 != 0 {
			return errors.New("truncated stereo float32 PCM frame")
		}
		for i := 0; i < n/4; i++ {
			samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(buffer[i*4:]))
		}
		if applyErr := volume.Apply(samples[:n/4], 2); applyErr != nil {
			return applyErr
		}
		for i := 0; i < n/4; i++ {
			binary.LittleEndian.PutUint32(buffer[i*4:], math.Float32bits(samples[i]))
		}
		if n > 0 {
			written, writeErr := output.Write(buffer[:n])
			if writeErr != nil {
				return writeErr
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		if err != nil {
			return nil
		}
	}
}
