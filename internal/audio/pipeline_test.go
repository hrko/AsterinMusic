package audio

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

func TestPCMPartialFinalBlock(t *testing.T) {
	var input, output bytes.Buffer
	for i := 0; i < 6; i++ {
		binary.Write(&input, binary.LittleEndian, float32(0.5))
	}
	if err := applyPCM(&input, &output, NewVolume(48000)); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 24 {
		t.Fatal(output.Len())
	}
	gain, _ := Gain(30)
	got := math.Float32frombits(binary.LittleEndian.Uint32(output.Bytes()))
	if math.Abs(float64(got)-0.5*gain) > 1e-7 {
		t.Fatal(got)
	}
	if err := applyPCM(bytes.NewReader([]byte{1, 2, 3}), &output, NewVolume(48000)); err == nil {
		t.Fatal("accepted incomplete PCM")
	}
}
