package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

func score() error {
	binary := os.Getenv("ASTERINMUSIC_VISQOL")
	if binary == "" {
		if runtime.GOOS == "windows" {
			return fmt.Errorf("run quality-score in WSL/Linux, or set ASTERINMUSIC_VISQOL to a native Windows ViSQOL executable")
		}
		binary = ".dev/visqol/source/bazel-bin/visqol"
	}
	b, e := os.ReadFile(filepath.Join(root, "pairs.json"))
	if e != nil {
		return e
	}
	var pairs []pair
	if e = json.Unmarshal(b, &pairs); e != nil {
		return e
	}
	var ids []string
	groups := map[string][]pair{}
	for _, p := range pairs {
		if _, ok := groups[p.ID]; !ok {
			ids = append(ids, p.ID)
		}
		groups[p.ID] = append(groups[p.ID], p)
	}
	base := filepath.Join(root, "score-shards")
	if e = os.MkdirAll(base, 0755); e != nil {
		return e
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobs := make(chan string, len(ids))
	for _, id := range ids {
		jobs <- id
	}
	close(jobs)
	errs := make(chan error, len(ids))
	var wg sync.WaitGroup
	for w := 0; w < min(12, runtime.NumCPU()); w++ {
		wg.Go(func() {
			for id := range jobs {
				if ctx.Err() != nil {
					return
				}
				dir := filepath.Join(base, id)
				if e := os.MkdirAll(dir, 0755); e != nil {
					errs <- e
					cancel()
					return
				}
				input := filepath.Join(dir, "batch.csv")
				output := filepath.Join(dir, "scores.csv")
				f, e := os.Create(input)
				if e != nil {
					errs <- e
					cancel()
					return
				}
				cw := csv.NewWriter(f)
				cw.Write([]string{"reference", "degraded"})
				for _, p := range groups[id] {
					cw.Write([]string{p.Reference, p.Degraded})
				}
				cw.Flush()
				f.Close()
				if e = cw.Error(); e != nil {
					errs <- e
					cancel()
					return
				}
				if e = os.Remove(output); e != nil && !os.IsNotExist(e) {
					errs <- e
					cancel()
					return
				}
				log, e := os.Create(filepath.Join(dir, "visqol.log"))
				if e != nil {
					errs <- e
					cancel()
					return
				}
				cmd := exec.CommandContext(ctx, binary, "--batch_input_csv", input, "--results_csv", output, "--similarity_to_quality_model", ".dev/visqol/source/model/libsvm_nu_svr_model.txt")
				cmd.Env = append(os.Environ(), "OMP_NUM_THREADS=1", "OPENBLAS_NUM_THREADS=1", "MKL_NUM_THREADS=1")
				cmd.Stdout = log
				cmd.Stderr = log
				e = cmd.Run()
				log.Close()
				if e != nil {
					errs <- fmt.Errorf("%s: %w (see %s)", id, e, dir)
					cancel()
					return
				}
				fmt.Println("scored", id)
			}
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		return e
	}
	all, e := os.Create(filepath.Join(root, "scores.csv"))
	if e != nil {
		return e
	}
	defer all.Close()
	cw := csv.NewWriter(all)
	for i, id := range ids {
		f, e := os.Open(filepath.Join(base, id, "scores.csv"))
		if e != nil {
			return e
		}
		rows, e := csv.NewReader(f).ReadAll()
		f.Close()
		if e != nil {
			return e
		}
		if len(rows) != len(groups[id])+1 {
			return fmt.Errorf("incomplete rows for %s: %d", id, len(rows))
		}
		if i == 0 {
			cw.Write(rows[0])
		}
		for _, row := range rows[1:] {
			cw.Write(row)
		}
	}
	cw.Flush()
	return cw.Error()
}
