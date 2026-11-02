//go:build !windows

package source

import "os"

func secureAuthDir(path string) error { return os.Chmod(path, 0700) }
