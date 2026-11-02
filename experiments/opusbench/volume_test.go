package main

import (
	"math"
	"testing"
)

// Volume is applied before each encode, with the same 10ms linear-gain ramp as
// RainMusic. Decode through the reference implementation, not each candidate.
func TestVolumeSteps(t *testing.T) {
	if nativeVersion() == "disabled" {
		t.Skip("native decoder required")
	}
	for _, name := range []string{"libopus", "pion", "gopus"} {
		t.Run(name, func(t *testing.T) {
			e, err := create(name)
			if err != nil {
				t.Fatal(err)
			}
			defer e.close()
			decode, closeDecoder, err := referenceDecoder()
			if err != nil {
				t.Fatal(err)
			}
			defer closeDecoder()
			p := make([]float32, 1920)
			out := make([]float32, 1920)
			packet := make([]byte, 4000)
			current := 1.0
			mute, resume := -1, -1
			baseline := 0.0
			for f := 0; f < 45; f++ {
				target := 1.0
				if f >= 20 && f < 30 {
					target = 0
				}
				step := (target - current) / 480
				for i := 0; i < 960; i++ {
					if i < 480 {
						current += step
					} else {
						current = target
					}
					x := 0.2 * math.Sin(2*math.Pi*440*float64(f*960+i)/48000) * current
					p[2*i] = float32(x)
					p[2*i+1] = float32(x)
				}
				n, err := e.encode(p, packet)
				if err != nil {
					t.Fatal(err)
				}
				n, err = decode(packet[:n], out)
				if err != nil || n != 960 {
					t.Fatalf("decode %d %v", n, err)
				}
				var power float64
				for _, x := range out {
					power += float64(x) * float64(x)
				}
				rms := math.Sqrt(power / 1920)
				if f == 19 {
					baseline = rms
				}
				if f >= 20 && f < 30 && mute < 0 && rms < baseline*0.001 {
					mute = (f - 20 + 1) * 20
				}
				if f >= 30 && resume < 0 && rms > baseline*0.8 {
					resume = (f - 30 + 1) * 20
				}
			}
			if mute < 0 || mute > 100 || resume < 0 || resume > 100 {
				t.Fatalf("mute=%dms resume=%dms", mute, resume)
			}
			t.Logf("decoded mute <=%dms (-60dB relative); resume <=%dms", mute, resume)
		})
	}
}
