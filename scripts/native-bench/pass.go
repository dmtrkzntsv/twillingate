package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ---- pass: step 4, the daily pass in Go against the old one on DuckDB ----
//
// Each run is one process of the main module's measure test TestMeasurePass
// (internal/archive/measure_pass_test.go), built with `go test -c -tags
// measure -o archive.test ./internal/archive` and passed as -bin: one month
// (a step 3f month file, cut into one log a day by the test's first run)
// through the dump, the merge and the derived files, on one side, with or
// without GOMEMLIMIT. A cell is a first run and -passes more, each a fresh
// process, so each peak is its own.

type passPhaseOut struct {
	Name      string  `json:"name"`
	WallMS    float64 `json:"wall_ms"`
	SelfPeak  int     `json:"self_peak_kb"`
	ChildPeak int     `json:"child_peak_kb"`
	GCs       uint64  `json:"gcs"`
	GCCPUMS   float64 `json:"gc_cpu_ms"`
	CPUMS     float64 `json:"cpu_ms"`
}

type passRun struct {
	Side       string         `json:"side"`
	Case       string         `json:"case"`
	Logs       string         `json:"logs"`
	GoMemLimit string         `json:"gomemlimit"`
	Inputs     int            `json:"inputs"`
	Rows       int64          `json:"rows"`
	Columns    int            `json:"columns"`
	RowGroups  int            `json:"row_groups"`
	BaselineKB int            `json:"baseline_kb"`
	Phases     []passPhaseOut `json:"phases"`
	MonthBytes int64          `json:"month_bytes"`
	Load       string         `json:"load_at_start"`
	HeapPeak   uint64         `json:"heap_peak_mib"`
	Failed     string         `json:"failed,omitempty"`
}

type passCellOut struct {
	Volume, Case, Logs, Side, Limit string
	Runs                            []passRun
	GateLoad                        float64
	Quiet                           bool
}

func (r passRun) wall() float64 {
	var w float64
	for _, p := range r.Phases {
		w += p.WallMS
	}
	return w
}

// peak is the run's largest phase peak, the Go process plus the engine
// child.
func (r passRun) peak() int {
	m := 0
	for _, p := range r.Phases {
		m = max(m, p.SelfPeak+p.ChildPeak)
	}
	return m
}

var passCases = []struct{ cs, logs string }{
	{"pass", "plain"}, {"dump", "plain"}, {"leftover", "plain"}, {"pass", "wide"},
}

