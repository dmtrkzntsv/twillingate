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

// ---- dashboards: step 3f, the built-in dashboards on the native engine ----
//
// Each cell is one run of the main module's measure test
// TestMeasureSystemDashboard (internal/app/measure_system_test.go), built
// with `go test -c -tags measure -o app.test ./internal/app` and passed as
// -bin: one dashboard over one range on one engine, a fresh process, so its
// peak RSS is its own. The test generates the files on its first run in a
// volume's directory.

type dashCell struct {
	Engine    string         `json:"engine"`
	Dashboard string         `json:"dashboard"`
	Days      int            `json:"days"`
	From      string         `json:"from"`
	To        string         `json:"to"`
	Widgets   int            `json:"widgets"`
	FirstMS   float64        `json:"first_ms"`
	WarmMS    []float64      `json:"warm_ms"`
	BestMS    float64        `json:"best_ms"`
	SelfPeak  int            `json:"self_peak_kb"`
	ChildPeak int            `json:"child_peak_kb"`
	Children  int            `json:"children"`
	IdleRSS   int            `json:"idle_rss_kb"`
	First     map[string]int `json:"first_widget_ms"`
	Best      map[string]int `json:"best_widget_ms"`
	Rows      map[string]int `json:"rows"`
	Load      string         `json:"load_at_start"`
	// Set by the bench.
	Volume   string  `json:"volume"`
	GateLoad float64 `json:"gate_load"`
	Quiet    bool    `json:"quiet"`
}

// dashboards3f are the system dashboards (their directory and title) and
// the ranges they run over: every one at 7, 30 and 90 days, Views and
// Product over the year too.
var dashboards3f = []struct {
	dir, title string
	year       bool
}{
	{"views", "Views", true}, {"product", "Product", true}, {"users", "Users", false}, {"groups", "Groups", false},
	{"retention", "Retention", false}, {"vitals", "Web Vitals", false}, {"measures", "Measures", false},
}

const (
	dashQuietLimit = 5 * time.Minute
)

func dashboardsCmd(args []string) error {
	fs := flag.NewFlagSet("dashboards", flag.ContinueOnError)
	bin := fs.String("bin", "", "the measure test binary (go test -c -tags measure ./internal/app)")
	pkg := fs.String("pkg", "", "internal/app of the checkout the binary was built from (its working directory)")
	out := fs.String("out", "RESULTS.md", "Markdown file the tables are appended to")
	only := fs.String("volumes", "100k,1m", "volumes to run")
	passes := fs.Int("passes", 3, "warm runs per cell after the first")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || *bin == "" || *pkg == "" {
		return errors.New("usage: native-bench dashboards -bin app.test -pkg <checkout>/internal/app [-out RESULTS.md] [-volumes 100k,1m] <root>")
	}
	root := fs.Arg(0)
	logf := func(format string, a ...any) {
		fmt.Printf("%s "+format+"\n", append([]any{time.Now().Format("15:04:05")}, a...)...)
	}
	start := time.Now()
	var all []dashCell
	for _, v := range volumes {
		if !slices.Contains(strings.Split(*only, ","), v.name) {
			continue
		}
		dir := filepath.Join(root, v.name)
		cellsDir := filepath.Join(dir, "cells")
		if err := os.MkdirAll(cellsDir, 0o750); err != nil {
			return err
		}
		// The files: generated (and derived) by the test's first run.
		gen := osexec.Command(*bin, "-test.run", "^TestMeasureSystemDashboard$", "-test.v", "-test.timeout", "2h")
		gen.Dir, gen.Stdout, gen.Stderr = *pkg, os.Stdout, os.Stderr
		gen.Env = append(os.Environ(), "MEASURE_DIR="+dir, fmt.Sprintf("MEASURE_MONTH_EVENTS=%d", v.perMonth))
		if err := gen.Run(); err != nil {
			return fmt.Errorf("%s: generating: %w", v.name, err)
		}
		for _, days := range []int{7, 30, 90, 365} {
			for _, d := range dashboards3f {
				if days == 365 && !d.year {
					continue
				}
				for _, eng := range []string{"native", "duckdb"} {
					path := filepath.Join(cellsDir, fmt.Sprintf("%s-%s-%d.json", eng, d.dir, days))
					var c dashCell
					if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &c) == nil && c.Volume != "" {
						logf("%s %s %s %dd: kept from an earlier run", v.name, eng, d.dir, days)
						all = append(all, c)
						continue
					}
					load, quiet := waitQuiet(quietLoad, quietPoll, dashQuietLimit, readLoad)
					if !quiet {
						logf("gate: gave up at load %.2f", load)
					}
					cmd := osexec.Command(*bin, "-test.run", "^TestMeasureSystemDashboard$", "-test.v", "-test.timeout", "30m")
					cmd.Dir = *pkg
					cmd.Env = append(os.Environ(), "GOMAXPROCS=2", "MEASURE_DIR="+dir,
						fmt.Sprintf("MEASURE_MONTH_EVENTS=%d", v.perMonth), fmt.Sprintf("MEASURE_PASSES=%d", *passes),
						fmt.Sprintf("MEASURE_CELL=%s:%s:%d", eng, d.dir, days), "MEASURE_OUT="+path+".tmp")
					outb, err := cmd.CombinedOutput()
					if err != nil {
						return fmt.Errorf("%s %s %s %dd: %w\n%s", v.name, eng, d.dir, days, err, outb)
					}
					b, err := os.ReadFile(path + ".tmp")
					if err != nil {
						return err
					}
					if err := json.Unmarshal(b, &c); err != nil {
						return err
					}
					c.Volume, c.GateLoad, c.Quiet = v.name, load, quiet
					if b, err = json.Marshal(c); err != nil {
						return err
					}
					if err := os.WriteFile(path, b, 0o644); err != nil {
						return err
					}
					os.Remove(path + ".tmp")
					logf("%s %s %s %dd: first %.0f ms, best %.0f ms, peak %d + %d MB, load %.2f%s", v.name, eng, d.dir, days,
						c.FirstMS, c.BestMS, c.SelfPeak/1024, c.ChildPeak/1024, load, map[bool]string{false: " (unquiet)"}[quiet])
					all = append(all, c)
				}
			}
		}
	}
	b, err := json.MarshalIndent(all, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "dashboards.json"), b, 0o644); err != nil {
		return err
	}
	md := dashboardsMD(all, start, *passes)
	f, err := os.OpenFile(*out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(md)
	logf("dashboards: done in %s", time.Since(start).Round(time.Second))
	return err
}

