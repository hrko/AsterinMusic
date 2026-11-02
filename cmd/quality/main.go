// quality prepares and scores a pinned local corpus. No shell scripts or APIs.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

const root = ".dev/visqol/evaluation"

type clip struct {
	ID, Group, Title, Path   string
	SourceSHA256             string
	StartSeconds, InputScale float64
}
type pair struct {
	ID, Group, Codec, Channel, Reference, Degraded string
	Volume                                         int
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: quality prepare|score|summarize")
	}
	if os.Args[1] == "score" {
		return score()
	}
	if os.Args[1] == "summarize" {
		return summarize()
	}
	if os.Args[1] != "prepare" {
		return fmt.Errorf("unknown action")
	}
	b, err := os.ReadFile("experiments/visqol/corpus.json")
	if err != nil {
		return err
	}
	var clips []clip
	if err = json.Unmarshal(b, &clips); err != nil {
		return err
	}
	var pairs []pair
	for i := range clips {
		c := &clips[i]
		fmt.Printf("prepare %d/%d %s\n", i+1, len(clips), c.ID)
		dir := filepath.Join(root, c.ID)
		if err = os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		f, e := os.Open(c.Path)
		if e != nil {
			return e
		}
		h := sha256.New()
		_, e = io.Copy(h, f)
		f.Close()
		if e != nil {
			return e
		}
		c.SourceSHA256 = fmt.Sprintf("%x", h.Sum(nil))
		// Decode at most 180 seconds; choose the earliest active 8-second window.
		data, e := exec.Command("ffmpeg", "-v", "error", "-i", c.Path, "-t", "180", "-ar", "48000", "-ac", "2", "-f", "f32le", "pipe:1").Output()
		if e != nil {
			return fmt.Errorf("decode %s: %w", c.ID, e)
		}
		samples := floats(data)
		start := 0
		for start+960 <= len(samples) {
			var energy float64
			for _, x := range samples[start : start+960] {
				energy += float64(x) * float64(x)
			}
			if math.Sqrt(energy/960) > 0.001 {
				break
			}
			start += 960
		}
		start = max(0, start-4800)
		if start+8*48000*2 > len(samples) {
			start = max(0, len(samples)-8*48000*2)
		}
		c.StartSeconds = float64(start) / 96000
		p := make([]float32, 9*48000*2)
		n := min(8*48000*2, len(samples)-start)
		copy(p[48000:48000+n], samples[start:start+n])
		peak := 0.0
		for _, x := range p {
			peak = math.Max(peak, math.Abs(float64(x)))
		}
		if peak < 1e-5 {
			return fmt.Errorf("silent source: %s", c.ID)
		}
		c.InputScale = 0.8 / peak
		for j := range p {
			p[j] *= float32(c.InputScale)
		}
		pcm := filepath.Join(dir, "input.f32")
		if e = os.WriteFile(pcm, floatBytes(p), 0644); e != nil {
			return e
		}
		ext := ""
		if runtime.GOOS == "windows" {
			ext = ".exe"
		}
		for _, mode := range []string{"nosimd", "simd"} {
			binaryPath, e := filepath.Abs(filepath.Join("bin", "opusbench-"+mode+ext))
			if e != nil {
				return e
			}
			args := []string{"-codecs", "gopus", "-pcm", pcm, "-out", filepath.Join(dir, mode)}
			if mode == "nosimd" {
				args[1] = "libopus,gopus"
			}
			cmd := exec.Command(binaryPath, args...)
			var log bytes.Buffer
			cmd.Stdout = &log
			cmd.Stderr = &log
			if e = cmd.Run(); e != nil {
				return fmt.Errorf("encode %s: %w: %s", c.ID, e, log.String())
			}
			if e = os.WriteFile(filepath.Join(dir, mode+".log"), log.Bytes(), 0644); e != nil {
				return e
			}
		}
		for _, vol := range []int{100, 30} {
			suffix := "100"
			restore := 1.0
			if vol == 30 {
				suffix = "4"
				restore = math.Pow(10, 28.0/20)
			}
			reference, e := readFloatWAV(filepath.Join(dir, "nosimd", "source-"+suffix+".wav"))
			if e != nil {
				return e
			}
			for _, channel := range []string{"mix", "left", "right"} {
				refPath := filepath.ToSlash(filepath.Join(dir, fmt.Sprintf("reference-%d-%s.wav", vol, channel)))
				if e = writeMono(refPath, reference, channel, restore); e != nil {
					return e
				}
				for _, codec := range []string{"libopus", "gopus-scalar", "gopus-simd"} {
					mode, name := "nosimd", "libopus"
					if codec != "libopus" {
						name = "gopus"
					}
					if codec == "gopus-simd" {
						mode = "simd"
					}
					decoded, e := readFloatWAV(filepath.Join(dir, mode, name+"-"+suffix+".wav"))
					if e != nil {
						return e
					}
					path := filepath.ToSlash(filepath.Join(dir, fmt.Sprintf("%s-%d-%s.wav", codec, vol, channel)))
					if e = writeMono(path, decoded, channel, restore); e != nil {
						return e
					}
					pairs = append(pairs, pair{c.ID, c.Group, codec, channel, refPath, path, vol})
				}
				// Self-comparison calibrates the metric ceiling for every reference.
				pairs = append(pairs, pair{c.ID, c.Group, "identity", channel, refPath, refPath, vol})
			}
		}
		// Known degraded anchor: 3kHz lowpass, evaluated only at gain 1 on the mix.
		ref := filepath.ToSlash(filepath.Join(dir, "reference-100-mix.wav"))
		anchor := filepath.ToSlash(filepath.Join(dir, "anchor-lowpass.wav"))
		if out, e := exec.Command("ffmpeg", "-v", "error", "-y", "-i", ref, "-af", "lowpass=f=3000", "-c:a", "pcm_s16le", anchor).CombinedOutput(); e != nil {
			return fmt.Errorf("anchor %w: %s", e, out)
		}
		pairs = append(pairs, pair{c.ID, c.Group, "anchor-lowpass", "mix", ref, anchor, 100})
	}
	if err = writeJSON(filepath.Join(root, "manifest.json"), clips); err != nil {
		return err
	}
	if err = writeJSON(filepath.Join(root, "pairs.json"), pairs); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(root, "batch.csv"))
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	w.Write([]string{"reference", "degraded"})
	for _, p := range pairs {
		w.Write([]string{p.Reference, p.Degraded})
	}
	w.Flush()
	return w.Error()
}
func floats(b []byte) []float32 {
	p := make([]float32, len(b)/4)
	for i := range p {
		p[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return p
}
func floatBytes(p []float32) []byte {
	b := make([]byte, len(p)*4)
	for i, x := range p {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return b
}
func readFloatWAV(path string) ([]float32, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	if len(b) < 44 || string(b[36:40]) != "data" || binary.LittleEndian.Uint16(b[20:]) != 3 {
		return nil, fmt.Errorf("unexpected benchmark WAV: %s", path)
	}
	return floats(b[44:]), nil
}
func writeMono(path string, p []float32, channel string, gain float64) error {
	b := make([]byte, 44+len(p))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 48000)
	binary.LittleEndian.PutUint32(b[28:], 96000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(len(p)))
	for i := 0; i < len(p)/2; i++ {
		x := float64(p[2*i]+p[2*i+1]) / 2
		if channel == "left" {
			x = float64(p[2*i])
		}
		if channel == "right" {
			x = float64(p[2*i+1])
		}
		x *= gain
		if math.Abs(x) > 1 {
			return fmt.Errorf("clipping %s sample %s", path, strconv.Itoa(i))
		}
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(int16(math.Round(x*32767))))
	}
	return os.WriteFile(path, b, 0644)
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, b, 0644)
}
