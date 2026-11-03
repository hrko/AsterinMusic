//go:build opus

package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"github.com/thesyncim/gopus"
	"io"
	"math"
	"os"
	"testing"
	"time"

	"asterinmusic/internal/process"
)

func TestOpusLiveBitrateChanges(t *testing.T) {
	encoder, err := gopus.NewEncoder(gopus.EncoderConfig{SampleRate: 48000, Channels: 2, Application: gopus.ApplicationAudio})
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.SetBitrateMode(gopus.BitrateModeCVBR); err != nil {
		t.Fatal(err)
	}
	pcm := make([]float32, 1920)
	packet := make([]byte, 4000)
	for _, rate := range []int{8000, 64000, 96000, 128000, 256000, 384000, 64000} {
		if err := encoder.SetBitrate(rate); err != nil {
			t.Fatalf("%d: %v", rate, err)
		}
		for frame := 0; frame < 3; frame++ {
			for i := range pcm {
				pcm[i] = float32(0.3 * math.Sin(float64(i+frame*1920)*0.07))
			}
			n, err := encoder.Encode(pcm, packet)
			if err != nil || n == 0 {
				t.Fatalf("%d: bytes=%d err=%v", rate, n, err)
			}
		}
	}
}

func TestNativePacingStallAndLatestVolume(t *testing.T) {
	input := make([]byte, 6*960*8)
	for i := 0; i < len(input); i += 4 {
		binary.LittleEndian.PutUint32(input[i:], math.Float32bits(0.5))
	}
	v := NewVolume(48000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	count := 0
	var previous time.Time
	err := encodeFrames(ctx, bytes.NewReader(input), v, 0, func(p []float32) ([]byte, error) {
		if count == 2 && p[len(p)-1] != 0 {
			t.Errorf("latest mute not applied: %g", p[len(p)-1])
		}
		return []byte{1}, nil
	}, func([]byte) error {
		now := time.Now()
		if count == 2 && now.Sub(previous) < 95*time.Millisecond {
			t.Errorf("burst after stall: %s", now.Sub(previous))
		}
		previous = now
		if count == 1 {
			time.Sleep(80 * time.Millisecond)
			v.Set(100)
			v.Set(10)
			v.Set(0)
		}
		count++
		if count == 4 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || count != 4 {
		t.Fatalf("cancel: %d %v", count, err)
	}
}

func TestIntegrationNativeVolumeLatency(t *testing.T) {
	if os.Getenv("ASTERINMUSIC_AUDIO_TEST") != "1" {
		t.Skip("mise run native-audio-check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v := NewVolume(48000)
	v.Set(100)
	var stream bytes.Buffer
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8] = 1
	head[9] = 2
	binary.LittleEndian.PutUint32(head[12:], 48000)
	stream.Write(page(0, 2, []byte{19}, head))
	tags := make([]byte, 16)
	copy(tags, "OpusTags")
	stream.Write(page(1, 0, []byte{16}, tags))
	count := 0
	var changed time.Time
	var response time.Duration
	err := StreamOpus(ctx, "ffmpeg", func(ctx context.Context, w io.Writer) error {
		return process.Run(ctx, "ffmpeg", []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1.6", "-f", "wav", "pipe:1"}, nil, w)
	}, v, func(packet []byte) error {
		count++
		laces := make([]byte, len(packet)/255+1)
		for i := 0; i < len(laces)-1; i++ {
			laces[i] = 255
		}
		laces[len(laces)-1] = byte(len(packet) % 255)
		p := page(uint32(count+1), 0, laces, packet)
		binary.LittleEndian.PutUint64(p[6:], uint64(count*960))
		binary.LittleEndian.PutUint32(p[22:], 0)
		binary.LittleEndian.PutUint32(p[22:], oggCRC(p))
		stream.Write(p)
		if count == 20 {
			changed = time.Now()
			v.Set(0)
		}
		if count == 21 {
			response = time.Since(changed)
		}
		if count == 40 {
			v.Set(20)
			v.Set(0)
			v.Set(100)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if response > 100*time.Millisecond {
		t.Fatalf("packet response %s", response)
	}
	var decoded bytes.Buffer
	if err = process.Run(ctx, "ffmpeg", []string{"-hide_banner", "-loglevel", "error", "-i", "pipe:0", "-f", "f32le", "pipe:1"}, bytes.NewReader(stream.Bytes()), &decoded); err != nil {
		t.Fatal(err)
	}
	rms := func(frame int) float64 {
		b := decoded.Bytes()
		start := frame * 960 * 8
		end := start + 960*8
		if end > len(b) {
			t.Fatal("truncated decoded stream")
		}
		var sum float64
		for i := start; i < end; i += 4 {
			x := float64(math.Float32frombits(binary.LittleEndian.Uint32(b[i:])))
			sum += x * x
		}
		return math.Sqrt(sum / 1920)
	}
	baseline := rms(10)
	muteFrame := -1
	for i := 20; i < 25; i++ {
		if rms(i) < baseline*0.001 {
			muteFrame = i
			break
		}
	}
	if muteFrame < 0 {
		t.Fatalf("mute did not settle within 100ms: rms=%g", rms(24))
	}
	if rms(43) < baseline*0.8 {
		t.Fatal("unmute did not restore gain within 80ms")
	}
	t.Logf("next packet after Set: %s; decoded mute settled in <=%dms; unmute <=80ms; packets=%d", response, (muteFrame-20+1)*20, count)
}

func TestIntegrationNativeCancelAtSink(t *testing.T) {
	if os.Getenv("ASTERINMUSIC_AUDIO_TEST") != "1" {
		t.Skip("mise run native-audio-check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	count := 0
	start := time.Now()
	err := StreamOpus(ctx, "ffmpeg", func(ctx context.Context, w io.Writer) error {
		return process.Run(ctx, "ffmpeg", []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=duration=300", "-f", "wav", "pipe:1"}, nil, w)
	}, NewVolume(48000), func([]byte) error { count++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || count != 1 || time.Since(start) > 4*time.Second {
		t.Fatalf("cancel count=%d err=%v elapsed=%s", count, err, time.Since(start))
	}
}
