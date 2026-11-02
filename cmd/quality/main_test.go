package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestMonoProjectionAndCommonGain(t *testing.T) {
	p := []float32{0.1, 0.3, -0.1, -0.3}
	for _, tt := range []struct {
		channel string
		want    int16
	}{{"left", 6553}, {"right", 19660}, {"mix", 13107}} {
		path := filepath.Join(t.TempDir(), "audio.wav")
		if err := writeMono(path, p, tt.channel, 2); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got := int16(binary.LittleEndian.Uint16(b[44:]))
		if got < tt.want-1 || got > tt.want+1 {
			t.Fatalf("%s: %d", tt.channel, got)
		}
		if len(b) != 48 || binary.LittleEndian.Uint32(b[40:]) != 4 {
			t.Fatal("invalid WAV length")
		}
	}
	if err := writeMono(filepath.Join(t.TempDir(), "clip.wav"), []float32{1, 1}, "mix", 2); err == nil {
		t.Fatal("clipping accepted")
	}
}
