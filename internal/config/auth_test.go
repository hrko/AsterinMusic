package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNiconicoAuthConfig(t *testing.T) {
	for _, auth := range []string{
		"method: bad", "method: password\n      username: user", "method: cookies", "method: none\n      password: ''", "method: password\n      username: user\n      password: secret-value\n      cookies_file: ''",
	} {
		o := setup(t, valid+"sources:\n  niconico:\n    auth:\n      "+auth+"\n")
		_, err := Read(o, lookup)
		if err == nil || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("invalid auth not rejected safely: %v", err)
		}
	}
	o := setup(t, valid+"sources:\n  niconico:\n    auth:\n      method: cookies\n      cookies_file: cookies.txt\n")
	path := filepath.Join(filepath.Dir(o.ConfigFile), "cookies.txt")
	if err := os.WriteFile(path, []byte("# Netscape HTTP Cookie File\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Read(o, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if c.NiconicoAuth.CookiesFile != path {
		t.Fatal("cookie path not config-relative")
	}
	o = setup(t, valid+"sources:\n  niconico:\n    auth:\n      method: password\n      username: user\n      password: '${TOKEN}'\n")
	c, err = Read(o, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if c.NiconicoAuth.Password != "secret-value" {
		t.Fatal("password interpolation failed")
	}
}
