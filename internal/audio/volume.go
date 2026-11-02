package audio

import (
	"errors"
	"math"
	"sync"
)

const DefaultVolume = 30

func Gain(percent int) (float64, error) {
	if percent < 0 || percent > 100 {
		return 0, errors.New("volume must be between 0 and 100")
	}
	if percent == 0 {
		return 0, nil
	}
	return math.Pow(10, (40*(float64(percent)/100-1))/20), nil
}

// Volume applies a sample-linear ramp to interleaved float32 PCM. Set may be
// called concurrently; Apply must have one audio-loop caller. Frames, not
// individual channel samples, advance the ramp to preserve stereo balance.
type Volume struct {
	mu                    sync.Mutex
	percent               int
	current, target, step float64
	remaining, rampFrames int
}

func NewVolume(sampleRate int) *Volume {
	gain, _ := Gain(DefaultVolume)
	return &Volume{percent: DefaultVolume, current: gain, target: gain, rampFrames: max(1, sampleRate/100)}
}

func (v *Volume) Set(percent int) error {
	gain, err := Gain(percent)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.percent = percent
	v.target = gain
	v.remaining = v.rampFrames
	v.step = (gain - v.current) / float64(v.remaining)
	return nil
}

func (v *Volume) Percent() int { v.mu.Lock(); defer v.mu.Unlock(); return v.percent }

func (v *Volume) Apply(samples []float32, channels int) error {
	if channels < 1 || len(samples)%channels != 0 {
		return errors.New("PCM must contain complete frames")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for i := 0; i < len(samples); i += channels {
		if v.remaining > 0 {
			v.current += v.step
			v.remaining--
			if v.remaining == 0 {
				v.current = v.target
			}
		}
		for c := 0; c < channels; c++ {
			samples[i+c] *= float32(v.current)
		}
	}
	return nil
}
