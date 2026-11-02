package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

type rated struct {
	pair
	Score float64
}
type comparison struct {
	ID, Group, Channel                            string
	Volume                                        int
	Libopus, Scalar, SIMD, DeltaSIMD, DeltaScalar float64
}
type aggregate struct {
	Group, Channel                                                   string
	Volume, Count                                                    int
	MeanLibopus, MeanScalar, MeanSIMD, MeanDelta, MinDelta, P10Delta float64
	WorstID                                                          string
}

func summarize() error {
	b, e := os.ReadFile(filepath.Join(root, "pairs.json"))
	if e != nil {
		return e
	}
	var pairs []pair
	if e = json.Unmarshal(b, &pairs); e != nil {
		return e
	}
	f, e := os.Open(filepath.Join(root, "scores.csv"))
	if e != nil {
		return e
	}
	defer f.Close()
	rows, e := csv.NewReader(f).ReadAll()
	if e != nil {
		return e
	}
	scores := map[string]float64{}
	for i, row := range rows {
		if i == 0 {
			continue
		}
		if len(row) < 3 {
			return fmt.Errorf("invalid score row %d", i)
		}
		s, e := strconv.ParseFloat(row[2], 64)
		if e != nil || math.IsNaN(s) || math.IsInf(s, 0) {
			return fmt.Errorf("invalid score %v", row)
		}
		scores[row[0]+"|"+row[1]] = s
	}
	var ratings []rated
	triples := map[string]*comparison{}
	counts := map[string]int{}
	for _, p := range pairs {
		s, ok := scores[p.Reference+"|"+p.Degraded]
		if !ok {
			return fmt.Errorf("missing score %s", p.Degraded)
		}
		ratings = append(ratings, rated{p, s})
		if p.Codec == "identity" || p.Codec == "anchor-lowpass" {
			continue
		}
		key := fmt.Sprintf("%s/%d/%s", p.ID, p.Volume, p.Channel)
		if triples[key] == nil {
			triples[key] = &comparison{ID: p.ID, Group: p.Group, Channel: p.Channel, Volume: p.Volume}
		}
		t := triples[key]
		switch p.Codec {
		case "libopus":
			t.Libopus = s
		case "gopus-scalar":
			t.Scalar = s
		case "gopus-simd":
			t.SIMD = s
		default:
			return fmt.Errorf("unexpected codec")
		}
		counts[key]++
	}
	var comparisons []comparison
	groups := map[string][]comparison{}
	for key, t := range triples {
		if counts[key] != 3 {
			return fmt.Errorf("incomplete comparison %s", key)
		}
		t.DeltaSIMD = t.SIMD - t.Libopus
		t.DeltaScalar = t.Scalar - t.Libopus
		comparisons = append(comparisons, *t)
		for _, group := range []string{t.Group, "all"} {
			key = fmt.Sprintf("%s/%d/%s", group, t.Volume, t.Channel)
			groups[key] = append(groups[key], *t)
		}
	}
	sort.Slice(comparisons, func(i, j int) bool { return comparisons[i].DeltaSIMD < comparisons[j].DeltaSIMD })
	var aggregates []aggregate
	for _, g := range groups {
		a := aggregate{Group: g[0].Group, Volume: g[0].Volume, Channel: g[0].Channel, Count: len(g), MinDelta: math.Inf(1)}
		same := true
		var ds []float64
		for _, x := range g {
			if x.Group != a.Group {
				same = false
			}
			a.MeanLibopus += x.Libopus
			a.MeanScalar += x.Scalar
			a.MeanSIMD += x.SIMD
			a.MeanDelta += x.DeltaSIMD
			ds = append(ds, x.DeltaSIMD)
			if x.DeltaSIMD < a.MinDelta {
				a.MinDelta = x.DeltaSIMD
				a.WorstID = x.ID
			}
		}
		if !same {
			a.Group = "all"
		}
		n := float64(len(g))
		a.MeanLibopus /= n
		a.MeanScalar /= n
		a.MeanSIMD /= n
		a.MeanDelta /= n
		sort.Float64s(ds)
		a.P10Delta = ds[int(float64(len(ds)-1)*0.1)]
		aggregates = append(aggregates, a)
	}
	sort.Slice(aggregates, func(i, j int) bool {
		return fmt.Sprint(aggregates[i].Group, aggregates[i].Volume, aggregates[i].Channel) < fmt.Sprint(aggregates[j].Group, aggregates[j].Volume, aggregates[j].Channel)
	})
	if e = writeJSON(filepath.Join(root, "ratings.json"), ratings); e != nil {
		return e
	}
	if e = writeJSON(filepath.Join(root, "comparisons.json"), comparisons); e != nil {
		return e
	}
	if e = writeJSON(filepath.Join(root, "summary.json"), aggregates); e != nil {
		return e
	}
	fmt.Printf("Scored %d pairs; %d matched codec comparisons\n", len(ratings), len(comparisons))
	for _, a := range aggregates {
		if a.Channel == "mix" {
			fmt.Printf("%s vol=%d n=%d lib=%.4f scalar=%.4f simd=%.4f delta=%+.4f worst=%+.4f (%s)\n", a.Group, a.Volume, a.Count, a.MeanLibopus, a.MeanScalar, a.MeanSIMD, a.MeanDelta, a.MinDelta, a.WorstID)
		}
	}
	return nil
}
