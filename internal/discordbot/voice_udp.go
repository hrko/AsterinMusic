package discordbot

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
)

// voiceUDP owns the actual socket so cancellation never waits for an I/O
// mutex. Only RTP framing is local; transport encryption remains in disgo,
// and media encryption remains in godave. The Bot does not receive audio.
type voiceUDP struct {
	mu        sync.Mutex
	conn      net.Conn
	closed    bool
	opening   bool
	writeMu   sync.Mutex
	dave      godave.Session
	key       voice.Encrypter
	ssrc      uint32
	sequence  uint16
	timestamp uint32
	buffer    []byte
}

var _ voice.UDPConn = (*voiceUDP)(nil)

func (u *voiceUDP) socket() (net.Conn, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed || u.conn == nil {
		return nil, net.ErrClosed
	}
	return u.conn, nil
}

func (u *voiceUDP) Open(ctx context.Context, ip string, port int, ssrc uint32) (address string, externalPort int, err error) {
	u.mu.Lock()
	if u.closed || u.opening || u.conn != nil {
		u.mu.Unlock()
		return "", 0, net.ErrClosed
	}
	u.opening = true
	u.mu.Unlock()
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return "", 0, err
	}
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		_ = conn.Close()
		return "", 0, net.ErrClosed
	}
	u.conn = conn
	u.mu.Unlock()
	// Close the actual socket on cancellation, including while discovery Read
	// is blocked. Join the callback before returning success to its new owner.
	canceled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.Close(); close(canceled) })
	defer func() {
		if !stop() {
			<-canceled
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			_ = u.Close()
		} else {
			err = conn.SetDeadline(time.Time{})
			if err != nil {
				_ = u.Close()
			}
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return "", 0, err
	}
	var request [74]byte
	binary.BigEndian.PutUint16(request[0:2], 1)
	binary.BigEndian.PutUint16(request[2:4], 70)
	binary.BigEndian.PutUint32(request[4:8], ssrc)
	if _, err = conn.Write(request[:]); err != nil {
		return "", 0, err
	}
	var response [75]byte // Detect overlong datagrams as well as short ones.
	n, err := conn.Read(response[:])
	if err != nil {
		return "", 0, err
	}
	if n != 74 || binary.BigEndian.Uint16(response[0:2]) != 2 || binary.BigEndian.Uint16(response[2:4]) != 70 || binary.BigEndian.Uint32(response[4:8]) != ssrc {
		return "", 0, errors.New("invalid voice UDP discovery response")
	}
	address = string(bytes.TrimRight(response[8:72], "\x00"))
	externalPort = int(binary.BigEndian.Uint16(response[72:74]))
	if net.ParseIP(address) == nil || externalPort == 0 {
		return "", 0, errors.New("invalid voice UDP discovery address")
	}
	u.writeMu.Lock()
	u.ssrc = ssrc
	u.dave.AssignSsrcToCodec(ssrc, godave.CodecOpus)
	u.writeMu.Unlock()
	return address, externalPort, nil
}

func (u *voiceUDP) Close() error {
	u.mu.Lock()
	u.closed = true
	conn := u.conn
	u.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func (u *voiceUDP) SetSecretKey(mode voice.EncryptionMode, secret []byte) error {
	key, err := voice.NewEncrypter(mode, secret)
	if err != nil {
		return err
	}
	u.writeMu.Lock()
	defer u.writeMu.Unlock()
	u.key = key
	return nil
}

func (u *voiceUDP) Write(p []byte) (int, error) {
	u.writeMu.Lock()
	defer u.writeMu.Unlock()
	conn, err := u.socket()
	if err != nil {
		return 0, err
	}
	if u.key == nil {
		return 0, errors.New("voice UDP key not ready")
	}
	var header [voice.RTPHeaderSize]byte
	header[0], header[1] = voice.RTPVersionPadExtend, voice.RTPPayloadType
	binary.BigEndian.PutUint16(header[2:4], u.sequence)
	binary.BigEndian.PutUint32(header[4:8], u.timestamp)
	binary.BigEndian.PutUint32(header[8:12], u.ssrc)
	u.sequence++
	u.timestamp += voice.OpusFrameSize
	size := u.dave.MaxEncryptedFrameSize(len(p))
	if len(u.buffer) < size {
		u.buffer = make([]byte, size)
	}
	n, err := u.dave.Encrypt(u.ssrc, p, u.buffer)
	if err != nil {
		return 0, err
	}
	if _, err = conn.Write(u.key.Encrypt(header, u.buffer[:n])); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (u *voiceUDP) LocalAddr() net.Addr {
	c, e := u.socket()
	if e != nil {
		return nil
	}
	return c.LocalAddr()
}
func (u *voiceUDP) RemoteAddr() net.Addr {
	c, e := u.socket()
	if e != nil {
		return nil
	}
	return c.RemoteAddr()
}
func (u *voiceUDP) SetDeadline(t time.Time) error {
	c, e := u.socket()
	if e != nil {
		return e
	}
	return c.SetDeadline(t)
}
func (u *voiceUDP) SetReadDeadline(t time.Time) error {
	c, e := u.socket()
	if e != nil {
		return e
	}
	return c.SetReadDeadline(t)
}
func (u *voiceUDP) SetWriteDeadline(t time.Time) error {
	c, e := u.socket()
	if e != nil {
		return e
	}
	return c.SetWriteDeadline(t)
}
func (u *voiceUDP) Read([]byte) (int, error)           { return 0, errors.ErrUnsupported }
func (u *voiceUDP) ReadPacket() (*voice.Packet, error) { return nil, errors.ErrUnsupported }
