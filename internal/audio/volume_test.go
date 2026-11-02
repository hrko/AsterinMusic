package audio

import (
	"math"
	"testing"
)

func TestGain(t *testing.T) {
	for _, tc := range []struct {
		p    int
		gain float64
	}{{0, 0}, {30, 0.039810717055349734}, {50, 0.1}, {100, 1}} {
		g, err := Gain(tc.p)
		if err != nil || math.Abs(g-tc.gain) > 1e-9 {
			t.Fatalf("%d: %g %v", tc.p, g, err)
		}
	}
	if _, err := Gain(101); err == nil {
		t.Fatal("accepted invalid volume")
	}
}

func TestRampAndMute(t *testing.T) {
	v := NewVolume(48000)
	v.Set(0)
	samples := make([]float32, 960)
	for i := range samples {
		samples[i] = 1
	}
	if err := v.Apply(samples, 2); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(samples); i += 2 {
		if samples[i] != samples[i+1] {
			t.Fatal("stereo imbalance")
		}
		if i > 0 && samples[i] > samples[i-2] {
			t.Fatal("nonmonotonic fade")
		}
	}
	if samples[len(samples)-1] != 0 {
		t.Fatal("not muted after 10ms")
	}
}
