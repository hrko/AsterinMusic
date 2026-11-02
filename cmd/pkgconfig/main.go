// pkgconfig supplies the pinned libdave paths to cgo without platform scripts.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	root := os.Getenv("ASTERINMUSIC_DAVE_ROOT")
	lib := "dave"
	include := "dave"
	if runtime.GOOS == "windows" {
		lib = "libdave"
	}
	for _, arg := range os.Args[1:] {
		if arg == "opus" {
			root = os.Getenv("ASTERINMUSIC_OPUS_ROOT")
			lib = "opus"
			include = "opus"
		}
	}
	if root == "" {
		os.Exit(1)
	}
	quote := func(s string) string { return `"` + filepath.ToSlash(s) + `"` }
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "--cflags") {
			fmt.Print("-I" + quote(filepath.Join(root, "include", include)) + " ")
		}
		if strings.HasPrefix(arg, "--libs") {
			fmt.Print("-L" + quote(filepath.Join(root, "lib")) + " -l" + lib)
		}
	}
}
