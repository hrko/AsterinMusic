package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

var cleanupApply bool

func sameArtifactPath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

var generationFile = regexp.MustCompile(`^(?:asterinmusic-voice-([0-9]+)(?:\.exe)?|bot-([0-9]+)\.log)$`)

type artifact struct {
	path       string
	generation int64
	info       os.FileInfo
}

func cleanupPlan(root, executable, log string) ([]artifact, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	var remove []artifact
	for _, group := range []struct {
		dir    string
		keep   int
		prefix string
	}{{"bin", 3, "asterinmusic-voice-"}, {filepath.Join(".dev", "logs"), 10, "bot-"}} {
		dir := filepath.Join(root, group.dir)
		resolved, err := filepath.EvalSymlinks(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !sameArtifactPath(resolved, dir) {
			return nil, fmt.Errorf("refusing redirected artifact directory: %s", group.dir)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		var files []artifact
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), group.prefix) {
				continue
			}
			match := generationFile.FindStringSubmatch(entry.Name())
			if match == nil {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			stamp := match[1]
			if stamp == "" {
				stamp = match[2]
			}
			n, err := strconv.ParseInt(stamp, 10, 64)
			if err != nil {
				continue
			}
			files = append(files, artifact{filepath.Join(dir, entry.Name()), n, info})
		}
		sort.Slice(files, func(i, j int) bool { return files[i].generation > files[j].generation })
		for i, file := range files {
			if i < group.keep || sameArtifactPath(file.path, executable) || sameArtifactPath(file.path, log) {
				continue
			}
			remove = append(remove, file)
		}
	}
	return remove, nil
}

func cleanupArtifacts(root, executable string) error {
	log, err := os.ReadFile(filepath.Join(root, ".dev", "run", "latest-log"))
	if executable != "" && (err != nil || len(strings.TrimSpace(string(log))) == 0) {
		return fmt.Errorf("cannot identify active log; refusing cleanup")
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	plan, err := cleanupPlan(root, executable, strings.TrimSpace(string(log)))
	if err != nil {
		return err
	}
	for _, file := range plan {
		if cleanupApply {
			info, err := os.Lstat(file.path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || !os.SameFile(info, file.info) {
				return fmt.Errorf("artifact changed during cleanup: %s", file.path)
			}
			if err := os.Remove(file.path); err != nil {
				return err
			}
		}
		fmt.Println(file.path)
	}
	fmt.Printf("artifacts=%d applied=%t (keep newest 3 binaries and 10 logs, plus active files)\n", len(plan), cleanupApply)
	return nil
}
