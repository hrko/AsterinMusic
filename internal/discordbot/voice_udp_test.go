package discordbot

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
)

func udpFixture(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	return c
}

func TestVoiceUDPDiscoveryCancellation(t *testing.T) {
	for _, mode := range []string{"cancel", "close", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			server := udpFixture(t)
			u := &voiceUDP{dave: godave.NewNoopSession(slog.Default(), "2", nil)}
			defer u.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, _, err := u.Open(ctx, "127.0.0.1", server.LocalAddr().(*net.UDPAddr).Port, 42)
				result <- err
			}()
			var request [74]byte
			if _, _, err := server.ReadFromUDP(request[:]); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			switch mode {
			case "cancel":
				cancel()
			case "close":
				_ = u.Close()
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("discovery succeeded without response")
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if mode == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
					var timeout net.Error
					if !errors.As(err, &timeout) || !timeout.Timeout() {
						t.Fatal(err)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("real discovery I/O did not terminate")
			}
			if time.Since(start) >= time.Second {
				t.Fatal("fixed dependency timeout was retained")
			}
			if _, err := u.socket(); !errors.Is(err, net.ErrClosed) {
				t.Fatal("failed socket retained")
			}
		})
	}
}

func TestVoiceUDPRTPEncryptionAndResumeContinuity(t *testing.T) {
	for _, mode := range []voice.EncryptionMode{voice.EncryptionModeAEADAES256GCMRTPSize, voice.EncryptionModeAEADXChaCha20Poly1305RTPSize} {
		t.Run(string(mode), func(t *testing.T) {
			server := udpFixture(t)
			u := &voiceUDP{dave: godave.NewNoopSession(slog.Default(), "2", nil)}
			defer u.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			result := make(chan error, 1)
			go func() {
				address, port, err := u.Open(ctx, "127.0.0.1", server.LocalAddr().(*net.UDPAddr).Port, 42)
				if err == nil && (address != "127.0.0.1" || port != 9000) {
					err = errors.New("wrong discovered endpoint")
				}
				result <- err
			}()
			var request [74]byte
			_, peer, err := server.ReadFromUDP(request[:])
			if err != nil {
				t.Fatal(err)
			}
			binary.BigEndian.PutUint16(request[0:2], 2)
			copy(request[8:72], "127.0.0.1")
			binary.BigEndian.PutUint16(request[72:74], 9000)
			if _, err = server.WriteToUDP(request[:], peer); err != nil {
				t.Fatal(err)
			}
			if err = <-result; err != nil {
				t.Fatal(err)
			}
			cancel() // The successful socket must outlive its discovery context.
			key := make([]byte, 32)
			if err = u.SetSecretKey(mode, key); err != nil {
				t.Fatal(err)
			}
			decrypt, err := voice.NewEncrypter(mode, key)
			if err != nil {
				t.Fatal(err)
			}
			for i := range 3 {
				payload := []byte{1, 2, byte(i)}
				if _, err = u.Write(payload); err != nil {
					t.Fatal(err)
				}
				var packet [1500]byte
				n, _, err := server.ReadFromUDP(packet[:])
				if err != nil {
					t.Fatal(err)
				}
				if binary.BigEndian.Uint16(packet[2:4]) != uint16(i) || binary.BigEndian.Uint32(packet[4:8]) != uint32(i*voice.OpusFrameSize) || binary.BigEndian.Uint32(packet[8:12]) != 42 {
					t.Fatal("RTP continuity changed")
				}
				plain, err := decrypt.Decrypt(voice.RTPHeaderSize, packet[:n])
				if err != nil || !bytes.Equal(plain, payload) {
					t.Fatalf("payload: %v %v", plain, err)
				}
			}
		})
	}
}

type observedWriteConn struct {
	net.Conn
	entered chan struct{}
}

func (c observedWriteConn) Write(p []byte) (int, error) { close(c.entered); return c.Conn.Write(p) }

func TestVoiceUDPCloseInterruptsWrite(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	entered := make(chan struct{})
	u := &voiceUDP{conn: observedWriteConn{local, entered}, dave: godave.NewNoopSession(slog.Default(), "2", nil)}
	defer u.Close()
	if err := u.SetSecretKey(voice.EncryptionModeAEADAES256GCMRTPSize, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := u.Write([]byte{1, 2, 3}); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("write never started")
	}
	closed := make(chan struct{})
	go func() { _ = u.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close waited for write lock")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("write not interrupted")
	}
}
