package source

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
)

const MaxBufferedAudio = 256 << 20

// BufferedStream downloads ahead into a bounded temporary file. Playback must
// not stall the remote HTTP connection for minutes while Discord is paced.
// Readers can consume immediately; a complete download is not required first.
func (c *Client) BufferedStream(parent context.Context, raw string, output io.Writer) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	file, err := os.CreateTemp("", "rainmusic-audio-*")
	if err != nil {
		return err
	}
	defer func() { file.Close(); os.Remove(file.Name()) }()
	b := &spool{file: file, ctx: ctx, changed: make(chan struct{}), limit: MaxBufferedAudio}
	downloaded := make(chan error, 1)
	go func() { err := c.Stream(ctx, raw, b); b.finish(err); downloaded <- err }()
	_, copyErr := io.Copy(output, b)
	if copyErr != nil {
		cancel()
	}
	downloadErr := <-downloaded
	if parent.Err() != nil {
		return parent.Err()
	}
	return errors.Join(copyErr, downloadErr)
}

type spool struct {
	mu           sync.Mutex
	file         *os.File
	ctx          context.Context
	size, offset int64
	limit        int64
	done         bool
	err          error
	changed      chan struct{}
}

func (s *spool) signal() { close(s.changed); s.changed = make(chan struct{}) }
func (s *spool) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > s.limit-s.size {
		return 0, errors.New("audio exceeds temporary buffer limit")
	}
	n, err := s.file.WriteAt(p, s.size)
	s.size += int64(n)
	s.signal()
	return n, err
}
func (s *spool) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	s.err = err
	s.signal()
}
func (s *spool) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if err := s.ctx.Err(); err != nil {
			return 0, err
		}
		s.mu.Lock()
		size, done, err, changed := s.size, s.done, s.err, s.changed
		s.mu.Unlock()
		if s.offset < size {
			n, readErr := s.file.ReadAt(p[:min(int64(len(p)), size-s.offset)], s.offset)
			s.offset += int64(n)
			return n, readErr
		}
		if done {
			if err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		select {
		case <-changed:
		case <-s.ctx.Done():
			return 0, s.ctx.Err()
		}
	}
}
