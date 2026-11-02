package config

import (
	"errors"
	"path/filepath"
	"strings"
)

// ExtractOptions accepts global file flags before or after a subcommand.
func ExtractOptions(args []string) (Options, []string, error) {
	var o Options
	var rest []string
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		key, value, equals := strings.Cut(args[i], "=")
		if key != "--config" && key != "--env-file" {
			rest = append(rest, args[i])
			continue
		}
		if seen[key] {
			return o, nil, errors.New("duplicate configuration flag")
		}
		seen[key] = true
		if !equals {
			i++
			if i == len(args) {
				return o, nil, errors.New("configuration flag requires a path")
			}
			value = args[i]
		}
		if value == "" || strings.HasPrefix(value, "--") {
			return o, nil, errors.New("configuration flag requires a path")
		}
		path, err := filepath.Abs(value)
		if err != nil {
			return o, nil, errors.New("invalid configuration path")
		}
		if key == "--config" {
			o.ConfigFile = path
		} else {
			o.EnvFile = path
		}
	}
	return o, rest, nil
}
func (o Options) Args() []string {
	path := o.ConfigFile
	if path == "" {
		path, _ = filepath.Abs("rainmusic.yaml")
	}
	args := []string{"--config", path}
	if o.EnvFile != "" {
		args = append(args, "--env-file", o.EnvFile)
	}
	return args
}
