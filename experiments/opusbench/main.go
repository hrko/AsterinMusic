// opusbench compares the isolated encoder hot path, not network or FFmpeg time.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"

	pion "github.com/pion/opus"
	"github.com/thesyncim/gopus"
)

type codec struct {
	encode func([]float32, []byte) (int, error)
	close  func()
}

func create(name string) (codec, error) {
	switch name {
	case "libopus":
		return nativeEncoder()
	case "pion":
		e, err := pion.NewEncoder(pion.WithChannels(2), pion.WithBitrate(96000), pion.WithComplexity(9), pion.WithVBR(true), pion.WithConstrainedVBR(true))
		if err != nil {
			return codec{}, err
		}
		return codec{e.EncodeFloat32, func() {}}, nil
	case "gopus":
		e, err := gopus.NewEncoder(gopus.EncoderConfig{SampleRate: 48000, Channels: 2, Application: gopus.ApplicationAudio})
		if err != nil {
			return codec{}, err
		}
		for _, err = range []error{e.SetBitrate(96000), e.SetComplexity(9), e.SetBitrateMode(gopus.BitrateModeCVBR)} {
			if err != nil {
				return codec{}, err
			}
		}
		return codec{e.Encode, func() {}}, nil
	}
	return codec{}, fmt.Errorf("unknown codec %s", name)
}

type result struct {
	Codec                               string
	Gain                                float64
	Frames                              int
	MeanUS, P50US, P95US, P99US, MaxUS  float64
	Over20ms                            int
	AllocsPerFrame, BytesPerFrame, Kbps float64
	DelaySamples                        int
	SNRdB, GainRatio                    float64
	Error                               string `json:",omitempty"`
	PacketSHA256                        string `json:",omitempty"`
}

func main() {
	output := flag.String("out", ".dev/opusbench", "output directory")
	input := flag.String("pcm", "", "optional stereo 48kHz float32le fixture (first 12 seconds)")
	pure := flag.Bool("pure", false, "encode-only smoke test with CGO_ENABLED=0")
	selected := flag.String("codecs", "", "comma-separated codec names")
	noWAV := flag.Bool("no-wav", false, "omit listening artifacts")
	flag.Parse()
	if err := os.MkdirAll(*output, 0755); err != nil {
		panic(err)
	}
	pcm := fixture()
	if *input != "" {
		b, err := os.ReadFile(*input)
		if err != nil {
			panic(err)
		}
		if len(b) > 12*48000*8 {
			b = b[:12*48000*8]
		}
		b = b[:len(b)/7680*7680]
		pcm = make([]float32, len(b)/4)
		for i := range pcm {
			pcm[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
		}
		if len(pcm) == 0 {
			panic("empty PCM")
		}
	}
	names := []string{"libopus", "pion", "gopus"}
	if *pure {
		names = names[1:]
	}
	if *selected != "" {
		names = strings.Split(*selected, ",")
	}
	var results []result
	for _, name := range names {
		for _, gain := range []float64{1, math.Pow(10, -28.0/20)} {
			wavOutput := *output
			if *noWAV {
				wavOutput = ""
			}
			r := measure(name, pcm, gain, wavOutput, *pure)
			results = append(results, r)
			fmt.Printf("%s gain=%.5f mean=%.1fus p99=%.1fus alloc=%.2f SNR=%.2fdB error=%s\n", name, gain, r.MeanUS, r.P99US, r.AllocsPerFrame, r.SNRdB, r.Error)
		}
	}
	build, _ := debug.ReadBuildInfo()
	fixtureBytes := make([]byte, len(pcm)*4)
	for i, x := range pcm {
		binary.LittleEndian.PutUint32(fixtureBytes[i*4:], math.Float32bits(x))
	}
	report := struct {
		OS, Arch, Go, Libopus string
		CPUs                  int
		Build                 *debug.BuildInfo
		Results               []result
		FixtureSHA256         string
		EncodeOnly            bool
		SIMD                  map[string]bool
	}{runtime.GOOS, runtime.GOARCH, runtime.Version(), nativeVersion(), runtime.NumCPU(), build, results, fmt.Sprintf("%x", sha256.Sum256(fixtureBytes)), *pure, simdFeatures()}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		panic(err)
	}
	if err = os.WriteFile(filepath.Join(*output, "results.json"), b, 0644); err != nil {
		panic(err)
	}
	for _, r := range results {
		if r.Error != "" {
			os.Exit(1)
		}
	}
}
func measure(name string, source []float32, gain float64, out string, pure bool) (r result) {
	r.Codec = name
	r.Gain = gain
	defer func() {
		if p := recover(); p != nil {
			r.Error = fmt.Sprint(p)
		}
	}()
	pcm := make([]float32, len(source))
	for i, x := range source {
		pcm[i] = x * float32(gain)
	}
	enc, err := create(name)
	if err != nil {
		r.Error = err.Error()
		return
	}
	defer enc.close()
	packet := make([]byte, 4000)
	// Warm caches and lazy allocations on a separate state; quality starts fresh.
	for i := 0; i < 100; i++ {
		if _, err = enc.encode(pcm[(i%(len(pcm)/1920))*1920:][:1920], packet); err != nil {
			r.Error = err.Error()
			return
		}
	}
	frames := len(pcm) / 1920
	durations := make([]float64, frames*3)
	totalBytes := 0
	for i := range durations {
		p := pcm[(i%frames)*1920:][:1920]
		start := clockUS()
		n, e := enc.encode(p, packet)
		durations[i] = clockUS() - start
		if e != nil {
			r.Error = e.Error()
			return
		}
		totalBytes += n
	}
	r.Frames = len(durations)
	// Keep clock-wrapper allocations out of encoder allocation measurements.
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := range durations {
		if _, err = enc.encode(pcm[(i%frames)*1920:][:1920], packet); err != nil {
			r.Error = err.Error()
			return
		}
	}
	runtime.ReadMemStats(&after)
	r.AllocsPerFrame = float64(after.Mallocs-before.Mallocs) / float64(r.Frames)
	r.BytesPerFrame = float64(after.TotalAlloc-before.TotalAlloc) / float64(r.Frames)
	for _, d := range durations {
		r.MeanUS += d
		if d > 20000 {
			r.Over20ms++
		}
	}
	r.MeanUS /= float64(r.Frames)
	sort.Float64s(durations)
	r.P50US = durations[len(durations)/2]
	r.P95US = durations[len(durations)*95/100]
	r.P99US = durations[len(durations)*99/100]
	r.MaxUS = durations[len(durations)-1]
	r.Kbps = float64(totalBytes) * 8 / (float64(r.Frames) * 20)
	if pure {
		return
	}
	q, err := create(name)
	if err != nil {
		r.Error = err.Error()
		return
	}
	defer q.close()
	decode, closeDecoder, err := referenceDecoder()
	if err != nil {
		r.Error = err.Error()
		return
	}
	defer closeDecoder()
	decoded := make([]float32, 0, len(pcm)+3840)
	frame := make([]float32, 1920)
	silence := make([]float32, 1920)
	packetHash := sha256.New()
	var packetSize [4]byte
	for i := 0; i < frames+2; i++ {
		p := silence
		if i < frames {
			p = pcm[i*1920:][:1920]
		}
		n, e := q.encode(p, packet)
		if e != nil {
			r.Error = e.Error()
			return
		}
		binary.LittleEndian.PutUint32(packetSize[:], uint32(n))
		packetHash.Write(packetSize[:])
		packetHash.Write(packet[:n])
		got, e := decode(packet[:n], frame)
		if e != nil || got != 960 {
			r.Error = fmt.Sprintf("decode samples=%d err=%v", got, e)
			return
		}
		decoded = append(decoded, frame...)
	}
	r.DelaySamples, r.SNRdB, r.GainRatio = quality(pcm, decoded)
	r.PacketSHA256 = fmt.Sprintf("%x", packetHash.Sum(nil))
	if out == "" {
		return
	}
	stem := fmt.Sprintf("%s-%.0f", name, gain*100)
	writeWAV(filepath.Join(out, stem+".wav"), decoded[r.DelaySamples*2:][:len(pcm)])
	writeWAV(filepath.Join(out, fmt.Sprintf("source-%.0f.wav", gain*100)), pcm)
	return
}

