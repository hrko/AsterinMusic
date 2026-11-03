package source

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// AuthConfig contains secrets. Never log this value.
type AuthConfig struct{ Method, CookiesFile, Username, Password string }

type AuthError string

func (e AuthError) Error() string { return string(e) }

const (
	AuthRequired  AuthError = "ニコニコ動画のログインが必要です。管理者が認証設定を確認してください。"
	AuthCookies   AuthError = "ニコニコ動画のCookieを読み込めないか、期限切れです。管理者がCookieファイルを更新してください。"
	AuthLogin     AuthError = "ニコニコ動画へのログインに失敗しました。認証設定を確認してBotを再起動してください。追加認証が必要な場合はCookie方式を使用してください。"
	AuthTemporary AuthError = "ニコニコ動画へのログインを一時的に完了できませんでした。1分ほど待って再実行してください。"
)

func AuthMessage(err error, fallback string) string {
	var e AuthError
	if errors.As(err, &e) {
		return e.Error()
	}
	return fallback
}

func ValidateAuth(c AuthConfig) error {
	switch c.Method {
	case "", "none":
		if c.CookiesFile != "" || c.Username != "" || c.Password != "" {
			return errors.New("niconico auth none cannot contain credentials")
		}
	case "cookies":
		if c.CookiesFile == "" || c.Username != "" || c.Password != "" {
			return errors.New("niconico cookies auth requires only cookies_file")
		}
		if _, err := readCookies(c.CookiesFile); err != nil {
			return err
		}
	case "password":
		if strings.TrimSpace(c.Username) == "" || c.Password == "" || c.CookiesFile != "" || strings.ContainsAny(c.Username+c.Password, "\x00\r\n") {
			return errors.New("niconico password auth requires username and password only (no NUL/newlines)")
		}
	default:
		return errors.New("unknown niconico auth method")
	}
	return nil
}

type nicoAuth struct {
	config     AuthConfig
	gate       chan struct{}
	cookies    []byte
	generation uint64
	blocked    bool
	retryAfter time.Time
	now        func() time.Time
}

func (a *nicoAuth) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *nicoAuth) temporaryFailure() error {
	a.retryAfter = a.clock().Add(time.Minute)
	return AuthTemporary
}

// ConfigureAuth must be called before the client is used concurrently.
func (c *Client) ConfigureAuth(config AuthConfig) error {
	if err := ValidateAuth(config); err != nil {
		return err
	}
	c.auth = &nicoAuth{config: config, gate: make(chan struct{}, 1)}
	return nil
}

const cookieLimit = 4 << 20

func readCookies(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, AuthCookies
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, cookieLimit+1))
	if err != nil || len(b) > cookieLimit {
		return nil, AuthCookies
	}
	return cleanCookies(b)
}

// Restrict even an all-browser export to the Niconico domain. Do not preserve comments.
func cleanCookies(b []byte) ([]byte, error) {
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if len(lines) == 0 || (lines[0] != "# Netscape HTTP Cookie File" && lines[0] != "# HTTP Cookie File") {
		return nil, AuthCookies
	}
	var out strings.Builder
	out.WriteString("# Netscape HTTP Cookie File\n")
	for _, line := range lines[1:] {
		if line == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly_")) {
			continue
		}
		fields := strings.Split(strings.TrimPrefix(line, "#HttpOnly_"), "\t")
		if len(fields) != 7 {
			return nil, AuthCookies
		}
		expires := fields[4]
		if expires == "" {
			expires = "0"
		}
		exp, err := strconv.ParseInt(expires, 10, 64)
		if err != nil || exp < 0 || (fields[1] != "TRUE" && fields[1] != "FALSE") || (fields[3] != "TRUE" && fields[3] != "FALSE") || !strings.HasPrefix(fields[2], "/") || strings.ContainsAny(line, "\x00\r") || (fields[1] == "TRUE") != strings.HasPrefix(fields[0], ".") {
			return nil, AuthCookies
		}
		domain := strings.TrimPrefix(strings.ToLower(fields[0]), ".")
		if domain == "nicovideo.jp" || strings.HasSuffix(domain, ".nicovideo.jp") {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return []byte(out.String()), nil
}
func hasSession(b []byte) bool {
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(strings.TrimPrefix(line, "#HttpOnly_"), "\t")
		if len(f) != 7 || f[5] != "user_session" || f[6] == "" {
			continue
		}
		d := strings.TrimPrefix(strings.ToLower(f[0]), ".")
		if d != "nicovideo.jp" && d != "www.nicovideo.jp" {
			continue
		}
		exp, _ := strconv.ParseInt(f[4], 10, 64)
		if exp == 0 || exp > time.Now().Unix() {
			return true
		}
	}
	return false
}
func privateFiles(cookies []byte) (dir, jar string, err error) {
	dir, err = os.MkdirTemp("", "asterinmusic-auth-")
	if err != nil {
		return "", "", errors.New("cannot create private authentication directory")
	}
	if err = secureAuthDir(dir); err == nil {
		err = os.WriteFile(filepath.Join(dir, "cookies.txt"), cookies, 0600)
	}
	if err != nil {
		os.RemoveAll(dir)
		return "", "", errors.New("cannot secure authentication files")
	}
	return dir, filepath.Join(dir, "cookies.txt"), nil
}
func insertArgs(args, extra []string) []string {
	out := make([]string, 0, len(args)+len(extra))
	out = append(out, extra...)
	return append(out, args...)
}
func sessionExpired(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "invalid session, re-login required") || strings.Contains(s, "sensitive content, login required")
}
func loginFailure(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unable to login:") || strings.Contains(s, "mfa") || strings.Contains(s, "captcha")
}
func safeNicoError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	return errors.New("niconico extraction failed (upstream details withheld)")
}

