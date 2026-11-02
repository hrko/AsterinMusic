package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"testing"
	"time"

	"rainmusic/internal/process"
)

func TestIntegrationPacedOpus(t *testing.T) {
	if os.Getenv("RAINMUSIC_AUDIO_TEST") != "1" {
		t.Skip("mise run audio-check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	packets := 0
	err := StreamOpus(ctx, "ffmpeg", func(ctx context.Context, w io.Writer) error {
		return process.Run(ctx, "ffmpeg", []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=5", "-f", "wav", "pipe:1"}, nil, w)
	}, NewVolume(48000), func([]byte) error {
		packets++
		if !StreamPaced {
			time.Sleep(20 * time.Millisecond)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if packets < 250 || packets > 252 {
		t.Fatalf("truncated stream: %d packets", packets)
	}
}

func TestIntegrationGainAndOpus(t *testing.T) {
	if os.Getenv("RAINMUSIC_AUDIO_TEST") != "1" {
		t.Skip("mise run audio-check enables FFmpeg integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var source bytes.Buffer
	err := process.Run(ctx, "ffmpeg", []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.5", "-ar", "48000", "-ac", "2", "-f", "wav", "pipe:1"}, nil, &source)
	if err != nil {
		t.Fatal(err)
	}
	levels := map[int]float64{}
	for _, percent := range []int{100, 30, 0} {
		v := NewVolume(48000)
		v.Set(percent)
		v.Apply(make([]float32, 960), 2) // settle ramp before measurement
		var encoded bytes.Buffer
		err := Encode(ctx, "ffmpeg", func(_ context.Context, w io.Writer) error {
			_, err := io.Copy(w, bytes.NewReader(source.Bytes()))
			return err
		}, v, &encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(encoded.Bytes(), []byte("OpusHead")) {
			t.Fatal("missing Opus header")
		}
		var decoded bytes.Buffer
		if err := process.Run(ctx, "ffmpeg", []string{"-hide_banner", "-loglevel", "error", "-i", "pipe:0", "-f", "f32le", "pipe:1"}, bytes.NewReader(encoded.Bytes()), &decoded); err != nil {
			t.Fatal(err)
		}
		data := decoded.Bytes()
		if len(data) < 48000*2*4/3 {
			t.Fatal("decoded audio too short")
		}
		var sum float64
		for i := 0; i+4 <= len(data); i += 4 {
			x := float64(math.Float32frombits(binary.LittleEndian.Uint32(data[i:])))
			sum += x * x
		}
		levels[percent] = math.Sqrt(sum / float64(len(data)/4))
	}
	want, _ := Gain(30)
	ratio := levels[30] / levels[100]
	if math.Abs(ratio-want) > want*0.05 {
		t.Fatalf("RMS gain %g, want %g", ratio, want)
	}
	if levels[0] > 1e-6 {
		t.Fatalf("mute RMS %g", levels[0])
	}
	t.Logf("decoded RMS ratio at 30%%: %.5f; mute RMS %.8f", ratio, levels[0])
}

func TestIntegrationCancellation(t *testing.T) {
	if os.Getenv("RAINMUSIC_AUDIO_TEST") != "1" {
		t.Skip("mise run audio-check enables FFmpeg integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := Encode(ctx, "ffmpeg", func(ctx context.Context, w io.Writer) error {
		return process.Run(ctx, "ffmpeg", []string{"-hide_banner", "-loglevel", "error", "-re", "-f", "lavfi", "-i", "sine=frequency=440", "-f", "wav", "pipe:1"}, nil, w)
	}, NewVolume(48000), io.Discard)
	if err == nil || time.Since(start) > 6*time.Second {
		t.Fatalf("cancellation: %v after %s", err, time.Since(start))
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