func passCmd(args []string) error {
	fs := flag.NewFlagSet("pass", flag.ContinueOnError)
	bin := fs.String("bin", "", "the measure test binary (go test -c -tags measure ./internal/archive)")
	pkg := fs.String("pkg", "", "internal/archive of the checkout the binary was built from (its working directory)")
	src := fs.String("src", "", "the step 3f root, whose <volume>/raw/1/2026-09.parquet is the month")
	out := fs.String("out", "RESULTS.md", "Markdown file the tables are appended to")
	only := fs.String("volumes", "100k,1m", "volumes to run")
	passes := fs.Int("passes", 3, "warm runs per cell after the first")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || *bin == "" || *pkg == "" || *src == "" {
		return errors.New("usage: native-bench pass -bin archive.test -pkg <checkout>/internal/archive -src <3f root> [-out RESULTS.md] <root>")
	}
	root := fs.Arg(0)
	logf := func(format string, a ...any) {
		fmt.Printf("%s "+format+"\n", append([]any{time.Now().Format("15:04:05")}, a...)...)
	}
	start := time.Now()
	var all []passCellOut
	for _, v := range volumes {
		if !slices.Contains(strings.Split(*only, ","), v.name) {
			continue
		}
		dir := filepath.Join(root, v.name)
		cellsDir := filepath.Join(dir, "cells")
		if err := os.MkdirAll(cellsDir, 0o750); err != nil {
			return err
		}
		month := filepath.Join(*src, v.name, "raw", "1", "2026-09.parquet")
		env := []string{"MEASURE_PASS_MONTH=" + month, "MEASURE_PASS_DIR=" + dir}
		// The logs and the day files, written by the test's first run.
		if _, err := os.Stat(filepath.Join(dir, "days-wide")); err != nil {
			gen := osexec.Command(*bin, "-test.run", "^TestMeasurePass$", "-test.v", "-test.timeout", "2h")
			gen.Dir, gen.Stdout, gen.Stderr = *pkg, os.Stdout, os.Stderr
			gen.Env = append(os.Environ(), env...)
			if err := gen.Run(); err != nil {
				return fmt.Errorf("%s: preparing: %w", v.name, err)
			}
		}
		for _, c := range passCases {
			for _, side := range []string{"go", "duckdb"} {
				for _, limit := range []string{"", "128MiB"} {
					name := fmt.Sprintf("%s-%s-%s-%s.json", c.cs, c.logs, side, map[string]string{"": "nolimit"}[limit]+limit)
					path := filepath.Join(cellsDir, name)
					var cell passCellOut
					if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &cell) == nil && len(cell.Runs) > 0 {
						logf("%s %s: kept from an earlier run", v.name, name)
						all = append(all, cell)
						continue
					}
					load, quiet := waitQuiet(quietLoad, quietPoll, dashQuietLimit, readLoad)
					if !quiet {
						logf("gate: gave up at load %.2f", load)
					}
					cell = passCellOut{Volume: v.name, Case: c.cs, Logs: c.logs, Side: side, Limit: limit, GateLoad: load, Quiet: quiet}
					for run := 0; run <= *passes; run++ {
						r, err := passOnce(*bin, *pkg, env, c.cs, c.logs, side, limit, path+".tmp")
						if err != nil {
							return fmt.Errorf("%s %s: %w", v.name, name, err)
						}
						cell.Runs = append(cell.Runs, r)
						if r.Failed != "" {
							logf("%s %s: failed: %s", v.name, name, r.Failed)
							break // a failure is the cell's result
						}
						logf("%s %s run %d: %.0f ms, peak %d MB, load %s", v.name, name, run, r.wall(), r.peak()/1024, r.Load)
					}
					b, err := json.Marshal(cell)
					if err != nil {
						return err
					}
					if err := os.WriteFile(path, b, 0o644); err != nil {
						return err
					}
					all = append(all, cell)
				}
			}
		}
	}
	b, err := json.MarshalIndent(all, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "pass.json"), b, 0o644); err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(passMD(all, start, *passes))
	logf("pass: done in %s", time.Since(start).Round(time.Second))
	return err
}

// passOnce runs one process; a run the test fails (DuckDB out of memory)
// comes back with Failed set to the error line.
func passOnce(bin, pkg string, env []string, cs, logs, side, limit, out string) (passRun, error) {
	os.Remove(out)
	cmd := osexec.Command(bin, "-test.run", "^TestMeasurePass$", "-test.v", "-test.timeout", "2h")
	cmd.Dir = pkg
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, "GOMAXPROCS=2", "MEASURE_PASS_SIDE="+side, "MEASURE_PASS_CASE="+cs, "MEASURE_PASS_LOGS="+logs,
		"MEASURE_PASS_OUT="+out)
	if limit != "" {
		cmd.Env = append(cmd.Env, "GOMEMLIMIT="+limit)
	}
	outb, err := cmd.CombinedOutput()
	if err != nil {
		for _, l := range strings.Split(string(outb), "\n") {
			if strings.Contains(l, "measure_pass_test.go") {
				return passRun{Side: side, Case: cs, Logs: logs, GoMemLimit: limit, Failed: strings.TrimSpace(l)}, nil
			}
		}
		return passRun{}, fmt.Errorf("%w\n%s", err, outb)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return passRun{}, err
	}
	defer os.Remove(out)
	var r passRun
	return r, json.Unmarshal(b, &r)
}

