// Package updater manages immutable yt-dlp versions outside mise's tool store.
package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"rainmusic/internal/process"
	"rainmusic/internal/source"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var versionPattern = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}(?:\.\d+)?$`)

type State struct {
	Current  string `json:"current"`
	Previous string `json:"previous"`
}
type Manager struct {
	Root string
	HTTP *http.Client
}

func New() *Manager {
	root := os.Getenv("RAINMUSIC_UPDATE_DIR")
	if root == "" {
		root = filepath.Join(".dev", "yt-dlp")
	}
	return &Manager{Root: root, HTTP: &http.Client{Timeout: 60 * time.Second}}
}
func (m *Manager) state() (State, error) {
	var s State
	b, err := os.ReadFile(filepath.Join(m.Root, "state.json"))
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	if err == nil {
		for _, v := range []string{s.Current, s.Previous} {
			if v != "" && !versionPattern.MatchString(v) {
				return s, errors.New("invalid updater state")
			}
		}
	}
	return s, err
}
func asset() (string, error) {
	if runtime.GOARCH != "amd64" {
		return "", errors.New("updates require amd64")
	}
	switch runtime.GOOS {
	case "windows":
		return "yt-dlp.exe", nil
	case "linux":
		return "yt-dlp_linux", nil
	}
	return "", errors.New("unsupported update platform")
}
func (m *Manager) path(v string) (string, error) {
	a, err := asset()
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(m.Root, v, a))
}
func (m *Manager) Executable() (string, error) {
	s, err := m.state()
	if err != nil {
		return "", err
	}
	if s.Current == "" {
		return "yt-dlp", nil
	}
	p, err := m.path(s.Current)
	if err != nil {
		return "", err
	}
	_, err = os.Stat(p)
	return p, err
}
func (m *Manager) get(ctx context.Context, url string, w io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "RainMusic-updater")
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return errors.New("update download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("update HTTP status %d", resp.StatusCode)
	}
	_, err = io.Copy(w, &io.LimitedReader{R: resp.Body, N: limit + 1})
	return err
}
func (m *Manager) Latest(ctx context.Context) (string, error) {
	var b bytes.Buffer
	if err := m.get(ctx, "https://api.github.com/repos/yt-dlp/yt-dlp/releases/latest", &b, 1<<20); err != nil {
		return "", err
	}
	if b.Len() > 1<<20 {
		return "", errors.New("release response too large")
	}
	var r struct {
		Tag string `json:"tag_name"`
	}
	if err := json.Unmarshal(b.Bytes(), &r); err != nil {
		return "", err
	}
	if !versionPattern.MatchString(r.Tag) {
		return "", errors.New("invalid release version")
	}
	return r.Tag, nil
}
func (m *Manager) locked(fn func() error) error {
	if err := os.MkdirAll(m.Root, 0700); err != nil {
		return err
	}
	p := filepath.Join(m.Root, "update.lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("another update is active (or update.lock remains after interruption)")
	}
	f.Close()
	defer os.Remove(p)
	return fn()
}
func (m *Manager) save(s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(m.Root, "state-*.tmp")
	if err != nil {
		return err
	}
	p := f.Name()
	defer os.Remove(p)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(p, filepath.Join(m.Root, "state.json"))
}

// Apply verifies a release hash, its version, and a supplied public sample URL
// before selecting it for the next process start. Existing processes keep theirs.
func (m *Manager) Apply(ctx context.Context, version, sample string) error {
	if !versionPattern.MatchString(version) {
		return errors.New("invalid release version")
	}
	if _, err := source.Parse(sample); err != nil {
		return err
	}
	return m.locked(func() error {
		s, err := m.state()
		if err != nil {
			return err
		}
		if s.Current == version {
			return errors.New("version already selected")
		}
		name, err := asset()
		if err != nil {
			return err
		}
		base := "https://github.com/yt-dlp/yt-dlp/releases/download/" + version + "/"
		var checks bytes.Buffer
		if err = m.get(ctx, base+"SHA2-256SUMS", &checks, 1<<20); err != nil {
			return err
		}
		expected, err := checksum(checks.String(), name)
		if err != nil {
			return err
		}
		dir := filepath.Join(m.Root, version)
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		f, err := os.CreateTemp(dir, "download-*")
		if err != nil {
			return err
		}
		tmp := f.Name()
		defer os.Remove(tmp)
		hash := sha256.New()
		err = m.get(ctx, base+name, io.MultiWriter(f, hash), 150<<20)
		stat, statErr := f.Stat()
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if statErr != nil {
			return statErr
		}
		if closeErr != nil {
			return closeErr
		}
		if stat.Size() > 150<<20 {
			return errors.New("release binary too large")
		}
		if hex.EncodeToString(hash.Sum(nil)) != expected {
			return errors.New("release checksum mismatch")
		}
		if err = os.Chmod(tmp, 0700); err != nil {
			return err
		}
		target, err := m.path(version)
		if err != nil {
			return err
		}
		if err = os.Rename(tmp, target); err != nil {
			return err
		}
		probe, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		var out bytes.Buffer
		if err = process.Run(probe, target, []string{"--version"}, nil, &out); err != nil {
			return errors.New("candidate version probe failed")
		}
		if strings.TrimSpace(out.String()) != version {
			return errors.New("candidate version mismatch")
		}
		result, err := source.New(target).Inspect(probe, sample)
		if err != nil || len(result.Tracks) == 0 {
			return errors.New("candidate sample verification failed; current version unchanged")
		}
		return m.save(State{Current: version, Previous: s.Current})
	})
}
func (m *Manager) Rollback() error {
	return m.locked(func() error {
		s, err := m.state()
		if err != nil {
			return err
		}
		if s.Current == "" {
			return errors.New("no managed version selected")
		}
		if s.Previous != "" {
			p, e := m.path(s.Previous)
			if e != nil {
				return e
			}
			if _, e = os.Stat(p); e != nil {
				return e
			}
		}
		return m.save(State{Current: s.Previous, Previous: s.Current})
	})
}
func checksum(text, name string) (string, error) {
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			if b, err := hex.DecodeString(f[0]); err == nil && len(b) == 32 {
				return strings.ToLower(f[0]), nil
			}
		}
	}
	return "", errors.New("release checksum not found")
}
