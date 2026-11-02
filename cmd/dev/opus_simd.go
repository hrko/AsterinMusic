package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Compile both variants first, then alternate their execution order to reduce
// warm-machine/order bias. PGO is explicitly off in both builds.
func compareSIMD(nativeEnv []string, binDir, ext string) error {
	if runtime.GOARCH != "amd64" {
		return fmt.Errorf("this comparison profile requires amd64 v3")
	}
	root, err := filepath.Abs(filepath.Join(".dev", "opusbench", "simd-"+runtime.GOOS))
	if err != nil {
		return err
	}
	if err = os.MkdirAll(root, 0755); err != nil {
		return err
	}
	for _, mode := range []string{"nosimd", "simd"} {
		env := append(append([]string{}, nativeEnv...), "GOEXPERIMENT="+mode, "GOAMD64=v3", "GOMAXPROCS=1")
		if err = command(env, "go", "-C", "experiments/opusbench", "test", "-count=1", "-v", "./..."); err != nil {
			return err
		}
		binary := filepath.Join(binDir, "opusbench-"+mode+ext)
		if err = command(env, "go", "-C", "experiments/opusbench", "build", "-pgo=off", "-o", binary, "."); err != nil {
			return err
		}
		// Record the selected gopus CELT source files as build-path evidence.
		cmd := exec.Command("go", "-C", "experiments/opusbench", "list", "-json", "github.com/thesyncim/gopus/internal/celt")
		cmd.Env = append(os.Environ(), env...)
		data, e := cmd.Output()
		if e != nil {
			return e
		}
		if err = os.WriteFile(filepath.Join(root, mode+"-celt-build.json"), data, 0644); err != nil {
			return err
		}
		// Independently prove that the SIMD candidate can be built without cgo.
		if mode == "simd" {
			pureEnv := append(append([]string{}, env...), "CGO_ENABLED=0")
			pureBinary := filepath.Join(binDir, "opusbench-simd-pure"+ext)
			if err = command(pureEnv, "go", "-C", "experiments/opusbench", "build", "-pgo=off", "-o", pureBinary, "."); err != nil {
				return err
			}
			if err = command(pureEnv, pureBinary, "-pure", "-codecs", "gopus", "-no-wav", "-out", filepath.Join(root, "pure")); err != nil {
				return err
			}
		}
	}
	inputs := []string{""}
	if input := os.Getenv("ASTERINMUSIC_OPUS_PCM"); input != "" {
		input, err = filepath.Abs(input)
		if err != nil {
			return err
		}
		inputs = []string{input}
	}
	for _, input := range inputs {
		label := "synthetic"
		if input != "" {
			label = filepath.Base(input)
		}
		for run := 1; run <= 3; run++ {
			modes := []string{"nosimd", "simd"}
			if run%2 == 0 {
				modes = []string{"simd", "nosimd"}
			}
			for _, mode := range modes {
				output := filepath.Join(root, label, fmt.Sprintf("%s-%d", mode, run))
				args := []string{"-codecs", "libopus,gopus", "-no-wav", "-out", output}
				if input != "" {
					args = append(args, "-pcm", input)
				}
				if err = command(append(append([]string{}, nativeEnv...), "GOMAXPROCS=1"), filepath.Join(binDir, "opusbench-"+mode+ext), args...); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
