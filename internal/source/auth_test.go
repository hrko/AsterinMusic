package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const authURL = "https://www.nicovideo.jp/watch/sm46647529"

func testCookies(value string) []byte {
	return []byte("# Netscape HTTP Cookie File\n#HttpOnly_.nicovideo.jp\tTRUE\t/\tTRUE\t4102444800\tuser_session\t" + value + "\n")
}
func argValue(args []string, key string) string {
	for i, a := range args {
		if a == key && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
func newPasswordClient(t *testing.T) *Client {
	t.Helper()
	c := New("fake")
	if err := c.ConfigureAuth(AuthConfig{Method: "password", Username: "private-user", Password: `secret "with\spaces`}); err != nil {
		t.Fatal(err)
	}
	return c
}
func writeSession(args []string) error {
	return os.WriteFile(argValue(args, "--cookies"), testCookies("session-secret"), 0600)
}

func TestCookieIsolationReplacementAndCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.txt")
	original := append(testCookies("first"), []byte(".youtube.com\tTRUE\t/\tTRUE\t0\tOTHER\tprivate\n")...)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	c := New("fake")
	if err := c.ConfigureAuth(AuthConfig{Method: "cookies", CookiesFile: path}); err != nil {
		t.Fatal(err)
	}
	var dirs []string
	expected := "first"
	calls := 0
	c.Run = func(ctx context.Context, _ string, args []string, in io.Reader, out io.Writer) error {
		calls++
		jar := argValue(args, "--cookies")
		if jar == path || jar == "" {
			t.Fatal("original cookie path passed")
		}
		dirs = append(dirs, filepath.Dir(jar))
		b, err := os.ReadFile(jar)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "youtube") || !strings.Contains(string(b), expected) {
			t.Fatal("cookie isolation/reload failed")
		}
		if err = os.WriteFile(jar, testCookies("mutated"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err = out.Write([]byte("audio"))
		return err
	}
	for i := 0; i < 2; i++ {
		if err := c.Stream(context.Background(), authURL, io.Discard); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(path)
		if !bytes.Equal(b, original) {
			t.Fatal("source file modified")
		}
		expected = "second"
		original = testCookies(expected)
		os.WriteFile(path, original, 0600)
	}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("private files survived")
		}
	}
	c.Run = func(_ context.Context, _ string, args []string, _ io.Reader, out io.Writer) error {
		if argValue(args, "--cookies") != "" || argValue(args, "--netrc-location") != "" {
			t.Fatal("credentials sent to YouTube")
		}
		_, err := out.Write([]byte("audio"))
		return err
	}
	if err := c.Stream(context.Background(), "https://www.youtube.com/watch?v=PE_iyDsY5xM", io.Discard); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestPasswordReuseSecretsAndExpiry(t *testing.T) {
	c := newPasswordClient(t)
	logins, calls := 0, 0
	var dirs []string
	c.Run = func(_ context.Context, _ string, args []string, in io.Reader, out io.Writer) error {
		if strings.Contains(strings.Join(args, " "), "private-user") || strings.Contains(strings.Join(args, " "), "secret") {
			t.Fatal("secret argument")
		}
		dirs = append(dirs, filepath.Dir(argValue(args, "--cookies")))
		if netrc := argValue(args, "--netrc-location"); netrc != "" {
			logins++
			b, err := os.ReadFile(netrc)
			if err != nil || !strings.Contains(string(b), `machine niconico login "private-user"`) {
				t.Fatal("missing netrc")
			}
			found := false
			for i, a := range args {
				if a == "--twofactor" && i+1 < len(args) && args[i+1] == "" {
					found = true
				}
			}
			if !found || in != nil {
				t.Fatal("interactive authentication")
			}
			return writeSession(args)
		}
		calls++
		if calls == 2 {
			return errors.New("Invalid session, re-login required session-secret")
		}
		_, err := out.Write([]byte(`{"id":"sm1","title":"test","availability":"public"}`))
		return err
	}
	if _, err := c.Inspect(context.Background(), authURL); err != nil {
		t.Fatal(err)
	}
	if err := c.Stream(context.Background(), authURL, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := c.Stream(context.Background(), authURL, io.Discard); err != nil {
		t.Fatal(err)
	}
	if logins != 2 || calls != 4 {
		t.Fatalf("logins=%d calls=%d", logins, calls)
	}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("credentials not cleaned")
		}
	}
}