func mb(kb int) string { return fmt.Sprintf("%d MB", (kb+512)/1024) }

func dashMS(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.2f s", v/1000)
	}
	if v >= 10 {
		return fmt.Sprintf("%.0f ms", v)
	}
	return fmt.Sprintf("%.1f ms", v)
}

// slowest is the cell's slowest widget in its best run.
func slowest(c dashCell) string {
	name, top := "", -1
	for n, v := range c.Best {
		if v > top || v == top && n < name {
			name, top = n, v
		}
	}
	return fmt.Sprintf("%s %d ms", name, top)
}

func dashboardsMD(all []dashCell, start time.Time, passes int) string {
	var sb strings.Builder
	key := func(vol, eng, dir string, days int) string { return fmt.Sprintf("%s|%s|%s|%d", vol, eng, dir, days) }
	cells := map[string]dashCell{}
	var loads []float64
	unquiet := 0
	for _, c := range all {
		cells[key(c.Volume, c.Engine, c.Dashboard, c.Days)] = c
		loads = append(loads, c.GateLoad)
		if !c.Quiet {
			unquiet++
		}
	}
	lo, hi := minMax(loads)
	fmt.Fprintf(&sb, "\n## Step 3f: built-in dashboards\n\n"+
		"Started %s. Every cell is a fresh process (`native-bench dashboards`, which runs the main module's "+
		"`TestMeasureSystemDashboard`, `internal/app/measure_system_test.go`) with GOMAXPROCS=2: one dashboard over one "+
		"range ending 2026-09-30, every widget requested at once as the console sends them, once (\"first\") and then "+
		"%d times more (\"best\" = the fastest of those); wall is from the first request to the last answer. "+
		"Native = the dashboard's query widgets through `reporting.Service.WidgetData` with `native.New`'s defaults "+
		"(MaxScans 2, 2 workers each). DuckDB = the old SQL kept for parity (`internal/reporting/testdata/system-sql`, "+
		"and `vitals-by-device-browser.sql` for the two vitals widgets split from it) through `Service.Preview` "+
		"(uncached), so through `archive.Reader` and the engine child at the defaults (2 threads at GOMAXPROCS=2, one "+
		"query at a time, 512 MB); its first run includes spawning the child and building the views. Neither side has a "+
		"result cache; native keeps file footers and identities between runs in the process, so its warm runs reuse them. "+
		"Peak = VmHWM: native is the Go process; DuckDB is the Go process plus the engine child (\"Go + child\"). "+
		"Before every cell the bench waits until the 1-minute load average is at most %.1f (polling every %s, "+
		"giving up after %s and flagging the cell). Data: `archivetest.Generated` at 100k and 1M events a month, "+
		"12 months (2025-10 to 2026-09), with group ids (2,000 groups) and web vitals (half the measures), then the "+
		"daily pass (sessions, identities, aliases); new files under `~/.cache/twbench-native/3f/`, since step 1's "+
		"had neither group ids nor vitals.\n",
		start.UTC().Format(time.RFC3339), passes, quietLoad, quietPoll, dashQuietLimit)
	for _, v := range volumes {
		has := false
		for _, c := range all {
			has = has || c.Volume == v.name
		}
		if !has {
			continue
		}
		fmt.Fprintf(&sb, "\n### Step 3f, volume %s (%d events a month)\n\n", v.name, v.perMonth)
		sb.WriteString("| Dashboard (widgets native / SQL) | Range | Native first | Native best | DuckDB first | DuckDB best | Native peak | DuckDB peak (Go + child) | Slowest native widget (best run) | Load at start (native / DuckDB) |\n")
		sb.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for _, d := range dashboards3f {
			for _, days := range []int{7, 30, 90, 365} {
				n, okN := cells[key(v.name, "native", d.dir, days)]
				q, okQ := cells[key(v.name, "duckdb", d.dir, days)]
				if !okN || !okQ {
					continue
				}
				flag := func(c dashCell) string {
					if c.Quiet {
						return fmt.Sprintf("%.2f", c.GateLoad)
					}
					return fmt.Sprintf("%.2f (unquiet)", c.GateLoad)
				}
				fmt.Fprintf(&sb, "| %s (%d / %d) | %d d | %s | %s | %s | %s | %s | %s (%s + %s) | %s | %s / %s |\n",
					d.title, n.Widgets, q.Widgets, days, dashMS(n.FirstMS), dashMS(n.BestMS), dashMS(q.FirstMS), dashMS(q.BestMS),
					mb(n.SelfPeak), mb(q.SelfPeak+q.ChildPeak), mb(q.SelfPeak), mb(q.ChildPeak), slowest(n), flag(n), flag(q))
			}
		}
	}
	fmt.Fprintf(&sb, "\nLoad average (1 min) at cell start: %.2f to %.2f; cells that started above %.1f (the gate gave up): %d of %d.\n",
		lo, hi, quietLoad, unquiet, len(all))
	return sb.String()
}
