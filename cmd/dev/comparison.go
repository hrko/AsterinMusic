package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// comparison runs optional native-library experiments, never Bot lifecycle tasks.
func comparison(action string) error {
	out, err := exec.Command("mise", "where", "ffmpeg").Output()
	if err != nil {
		return fmt.Errorf("locate opus: %w", err)
	}
	opusRoot := strings.TrimSpace(string(out))
	if runtime.GOOS == "windows" {
		opusRoot = filepath.Join(opusRoot, "Library")
	}
	root := opusRoot
	if action == "dave-interop" {
		out, err = exec.Command("mise", "-C", "experiments", "where", "github:discord/libdave").Output()
		if err != nil {
			return fmt.Errorf("locate optional libdave (mise -C experiments install): %w", err)
		}
		root = strings.TrimSpace(string(out))
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	dir, err := filepath.Abs("bin")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	helper := filepath.Join(dir, "asterinmusic-pkgconfig"+ext)
	if err = command([]string{"CGO_ENABLED=0"}, "go", "build", "-o", helper, "./cmd/pkgconfig"); err != nil {
		return err
	}
	// cgo caches pkg-config output. Include its implementation and root in the
	// compile flags so tool/path changes invalidate that cache.
	helperBytes, err := os.ReadFile(helper)
	if err != nil {
		return err
	}
	signature := sha256.Sum256(append(helperBytes, []byte(root+opusRoot)...))
	env := []string{"CGO_ENABLED=1", "CC=zig cc", "CXX=zig c++", "PKG_CONFIG=" + helper, "ASTERINMUSIC_DAVE_ROOT=" + root, "PATH=" + filepath.Join(root, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"), "LD_LIBRARY_PATH=" + filepath.Join(root, "lib") + string(os.PathListSeparator) + os.Getenv("LD_LIBRARY_PATH")}
	env = append(env, fmt.Sprintf("CGO_CFLAGS=-O2 -DASTERINMUSIC_PKGCONFIG_%x=1", signature[:8]))
	env = append(env, "ASTERINMUSIC_OPUS_ROOT="+opusRoot,
		"PATH="+filepath.Join(opusRoot, "bin")+string(os.PathListSeparator)+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"LD_LIBRARY_PATH="+filepath.Join(opusRoot, "lib")+string(os.PathListSeparator)+filepath.Join(root, "lib")+string(os.PathListSeparator)+os.Getenv("LD_LIBRARY_PATH"))
	if action == "opus-simd-compare" {
		return compareSIMD(env, dir, ext)
	}
	if action == "dave-interop" {
		return command(env, "go", "-C", "experiments/daveinterop", "test", "-tags", "daveinterop", "./...", "-count=1", "-v")
	}
	if action == "opus-compare" {
		if err := command(env, "go", "-C", "experiments/opusbench", "test", "-v", "./..."); err != nil {
			return err
		}
		output, err := filepath.Abs(filepath.Join(".dev", "opusbench", runtime.GOOS))
		if err != nil {
			return err
		}
		args := []string{"-C", "experiments/opusbench", "run", ".", "-out", output}
		if input := os.Getenv("ASTERINMUSIC_OPUS_PCM"); input != "" {
			input, err = filepath.Abs(input)
			if err != nil {
				return err
			}
			args = []string{"-C", "experiments/opusbench", "run", ".", "-out", filepath.Join(output, filepath.Base(input)), "-pcm", input}
		}
		return command(env, "go", args...)
	}
	return fmt.Errorf("unknown comparison task: %s", action)
}
