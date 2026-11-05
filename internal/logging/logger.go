// Package logging configures slog output; callers own event selection and safe attributes.
package logging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"strconv"
	"strings"
	"unicode"

	"go.opentelemetry.io/otel/trace"
)

type Options struct {
	Level      string            `koanf:"level"`
	Format     string            `koanf:"format"`
	Components map[string]string `koanf:"components"`
}

var components = map[string]bool{"app": true, "voice": true, "playback": true, "source": true, "history": true, "state": true, "interaction": true, "updater": true, "discord": true, "dave": true}

func level(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, errors.New("invalid logging level")
	}
}

func (o Options) Validate() error {
	if _, err := level(o.Level); err != nil {
		return err
	}
	if o.Format != "" && o.Format != "text" && o.Format != "json" {
		return errors.New("invalid logging format")
	}
	for component, v := range o.Components {
		if !components[component] {
			return errors.New("unknown logging component")
		}
		if v == "" {
			return errors.New("empty component logging level")
		}
		if _, err := level(v); err != nil {
			return err
		}
	}
	return nil
}

func New(out io.Writer, options Options) (*slog.Logger, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	minimum, _ := level(options.Level)
	overrides := make(map[string]slog.Level, len(options.Components))
	for k, v := range options.Components {
		overrides[k], _ = level(v)
	}
	// Filtering must happen after With(component), including component DEBUG
	// overrides when the default level is higher.
	opts := &slog.HandlerOptions{Level: slog.LevelDebug}
	var output slog.Handler = slog.NewTextHandler(out, opts)
	if options.Format == "json" {
		output = slog.NewJSONHandler(out, opts)
	}
	return slog.New(&handler{output: output, minimum: minimum, overrides: overrides, attrs: map[string]slog.Attr{}}), nil
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// OrDiscard keeps zero-value test fixtures usable without a global dependency.
func OrDiscard(l *slog.Logger) *slog.Logger {
	if l == nil {
		return discard
	}
	return l
}

func Component(l *slog.Logger, name string) *slog.Logger { return OrDiscard(l).With("component", name) }

// Dependency deliberately excludes upstream message text and arbitrary data.
// A raw library error/message can contain credentials even at WARN or ERROR.
func Dependency(l *slog.Logger, name string) *slog.Logger {
	return slog.New(&dependencyHandler{next: Component(l, name).Handler()})
}

type handler struct {
	output    slog.Handler
	minimum   slog.Level
	overrides map[string]slog.Level // immutable after construction
	attrs     map[string]slog.Attr  // immutable; groups are materialized by Handle
	order     []string
	groups    []string
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool {
	minimum := h.minimum
	if a, ok := h.attrs["component"]; ok {
		if override, ok := h.overrides[a.Value.String()]; ok {
			minimum = override
		}
	}
	return l >= minimum && h.output.Enabled(ctx, l)
}

// Preserve deterministic order and replace duplicate keys at the same scope.
func put(attrs map[string]slog.Attr, order *[]string, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Key == "" && a.Value.Kind() == slog.KindGroup {
		for _, child := range a.Value.Group() {
			put(attrs, order, child)
		}
		return
	}
	if old, ok := attrs[a.Key]; ok && old.Value.Kind() == slog.KindGroup && a.Value.Kind() == slog.KindGroup {
		children := map[string]slog.Attr{}
		var keys []string
		for _, child := range old.Value.Group() {
			put(children, &keys, child)
		}
		for _, child := range a.Value.Group() {
			put(children, &keys, child)
		}
		values := make([]slog.Attr, 0, len(keys))
		for _, key := range keys {
			values = append(values, children[key])
		}
		a.Value = slog.GroupValue(values...)
	}
	if _, ok := attrs[a.Key]; !ok {
		*order = append(*order, a.Key)
	}
	attrs[a.Key] = a
}

func clean(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= 1024 {
			b.WriteString("…")
			break
		}
		n++
		if unicode.IsControl(r) {
			quoted := strconv.QuoteRune(r)
			b.WriteString(quoted[1 : len(quoted)-1])
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func safeAttr(a slog.Attr) slog.Attr {
	if a.Equal(slog.Attr{}) {
		return a
	}
	a.Key = clean(a.Key)
	switch strings.ToLower(a.Key) {
	case "token", "password", "cookie", "cookies", "api_key", "authorization", "session_id", "secret_key", "private_key":
		return slog.Attr{}
	}
	a.Value = a.Value.Resolve()
	switch a.Key {
	case "bot_id", "guild_id", "channel_id", "operation_id", "playback_id":
		if a.Value.Kind() == slog.KindString && (a.Value.String() == "" || a.Value.String() == "0") {
			return slog.Attr{}
		}
	}
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(clean(a.Value.String()))
	case slog.KindAny:
		// Unknown objects and errors are not safe serialization boundaries.
		a.Value = slog.StringValue("[omitted]")
	case slog.KindGroup:
		attrs := map[string]slog.Attr{}
		var order []string
		for _, child := range a.Value.Group() {
			put(attrs, &order, safeAttr(child))
		}
		children := make([]slog.Attr, 0, len(order))
		for _, key := range order {
			children = append(children, attrs[key])
		}
		a.Value = slog.GroupValue(children...)
	}
	return a
}

func (h *handler) WithAttrs(values []slog.Attr) slog.Handler {
	n := *h
	n.attrs = maps.Clone(h.attrs)
	n.order = append([]string(nil), h.order...)
	if len(h.groups) > 0 {
		values = nest(h.groups, values)
	}
	for _, a := range values {
		put(n.attrs, &n.order, safeAttr(a))
	}
	return &n
}

func nest(groups []string, attrs []slog.Attr) []slog.Attr {
	for i := len(groups) - 1; i >= 0; i-- {
		attrs = []slog.Attr{{Key: groups[i], Value: slog.GroupValue(attrs...)}}
	}
	return attrs
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	n := *h
	n.groups = append(append([]string(nil), h.groups...), clean(name))
	return &n
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	attrs := maps.Clone(h.attrs)
	order := append([]string(nil), h.order...)
	var local []slog.Attr
	r.Attrs(func(a slog.Attr) bool { local = append(local, a); return true })
	local = nest(h.groups, local)
	for _, a := range local {
		put(attrs, &order, safeAttr(a))
	}
	n := slog.NewRecord(r.Time, r.Level, clean(r.Message), r.PC)
	for _, key := range order {
		// These keys belong to the output Handler. Do not create ambiguous JSON
		// by accepting application attributes with the same top-level names.
		if key == slog.TimeKey || key == slog.LevelKey || key == slog.MessageKey || key == "trace_id" || key == "span_id" || key == "trace_sampled" {
			continue
		}
		n.AddAttrs(attrs[key])
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		n.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()), slog.Bool("trace_sampled", sc.IsSampled()))
	}
	return h.output.Handle(ctx, n)
}

type dependencyHandler struct{ next slog.Handler }

func (h *dependencyHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}
func (h *dependencyHandler) WithAttrs(a []slog.Attr) slog.Handler {
	// Attributes attached by the application are already on next. Reject all
	// library With fields rather than accidentally preserving a secret string.
	return h
}
func (h *dependencyHandler) WithGroup(string) slog.Handler { return h }
func (h *dependencyHandler) Handle(ctx context.Context, r slog.Record) error {
	n := slog.NewRecord(r.Time, r.Level, "dependency event", r.PC)
	n.AddAttrs(slog.String("event", "dependency.log"))
	r.Attrs(func(a slog.Attr) bool {
		if (a.Key == "code" || a.Key == "status" || a.Key == "attempt") && a.Value.Kind() == slog.KindInt64 {
			n.AddAttrs(a)
		}
		return true
	})
	return h.next.Handle(ctx, n)
}
