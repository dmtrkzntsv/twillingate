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
	Engine    string            `json:"engine"`
	Mode      string            `json:"mode"` // widgets, many or stream (step 4c on; empty before)
	OpenLog   bool              `json:"open_log"`
	Dashboard string            `json:"dashboard"`
	Days      int               `json:"days"`
	From      string            `json:"from"`
	To        string            `json:"to"`
	Widgets   int               `json:"widgets"`
	FirstMS   float64           `json:"first_ms"`
	FirstOne  float64           `json:"first_widget_first_ms"`
	BestOne   float64           `json:"best_widget_first_ms"`
	WarmMS    []float64         `json:"warm_ms"`
	WarmCPU   []float64         `json:"warm_cpu_ms"`
	BestMS    float64           `json:"best_ms"`
	SelfPeak  int               `json:"self_peak_kb"`
	ChildPeak int               `json:"child_peak_kb"`
	Children  int               `json:"children"`
	IdleRSS   int               `json:"idle_rss_kb"`
	First     map[string]int    `json:"first_widget_ms"`
	Best      map[string]int    `json:"best_widget_ms"`
	Rows      map[string]int    `json:"rows"`
	Errors    map[string]string `json:"errors"`
	Load      string            `json:"load_at_start"`
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
	engines := fs.String("engines", "native,duckdb", "engines to run; stream is native through one WidgetDataStream (MEASURE_STREAM)")
	cellsName := fs.String("cells", "cells", "the directory under each volume the cells are kept in")
	openLogs := fs.String("openlog", "", "<volume>=<log>,...: each volume's Views and Product at 30 days run again, but stream and native, with <log> as -today's open log")
	today := fs.String("today", "2026-10-01", "the day an -openlog log is today's")
	streamMD := fs.Bool("stream-md", false, "write step 4c's tables (stream against per widget, -before and DuckDB) instead")
	from := fs.String("from", "", "a root whose <volume>/raw month files are rebuilt through the Go pass instead of generated (step 4)")
	before := fs.String("before", "", "an earlier run's dashboards.json, compared against this one's native cells")
	ranges := fs.String("ranges", "", "day ranges to run (default all); with -small, the dashboards without a year run only these")
	small := fs.String("small", "", "the ranges the dashboards without a year run (default as -ranges)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	wantDays := func(year bool, days int) bool {
		list := *ranges
		if !year && *small != "" {
			list = *small
		}
		return list == "" || slices.Contains(strings.Split(list, ","), fmt.Sprint(days))
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
		cellsDir := filepath.Join(dir, *cellsName)
		if err := os.MkdirAll(cellsDir, 0o750); err != nil {
			return err
		}
		// The files: generated (and derived) by the test's first run.
		gen := osexec.Command(*bin, "-test.run", "^TestMeasureSystemDashboard$", "-test.v", "-test.timeout", "2h")
		gen.Dir, gen.Stdout, gen.Stderr = *pkg, os.Stdout, os.Stderr
		gen.Env = append(os.Environ(), "MEASURE_DIR="+dir, fmt.Sprintf("MEASURE_MONTH_EVENTS=%d", v.perMonth))
		if *from != "" {
			gen.Env = append(gen.Env, "MEASURE_FROM="+filepath.Join(*from, v.name))
		}
		if err := gen.Run(); err != nil {
			return fmt.Errorf("%s: generating: %w", v.name, err)
		}
		logOf := map[string]string{}
		for _, kv := range strings.Split(*openLogs, ",") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				logOf[k] = v
			}
		}
		type cellSpec struct {
			dir  string
			days int
			log  bool
		}
		var specs []cellSpec
		for _, days := range []int{7, 30, 90, 365} {
			for _, d := range dashboards3f {
				if days == 365 && !d.year || !wantDays(d.year, days) {
					continue
				}
				specs = append(specs, cellSpec{d.dir, days, false})
			}
		}
		if logOf[v.name] != "" {
			specs = append(specs, cellSpec{"views", 30, true}, cellSpec{"product", 30, true})
		}
		for _, sp := range specs {
			days := sp.days
			for _, eng := range strings.Split(*engines, ",") {
				if sp.log && eng == "duckdb" {
					continue
				}
				suffix := ""
				if sp.log {
					suffix = "-log"
				}
				path := filepath.Join(cellsDir, fmt.Sprintf("%s-%s-%d%s.json", eng, sp.dir, days, suffix))
				var c dashCell
				if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &c) == nil && c.Volume != "" {
					logf("%s %s %s %dd: kept from an earlier run", v.name, eng, sp.dir, days)
					all = append(all, c)
					continue
				}
				load, quiet := waitQuiet(quietLoad, quietPoll, dashQuietLimit, readLoad)
				if !quiet {
					logf("gate: gave up at load %.2f", load)
				}
				cmd := osexec.Command(*bin, "-test.run", "^TestMeasureSystemDashboard$", "-test.v", "-test.timeout", "30m")
				cmd.Dir = *pkg
				cellEng := eng
				cmd.Env = append(os.Environ(), "GOMAXPROCS=2", "MEASURE_KEEP_GOING=1", "MEASURE_DIR="+dir,
					fmt.Sprintf("MEASURE_MONTH_EVENTS=%d", v.perMonth), fmt.Sprintf("MEASURE_PASSES=%d", *passes),
					"MEASURE_OUT="+path+".tmp")
				if eng == "stream" {
					cellEng = "native"
					cmd.Env = append(cmd.Env, "MEASURE_STREAM=1")
				}
				cmd.Env = append(cmd.Env, fmt.Sprintf("MEASURE_CELL=%s:%s:%d", cellEng, sp.dir, days))
				if sp.log {
					cmd.Env = append(cmd.Env, "MEASURE_OPEN_LOG="+logOf[v.name], "MEASURE_TODAY="+*today)
				}
				outb, err := cmd.CombinedOutput()
				if err != nil {
					return fmt.Errorf("%s %s %s %dd: %w\n%s", v.name, eng, sp.dir, days, err, outb)
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
				logf("%s %s %s %dd%s: first %.0f ms, best %.0f ms (first widget %.0f ms), peak %d + %d MB, load %.2f%s", v.name, eng, sp.dir, days, suffix,
					c.FirstMS, c.BestMS, c.BestOne, c.SelfPeak/1024, c.ChildPeak/1024, load, map[bool]string{false: " (unquiet)"}[quiet])
				all = append(all, c)
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
	if *streamMD {
		var old []dashCell
		if *before != "" {
			b, err := os.ReadFile(*before)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(b, &old); err != nil {
				return err
			}
		}
		md = dashboardsStreamMD(all, old, start, *passes)
	} else if *before != "" {
		b, err := os.ReadFile(*before)
		if err != nil {
			return err
		}
		var old []dashCell
		if err := json.Unmarshal(b, &old); err != nil {
			return err
		}
		md = dashboardsCompareMD(all, old, start, *passes)
	}
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

// dashboardsCompareMD is the native cells of all (step 4, on files the Go
// pass wrote) against the same cells of before (step 3f, on files DuckDB
// wrote), with before's DuckDB cell for reference.
func dashboardsCompareMD(all, before []dashCell, start time.Time, passes int) string {
	var sb strings.Builder
	key := func(vol, eng, dir string, days int) string { return fmt.Sprintf("%s|%s|%s|%d", vol, eng, dir, days) }
	old := map[string]dashCell{}
	for _, c := range before {
		old[key(c.Volume, c.Engine, c.Dashboard, c.Days)] = c
	}
	var loads []float64
	unquiet := 0
	for _, c := range all {
		loads = append(loads, c.GateLoad)
		if !c.Quiet {
			unquiet++
		}
	}
	lo, hi := minMax(loads)
	fmt.Fprintf(&sb, "\n## Step 4: built-in dashboards on files the Go pass wrote\n\n"+
		"Started %s. The same cells as step 3f (`native-bench dashboards`, `TestMeasureSystemDashboard`, one fresh "+
		"process a cell, GOMAXPROCS=2, every widget at once, a first run then %d more, \"best\" the fastest), native only, "+
		"on step 3f's events rebuilt through the daily pass in Go: each month written as one log a day, then dumped, "+
		"merged and derived a month at a time (`MEASURE_FROM`), so a month file has a row group per family per day "+
		"(about 90) where step 3f's DuckDB-written ones had one (100k) or 9 of 122,880 rows (1M). \"3f\" columns are "+
		"step 3f's cells; \"change\" is step 4's best over step 3f's native best. Before every cell the bench waits "+
		"until the 1-minute load average is at most %.1f.\n", start.UTC().Format(time.RFC3339), passes, quietLoad)
	for _, v := range volumes {
		has := false
		for _, c := range all {
			has = has || c.Volume == v.name
		}
		if !has {
			continue
		}
		fmt.Fprintf(&sb, "\n### Step 4 dashboards, volume %s (%d events a month)\n\n", v.name, v.perMonth)
		sb.WriteString("| Dashboard | Range | 3f native first / best | Step 4 native first / best | Change (best) | 3f DuckDB best | 3f native peak | Step 4 native peak | Slowest widget (best run) | Load at start |\n")
		sb.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for _, d := range dashboards3f {
			for _, days := range []int{7, 30, 90, 365} {
				var n dashCell
				found := false
				for _, c := range all {
					if c.Volume == v.name && c.Engine == "native" && c.Dashboard == d.dir && c.Days == days {
						n, found = c, true
					}
				}
				if !found {
					continue
				}
				o, okO := old[key(v.name, "native", d.dir, days)]
				q, okQ := old[key(v.name, "duckdb", d.dir, days)]
				was, change, duck, oldPeak := "–", "–", "–", "–"
				if okO {
					was = dashMS(o.FirstMS) + " / " + dashMS(o.BestMS)
					change = fmt.Sprintf("%.2fx", n.BestMS/o.BestMS)
					oldPeak = mb(o.SelfPeak)
				}
				if okQ {
					duck = dashMS(q.BestMS)
				}
				load := fmt.Sprintf("%.2f", n.GateLoad)
				if !n.Quiet {
					load += " (unquiet)"
				}
				fmt.Fprintf(&sb, "| %s (%d) | %d d | %s | %s / %s | %s | %s | %s | %s | %s | %s |\n", d.title, n.Widgets, days, was,
					dashMS(n.FirstMS), dashMS(n.BestMS), change, duck, oldPeak, mb(n.SelfPeak), slowest(n), load)
			}
		}
	}
	fmt.Fprintf(&sb, "\nLoad average (1 min) at cell start: %.2f to %.2f; cells that started above %.1f (the gate gave up): %d of %d.\n",
		lo, hi, quietLoad, unquiet, len(all))
	return sb.String()
}
