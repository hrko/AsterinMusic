package audio

const DefaultBitrate = 96000

// bitrateControl is owned by the encoding loop. Missing cache values retain
// the last valid target; the callback must be nonblocking and concurrency-safe.
type bitrateControl struct {
	read    func() int
	applied int
}

func (b *bitrateControl) apply(set func(int) error) error {
	target := b.applied
	if target == 0 {
		target = DefaultBitrate
	}
	if b.read != nil {
		if value := b.read(); value >= 8000 && value <= 384000 {
			target = value
		}
	}
	if target == b.applied {
		return nil
	}
	if err := set(target); err != nil {
		return err
	}
	b.applied = target
	return nil
}
