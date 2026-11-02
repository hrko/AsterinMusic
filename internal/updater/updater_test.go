package updater

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBadHashDoesNotSelectCandidate(t *testing.T) {
	m := &Manager{Root: t.TempDir()}
	name, err := asset()
	if err != nil {
		t.Skip(err)
	}
	m.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		body := "corrupted binary"
		if strings.HasSuffix(r.URL.Path, "SHA2-256SUMS") {
			body = strings.Repeat("0", 64) + "  " + name
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	if err = m.Apply(context.Background(), "2026.08.19", "https://www.youtube.com/watch?v=aqz-KE-bpKQ"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatal(err)
	}
	if p, err := m.Executable(); err != nil || p != "yt-dlp" {
		t.Fatal(p, err)
	}
	if _, err = os.Stat(filepath.Join(m.Root, "update.lock")); !os.IsNotExist(err) {
		t.Fatal("lock retained")
	}
}
func TestRollbackToMiseAndInvalidState(t *testing.T) {
	m := &Manager{Root: t.TempDir()}
	if err := m.save(State{Current: "2026.08.19"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Rollback(); err != nil {
		t.Fatal(err)
	}
	if p, err := m.Executable(); err != nil || p != "yt-dlp" {
		t.Fatal(p, err)
	}
	if err := os.WriteFile(filepath.Join(m.Root, "state.json"), []byte(`{"current":"../../escape"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Executable(); err == nil {
		t.Fatal("unsafe state accepted")
	}
}
