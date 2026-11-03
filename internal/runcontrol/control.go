// Package runcontrol provides authenticated loopback-only development control.
// The listening socket is also a process-lifetime single-instance lock.
package runcontrol

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

type State struct {
	Playback   *Playback `json:"playback,omitempty"`
	Arguments  []string  `json:"arguments,omitempty"`
	PID        int       `json:"pid"`
	Executable string    `json:"executable"`
	Token      string    `json:"token"`
	Address    string    `json:"address"`
	Ready      bool      `json:"ready"`
}
type Playback struct {
	Active   bool   `json:"active"`
	Started  bool   `json:"started"`
	Sequence uint64 `json:"sequence"`
}
type Control struct {
	server      *http.Server
	listener    net.Listener
	state       State
	ready       atomic.Bool
	path        string
	stopMu      sync.Mutex
	prepareStop func() error
	playback    func() Playback
}
type key struct{}

func Address(root string, operation bool) string {
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	port := 30000 + int(binary.LittleEndian.Uint16(sum[:2])%10000)*2
	if operation {
		port++
	}
	return fmt.Sprintf("127.0.0.1:%d", port)
}
func Path(root string) string { return filepath.Join(root, ".dev", "run", "control.json") }
func Start(ctx context.Context, root string, cancel context.CancelFunc, arguments ...string) (context.Context, *Control, error) {
	listener, err := net.Listen("tcp", Address(root, false))
	if err != nil {
		return ctx, nil, fmt.Errorf("another instance or port conflict: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		listener.Close()
		return ctx, nil, err
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		listener.Close()
		return ctx, nil, err
	}
	c := &Control{listener: listener, path: Path(root), state: State{Arguments: append([]string(nil), arguments...), PID: os.Getpid(), Executable: exe, Address: listener.Addr().String(), Token: hex.EncodeToString(token)}}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if !c.authorize(w, r) {
			return
		}
		s := c.state
		s.Ready = c.ready.Load()
		s.Token = ""
		c.stopMu.Lock()
		readPlayback := c.playback
		c.stopMu.Unlock()
		if readPlayback != nil {
			p := readPlayback()
			s.Playback = &p
		}
		json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		if !c.authorize(w, r) {
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		c.ready.Store(false)
		c.stopMu.Lock()
		defer c.stopMu.Unlock()
		if c.prepareStop != nil {
			if err := c.prepareStop(); err != nil {
				c.ready.Store(true)
				http.Error(w, "checkpoint failed; bot remains running", 500)
				return
			}
		}
		cancel()
		w.WriteHeader(202)
	})
	c.server = &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	if err = os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		listener.Close()
		return ctx, nil, err
	}
	b, _ := json.Marshal(c.state)
	if err = os.WriteFile(c.path, b, 0600); err != nil {
		listener.Close()
		return ctx, nil, err
	}
	go c.server.Serve(listener)
	return context.WithValue(ctx, key{}, c), c, nil
}
func (c *Control) authorize(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+c.state.Token {
		w.WriteHeader(403)
		return false
	}
	return true
}
func (c *Control) Close() { os.Remove(c.path); c.server.Close() }
func Ready(ctx context.Context) {
	if c, ok := ctx.Value(key{}).(*Control); ok {
		c.ready.Store(true)
	}
}

// BeforeStop checkpoints runtime state before cancellation can clear it.
func BeforeStop(ctx context.Context, fn func() error) {
	if c, ok := ctx.Value(key{}).(*Control); ok {
		c.stopMu.Lock()
		defer c.stopMu.Unlock()
		c.prepareStop = fn
	}
}

// ObservePlayback exposes only playback lifecycle state, never titles or URLs.
func ObservePlayback(ctx context.Context, fn func() Playback) {
	if c, ok := ctx.Value(key{}).(*Control); ok {
		c.stopMu.Lock()
		c.playback = fn
		c.stopMu.Unlock()
	}
}
func Request(root, action string) (State, error) {
	var stored State
	b, err := os.ReadFile(Path(root))
	if err != nil {
		return stored, err
	}
	if err = json.Unmarshal(b, &stored); err != nil {
		return stored, err
	}
	if stored.Address != Address(root, false) || len(stored.Token) != 64 {
		return State{}, errors.New("invalid control metadata")
	}
	method := "GET"
	if action == "stop" {
		method = "POST"
	}
	req, err := http.NewRequest(method, "http://"+stored.Address+"/"+action, nil)
	if err != nil {
		return State{}, err
	}
	req.Header.Set("Authorization", "Bearer "+stored.Token)
	client := http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return State{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 202 {
		return State{}, fmt.Errorf("control returned %d", response.StatusCode)
	}
	if action == "stop" {
		return State{}, nil
	}
	var state State
	err = json.NewDecoder(response.Body).Decode(&state)
	if err == nil && (state.PID != stored.PID || state.Executable != stored.Executable) {
		err = errors.New("control identity mismatch")
	}
	return state, err
}
