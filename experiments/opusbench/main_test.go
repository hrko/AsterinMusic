package main

import (
	"math"
	"testing"
)

func TestQualityAlignment(t *testing.T) {
	src := fixture()[:48000*2]
	dst := append(make([]float32, 312*2), src...)
	dst = append(dst, make([]float32, 1920)...)
	delay, snr, gain := quality(src, dst)
	if delay != 312 || snr < 100 || math.Abs(gain-1) > 1e-9 {
		t.Fatalf("delay=%d snr=%g gain=%g", delay, snr, gain)
	}
	for i := range dst {
		dst[i] *= 0.5
	}
	_, snr, gain = quality(src, dst)
	if math.Abs(gain-0.5) > 1e-9 || math.Abs(snr-6.0206) > 0.001 {
		t.Fatalf("gain=%g snr=%g", gain, snr)
	}
}