// Delay search uses both stereo channels; SNR is a waveform diagnostic, not a
// perceptual quality score. No gain fitting is applied to hide amplitude errors.
func quality(src, dst []float32) (delay int, snr, ratio float64) {
	best := math.Inf(-1)
	for lag := 0; lag <= 960; lag++ {
		var dot float64
		for i := 4800; i < len(src)-1920; i += 38 {
			dot += float64(src[i]) * float64(dst[i+lag*2])
			dot += float64(src[i+1]) * float64(dst[i+lag*2+1])
		}
		if dot > best {
			best = dot
			delay = lag
		}
	}
	var signal, noise, power float64
	for i := 1920; i < len(src); i++ {
		x, y := float64(src[i]), float64(dst[i+delay*2])
		signal += x * x
		noise += (x - y) * (x - y)
		power += y * y
	}
	return delay, 10 * math.Log10(signal/math.Max(noise, 1e-30)), math.Sqrt(power / signal)
}
func fixture() []float32 {
	p := make([]float32, 12*48000*2)
	rng := rand.New(rand.NewPCG(42, 17))
	for i := 0; i < len(p)/2; i++ {
		t := float64(i) / 48000
		beat := math.Mod(t, 0.5)
		env := math.Exp(-beat * 14)
		noise := (rng.Float64()*2 - 1) * 0.08 * math.Exp(-beat*50)
		for c := 0; c < 2; c++ {
			f := []float64{220, 329.63}[c]
			x := 0.18*math.Sin(2*math.Pi*f*t) + 0.1*math.Sin(2*math.Pi*f*1.5*t) + 0.06*math.Sin(2*math.Pi*(1800*t+300*t*t))
			x += env*0.15*math.Sin(2*math.Pi*70*t) + noise
			if t >= 8 && t < 9 {
				x = 0
			}
			p[i*2+c] = float32(x)
		}
	}
	return p
}
func writeWAV(path string, p []float32) {
	b := make([]byte, 44+len(p)*4)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 3)
	binary.LittleEndian.PutUint16(b[22:], 2)
	binary.LittleEndian.PutUint32(b[24:], 48000)
	binary.LittleEndian.PutUint32(b[28:], 384000)
	binary.LittleEndian.PutUint16(b[32:], 8)
	binary.LittleEndian.PutUint16(b[34:], 32)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(len(p)*4))
	for i, x := range p {
		binary.LittleEndian.PutUint32(b[44+i*4:], math.Float32bits(x))
	}
	if err := os.WriteFile(path, b, 0644); err != nil {
		panic(err)
	}
}
