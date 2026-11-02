package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

// Packets reads FFmpeg's single-stream Ogg Opus output, preserving packets
// across page boundaries. The sink controls pacing/backpressure.
func Packets(r io.Reader, sink func([]byte) error) error {
	var packet []byte
	var serial, sequence uint32
	pages, headers := 0, 0
	for {
		header := make([]byte, 27)
		_, err := io.ReadFull(r, header)
		if err == io.EOF && pages > 0 && len(packet) == 0 && headers == 2 {
			return nil
		}
		if err != nil {
			return err
		}
		if string(header[:4]) != "OggS" || header[4] != 0 {
			return errors.New("invalid Ogg page")
		}
		if (header[5]&1 != 0) != (len(packet) > 0) {
			return errors.New("invalid Ogg continuation")
		}
		if pages == 0 {
			serial = binary.LittleEndian.Uint32(header[14:])
			sequence = binary.LittleEndian.Uint32(header[18:])
		}
		if binary.LittleEndian.Uint32(header[14:]) != serial || binary.LittleEndian.Uint32(header[18:]) != sequence {
			return errors.New("unexpected Ogg stream or sequence")
		}
		sequence++
		pages++
		laces := make([]byte, int(header[26]))
		if _, err := io.ReadFull(r, laces); err != nil {
			return err
		}
		length := 0
		for _, l := range laces {
			length += int(l)
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(r, body); err != nil {
			return err
		}
		checksum := binary.LittleEndian.Uint32(header[22:])
		clear(header[22:26])
		crc := oggCRC(append(append(header, laces...), body...))
		if crc != checksum {
			return errors.New("Ogg CRC mismatch")
		}
		offset := 0
		for _, l := range laces {
			packet = append(packet, body[offset:offset+int(l)]...)
			offset += int(l)
			if len(packet) > 1<<20 {
				return errors.New("Ogg packet too large")
			}
			if l == 255 {
				continue
			}
			if headers == 0 {
				if len(packet) < 19 || !bytes.HasPrefix(packet, []byte("OpusHead")) || packet[9] != 2 {
					return errors.New("expected stereo Opus header")
				}
				headers++
			} else if headers == 1 {
				if !bytes.HasPrefix(packet, []byte("OpusTags")) {
					return errors.New("expected Opus tags")
				}
				headers++
			} else {
				if len(packet) == 0 {
					return errors.New("empty Opus packet")
				}
				if err := sink(append([]byte(nil), packet...)); err != nil {
					return err
				}
			}
			packet = packet[:0]
		}
	}
}

func oggCRC(data []byte) uint32 {
	var crc uint32
	for _, b := range data {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