func TestLoginCircuitAndRedaction(t *testing.T) {
	for _, message := range []string{"Unable to login: invalid password private-user", "Unable to login: MFA challenge failed session-secret", "CAPTCHA required"} {
		t.Run(message, func(t *testing.T) {
			c := newPasswordClient(t)
			calls := 0
			c.Run = func(context.Context, string, []string, io.Reader, io.Writer) error {
				calls++
				return errors.New(message)
			}
			for i := 0; i < 3; i++ {
				err := c.Stream(context.Background(), authURL, io.Discard)
				if !errors.Is(err, AuthLogin) {
					t.Fatalf("unexpected safe error: %v", err)
				}
			}
			if calls != 1 {
				t.Fatal("repeated failed logins")
			}
		})
	}
	c := newPasswordClient(t)
	calls := 0
	c.Run = func(_ context.Context, _ string, args []string, _ io.Reader, _ io.Writer) error {
		calls++
		if argValue(args, "--netrc-location") != "" {
			return writeSession(args)
		}
		return errors.New("Invalid session, re-login required")
	}
	for i := 0; i < 2; i++ {
		if err := c.Stream(context.Background(), authURL, io.Discard); !errors.Is(err, AuthLogin) {
			t.Fatal(err)
		}
	}
	if calls != 4 {
		t.Fatalf("expiry retry circuit: %d", calls)
	}
}

func TestConcurrentLoginAndCancelledWait(t *testing.T) {
	c := newPasswordClient(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var logins atomic.Int32
	c.Run = func(ctx context.Context, _ string, args []string, _ io.Reader, out io.Writer) error {
		if argValue(args, "--netrc-location") != "" {
			if logins.Add(1) == 1 {
				close(started)
			}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return writeSession(args)
		}
		_, err := out.Write([]byte("audio"))
		return err
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- c.Stream(context.Background(), authURL, io.Discard) }()
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Stream(ctx, authURL, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if logins.Load() != 1 {
		t.Fatal("concurrent login")
	}
}

func TestCookieErrorsAndCancellationCleanup(t *testing.T) {
	for _, expiry := range []string{"", "0"} {
		b := bytes.ReplaceAll(testCookies("x"), []byte("4102444800"), []byte(expiry))
		if _, err := cleanCookies(b); err != nil || !hasSession(b) {
			t.Fatal("session cookie rejected")
		}
	}
	for _, b := range [][]byte{[]byte("bad"), []byte("# Netscape HTTP Cookie File\nbad"), bytes.ReplaceAll(testCookies("x"), []byte("4102444800"), []byte("invalid"))} {
		if _, err := cleanCookies(b); err == nil {
			t.Fatal("invalid cookie accepted")
		}
	}
	if hasSession(bytes.ReplaceAll(testCookies("x"), []byte("4102444800"), []byte("1"))) {
		t.Fatal("expired session accepted")
	}
	path := filepath.Join(t.TempDir(), "cookies.txt")
	os.WriteFile(path, testCookies("x"), 0600)
	c := New("fake")
	if err := c.ConfigureAuth(AuthConfig{Method: "cookies", CookiesFile: path}); err != nil {
		t.Fatal(err)
	}
	var dir string
	c.Run = func(ctx context.Context, _ string, args []string, _ io.Reader, _ io.Writer) error {
		dir = filepath.Dir(argValue(args, "--cookies"))
		<-ctx.Done()
		return fmt.Errorf("private upstream: %w", ctx.Err())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := c.Stream(ctx, authURL, io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("cancelled request left cookie copy")
	}
	c.Run = func(context.Context, string, []string, io.Reader, io.Writer) error {
		return errors.New("network error: session-secret")
	}
	if err := c.Stream(context.Background(), authURL, io.Discard); err == nil || strings.Contains(err.Error(), "session-secret") {
		t.Fatal("error not redacted")
	}
}

func TestNetrcReadableByPython(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skip("Python not installed; run through mise for netrc parser verification")
	}
	c := newPasswordClient(t)
	c.Run = func(ctx context.Context, _ string, args []string, _ io.Reader, out io.Writer) error {
		if path := argValue(args, "--netrc-location"); path != "" {
			cmd := exec.CommandContext(ctx, python, "-c", `import json,netrc,sys; json.dump(netrc.netrc(sys.argv[1]).authenticators('niconico'),sys.stdout)`, path)
			b, err := cmd.Output()
			if err != nil {
				return errors.New("Python could not parse private netrc")
			}
			var credentials []string
			if err = json.Unmarshal(b, &credentials); err != nil {
				return errors.New("invalid netrc parser response")
			}
			if len(credentials) != 3 || credentials[0] != c.auth.config.Username || credentials[2] != c.auth.config.Password {
				return errors.New("netrc did not preserve credentials")
			}
			return writeSession(args)
		}
		_, err := out.Write([]byte("audio"))
		return err
	}
	if err = c.Stream(context.Background(), authURL, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestPartialAudioIsNeverRetried(t *testing.T) {
	c := newPasswordClient(t)
	calls := 0
	c.Run = func(_ context.Context, _ string, args []string, _ io.Reader, out io.Writer) error {
		calls++
		if argValue(args, "--netrc-location") != "" {
			return writeSession(args)
		}
		out.Write([]byte("prefix"))
		return errors.New("Invalid session, re-login required")
	}
	var b bytes.Buffer
	if err := c.Stream(context.Background(), authURL, &b); err == nil {
		t.Fatal("expected failure")
	}
	if calls != 2 || b.String() != "prefix" {
		t.Fatal("partially emitted audio replayed")
	}
}