func passMD(all []passCellOut, start time.Time, passes int) string {
	var sb strings.Builder
	var loads []float64
	unquiet := 0
	for _, c := range all {
		loads = append(loads, c.GateLoad)
		if !c.Quiet {
			unquiet++
		}
	}
	lo, hi := minMax(loads)
	fmt.Fprintf(&sb, "\n## Step 4: the daily pass in Go\n\n"+
		"Started %s. Every run is a fresh process (`native-bench pass`, which runs the main module's `TestMeasurePass`, "+
		"`internal/archive/measure_pass_test.go`) at GOMAXPROCS=2, once (\"first\") and %d times more (\"best\" = the "+
		"fastest of those). The month is step 3f's 2026-09 file, written as one NDJSON log a day as ingest writes them. "+
		"Cases: **pass** is the merge day's pass (the last day's log dumped, the month merged from it and the 29 other "+
		"days' files, then its sessions, identities and aliases derived); **dump** is the 30 logs dumped one after "+
		"another; **leftover** is the merge and the derive with a crashed merge's month file (the DuckDB-written source) "+
		"next to the Go day files; **wide** logs spread each of the 8 product attributes over 250 names (2,000 keys). "+
		"Go runs `dumpLog`, `mergeFiles` and `Archive.derive` in the process; DuckDB runs the old statements in an engine "+
		"child of 2 threads and 512 MB (the server's defaults at GOMAXPROCS=2), spawned before the clock. Peak = VmHWM, "+
		"reset at each phase (clear_refs), so a phase's peak includes what the one before left resident; for DuckDB the "+
		"Go process plus the child. The cell's peak is the largest over its runs and phases. GC = collections and the GC's "+
		"CPU time in the best run, over the phases. Before every cell the bench waits until the 1-minute load average is at "+
		"most %.1f.\n",
		start.UTC().Format(time.RFC3339), passes, quietLoad)
	for _, v := range volumes {
		has := false
		for _, c := range all {
			has = has || c.Volume == v.name
		}
		if !has {
			continue
		}
		fmt.Fprintf(&sb, "\n### Step 4 pass, volume %s (%d events a month)\n\n", v.name, v.perMonth)
		sb.WriteString("| Case | Side | GOMEMLIMIT | First | Best | Best by phase (dump / merge / derive) | Peak | Peak by phase | Baseline | GCs / GC CPU | Month file | Load at start |\n")
		sb.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for _, c := range all {
			if c.Volume != v.name {
				continue
			}
			limit := c.Limit
			if limit == "" {
				limit = "none"
			}
			load := fmt.Sprintf("%.2f", c.GateLoad)
			if !c.Quiet {
				load += " (unquiet)"
			}
			kase := c.Case
			if c.Logs == "wide" {
				kase += " (wide)"
			}
			if f := c.Runs[len(c.Runs)-1].Failed; f != "" {
				fmt.Fprintf(&sb, "| %s | %s | %s | failed: %s | | | | | | | | %s |\n", kase, c.Side, limit, f, load)
				continue
			}
			best := c.Runs[0]
			for _, r := range c.Runs[1:] {
				if r.wall() < best.wall() {
					best = r
				}
			}
			peak := 0
			byPhase := map[string]int{}
			for _, r := range c.Runs {
				peak = max(peak, r.peak())
				for _, p := range r.Phases {
					byPhase[p.Name] = max(byPhase[p.Name], p.SelfPeak+p.ChildPeak)
				}
			}
			var walls, peaks []string
			var gcs uint64
			var gcCPU float64
			for _, p := range best.Phases {
				walls = append(walls, p.Name+" "+dashMS(p.WallMS))
				peaks = append(peaks, p.Name+" "+mb(byPhase[p.Name]))
				gcs += p.GCs
				gcCPU += p.GCCPUMS
			}
			fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %d / %s | %d MB, %d cols, %d groups | %s |\n",
				kase, c.Side, limit, dashMS(c.Runs[0].wall()), dashMS(best.wall()), strings.Join(walls, ", "), mb(peak),
				strings.Join(peaks, ", "), mb(best.BaselineKB), gcs, dashMS(gcCPU), best.MonthBytes>>20, best.Columns, best.RowGroups, load)
		}
	}
	fmt.Fprintf(&sb, "\nLoad average (1 min) at cell start: %.2f to %.2f; cells that started above %.1f (the gate gave up): %d of %d.\n",
		lo, hi, quietLoad, unquiet, len(all))
	return sb.String()
}
