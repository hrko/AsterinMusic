package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func page(seq uint32, flags byte, laces, body []byte) []byte {
	h := make([]byte, 27)
	copy(h, "OggS")
	h[5] = flags
	h[26] = byte(len(laces))
	binary.LittleEndian.PutUint32(h[18:], seq)
	h = append(h, laces...)
	h = append(h, body...)
	binary.LittleEndian.PutUint32(h[22:], oggCRC(h))
	return h
}
func TestOggPacketsContinuationAndCRC(t *testing.T) {
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[9] = 2
	stream := page(0, 2, []byte{19}, head)
	stream = append(stream, page(1, 0, []byte{8}, []byte("OpusTags"))...)
	payload := bytes.Repeat([]byte{42}, 300)
	stream = append(stream, page(2, 0, []byte{255}, payload[:255])...)
	stream = append(stream, page(3, 5, []byte{45}, payload[255:])...)
	count := 0
	if err := Packets(bytes.NewReader(stream), func(p []byte) error {
		count++
		if !bytes.Equal(p, payload) {
			t.Fatal("packet mismatch")
		}
		return nil
	}); err != nil || count != 1 {
		t.Fatal(err, count)
	}
	sentinel := errors.New("sink")
	if err := Packets(bytes.NewReader(stream), func([]byte) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	stream[len(stream)-1] ^= 1
	if err := Packets(bytes.NewReader(stream), func([]byte) error { return nil }); err == nil {
		t.Fatal("CRC accepted")
	}
}
