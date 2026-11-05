// Package telemetry owns tracing configuration and bounded SDK lifecycle.
package telemetry

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// Options contains exporter credentials. Never log this value.
type Options struct {
	Enabled  bool              `koanf:"enabled"`
	Endpoint string            `koanf:"endpoint"`
	Headers  map[string]string `koanf:"headers"`
}

// Endpoint is a complete OTLP/HTTP traces URL, including its path.
// Plain HTTP is limited to explicit loopback addresses.
func (o Options) Validate() error {
	if o.Endpoint == "" {
		if o.Enabled {
			return errors.New("tracing.endpoint is required when tracing is enabled")
		}
	} else {
		u, err := url.Parse(o.Endpoint)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") || !strings.HasPrefix(u.Path, "/") {
			return errors.New("invalid tracing.endpoint: expected a complete HTTP(S) traces URL without credentials or query")
		}
		if u.Scheme == "http" {
			ip := net.ParseIP(u.Hostname())
			if ip == nil || !ip.IsLoopback() {
				return errors.New("tracing.endpoint requires HTTPS except for loopback IP addresses")
			}
		}
	}
	if len(o.Headers) > 16 {
		return errors.New("too many tracing.headers")
	}
	seen := map[string]bool{}
	for k, v := range o.Headers {
		key := strings.ToLower(k)
		if !httpguts.ValidHeaderFieldName(k) || !httpguts.ValidHeaderFieldValue(v) || len(k) > 128 || len(v) > 8192 || seen[key] {
			return errors.New("invalid tracing.headers")
		}
		switch key {
		case "host", "content-type", "content-length", "content-encoding", "transfer-encoding", "connection", "trailer", "upgrade":
			return errors.New("reserved tracing header")
		}
		seen[key] = true
	}
	return nil
}