func (a *nicoAuth) session(ctx context.Context, c *Client, url string, stale *uint64) ([]byte, uint64, error) {
	select {
	case a.gate <- struct{}{}:
		defer func() { <-a.gate }()
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	if a.blocked {
		return nil, 0, AuthLogin
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if a.clock().Before(a.retryAfter) {
		return nil, 0, AuthTemporary
	}
	if stale != nil && *stale == a.generation {
		a.cookies = nil
	}
	if hasSession(a.cookies) {
		return append([]byte(nil), a.cookies...), a.generation, nil
	}
	dir, jar, err := privateFiles([]byte("# Netscape HTTP Cookie File\n"))
	if err != nil {
		return nil, 0, err
	}
	defer os.RemoveAll(dir)
	quote := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	netrc := filepath.Join(dir, "netrc")
	if os.WriteFile(netrc, []byte("machine niconico login "+quote(a.config.Username)+" password "+quote(a.config.Password)+"\n"), 0600) != nil {
		return nil, 0, errors.New("cannot write private authentication file")
	}
	args := append([]string(nil), baseArgs...)
	args = append(args, "--cookies", jar, "--netrc", "--netrc-location", netrc, "--twofactor", "", "--skip-download", "--no-playlist", "--", url)
	loginCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	err = c.Run(loginCtx, c.Executable, args, nil, io.Discard)
	cookies, readErr := readCookies(jar)
	if loginCtx.Err() != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, a.temporaryFailure()
	}
	if readErr == nil && hasSession(cookies) {
		a.cookies = cookies
		a.generation++
		a.retryAfter = time.Time{}
		return append([]byte(nil), cookies...), a.generation, nil
	}
	if err == nil || loginFailure(err) || sessionExpired(err) {
		a.blocked = true
		return nil, 0, AuthLogin
	}
	return nil, 0, a.temporaryFailure()
}

func (c *Client) runSource(ctx context.Context, input Input, args []string, out io.Writer) error {
	if input.Site != "niconico" {
		return c.Run(ctx, c.Executable, args, nil, out)
	}
	a := c.auth
	if a == nil || a.config.Method == "" || a.config.Method == "none" {
		err := c.Run(ctx, c.Executable, args, nil, out)
		if sessionExpired(err) {
			return AuthRequired
		}
		return err
	}
	var stale *uint64
	for attempt := 0; attempt < 2; attempt++ {
		var cookies []byte
		var gen uint64
		var err error
		if a.config.Method == "cookies" {
			cookies, err = readCookies(a.config.CookiesFile)
		} else {
			cookies, gen, err = a.session(ctx, c, input.URL, stale)
		}
		if err != nil {
			return err
		}
		if !hasSession(cookies) {
			return AuthCookies
		}
		dir, jar, err := privateFiles(cookies)
		if err != nil {
			return err
		}
		count := &countingWriter{Writer: out}
		err = c.Run(ctx, c.Executable, insertArgs(args, []string{"--cookies", jar}), nil, count)
		os.RemoveAll(dir)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !sessionExpired(err) {
			return safeNicoError(ctx, err)
		}
		if a.config.Method == "cookies" {
			return AuthCookies
		}
		if attempt == 1 || count.n > 0 {
			select {
			case a.gate <- struct{}{}:
				if gen == a.generation {
					a.blocked = true
				}
				<-a.gate
			case <-ctx.Done():
				return ctx.Err()
			}
			return AuthLogin
		}
		// Retry only before any bytes have reached the consumer.
		stale = &gen
	}
	return AuthLogin
}
