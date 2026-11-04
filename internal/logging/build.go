package logging

import (
	"runtime"
	"runtime/debug"
)

// BuildAttributes deliberately excludes build paths, settings and timestamps.
func BuildAttributes() []any {
	attrs := []any{"go_version", runtime.Version()}
	if info, ok := debug.ReadBuildInfo(); ok {
		attrs = append(attrs, "version", info.Main.Version)
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				attrs = append(attrs, "revision", setting.Value)
			}
		}
	} else {
		attrs = append(attrs, "version", "unknown")
	}
	return attrs
}
