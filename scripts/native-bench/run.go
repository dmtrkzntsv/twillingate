package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ---- query: one query, once, in this process ----

var queryFuncs = map[string]func(Source, []string, int64, int64) ([]Row, error){
	"countries":         Countries,
	"dau":               DAU,
	"attrvalues":        AttributeValues,
	"attrvalues-approx": AttributeValuesApprox,
}

func newSource(name string) (Source, error) {
	switch name {
	case "arrow":
		return NewArrowSource(), nil
	case "pqgo":
		return NewPQGoSource(), nil
	}
	return nil, fmt.Errorf("unknown source %q (arrow, pqgo)", name)
}

func parseDay(s string) (int64, error) {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return 0, err
	}
	return d.Unix() / 86400, nil
}

// QueryResult is what `query` prints: one JSON line. Hash identifies the
// result rows, so the matrix can compare cells without printing them.
type QueryResult struct {
	WallMS    float64 `json:"wall_ms"`
	UserMS    float64 `json:"user_ms"`
	SysMS     float64 `json:"sys_ms"`
	PeakRSSKB int64   `json:"peak_rss_kb"`
	Rows      int     `json:"rows"`
	Hash      string  `json:"hash"`
}

func hashRows(rows []Row) string {
	h := sha256.New()
	for _, r := range rows {
		for _, v := range r {
			io.WriteString(h, v)
			h.Write([]byte{0x1f})
		}
		h.Write([]byte{0x1e})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

const queryUsage = "usage: native-bench query -src arrow|pqgo -q countries|dau|attrvalues|attrvalues-approx -from YYYY-MM-DD -to YYYY-MM-DD [-workers N] [-rows FILE] <files...>"

func queryCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	srcName := fs.String("src", "", "arrow or pqgo")
	qName := fs.String("q", "", "countries, dau, attrvalues or attrvalues-approx")
	fromS := fs.String("from", "", "first day, YYYY-MM-DD")
	toS := fs.String("to", "", "last day, YYYY-MM-DD")
	workers := fs.Int("workers", 2, "goroutines scanning row groups")
	rowsOut := fs.String("rows", "", "also write the result rows, as JSON, to this file (after the measurement)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	Workers = *workers
	q, ok := queryFuncs[*qName]
	if !ok || fs.NArg() == 0 {
		return errors.New(queryUsage)
	}
	src, err := newSource(*srcName)
	if err != nil {
		return err
	}
	from, err := parseDay(*fromS)
	if err != nil {
		return err
	}
	to, err := parseDay(*toS)
	if err != nil {
		return err
	}
	u0, s0 := cpuTimes()
	start := time.Now()
	rows, err := q(src, fs.Args(), from, to)
	wall := time.Since(start)
	u1, s1 := cpuTimes()
	if err != nil {
		return err
	}
	peak, err := peakRSSKB()
	if err != nil {
		return err
	}
	if err := json.NewEncoder(w).Encode(QueryResult{
		WallMS: ms(wall), UserMS: ms(u1 - u0), SysMS: ms(s1 - s0),
		PeakRSSKB: peak, Rows: len(rows), Hash: hashRows(rows),
	}); err != nil {
		return err
	}
	if *rowsOut == "" {
		return nil
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	return os.WriteFile(*rowsOut, b, 0o644)
}

// ---- matrix: every cell in a fresh process ----

var (
	months = []string{"2025-10", "2025-11", "2025-12", "2026-01", "2026-02", "2026-03",
		"2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09"}
	volumes = []struct {
		name     string
		perMonth int
	}{{"100k", 100000}, {"1m", 1000000}}
	layouts = []string{"duckdb", "arrow-1d", "arrow-4d", "pqgo-1d", "pqgo-4d"}
	ranges  = []struct{ name, from, to string }{
		{"30 d", "2026-09-01", "2026-09-30"},
		{"365 d", "2025-10-01", "2026-09-30"},
	}
	srcNames   = []string{"arrow", "pqgo"}
	queryNames = []string{"countries", "dau", "attrvalues"}
)

const cellRuns = 3

// FileWrite is one month file of a layout. Wall and PeakRSSKB are zero for
// the DuckDB layout, which is not rewritten.
type FileWrite struct {
	Month     string  `json:"month"`
	WallMS    float64 `json:"wall_ms"`
	Bytes     int64   `json:"bytes"`
	RowGroups int     `json:"row_groups"`
	PeakRSSKB int64   `json:"peak_rss_kb"`
	Load      float64 `json:"load,omitempty"`    // 1-minute load the write started at (step 1b)
	Unquiet   bool    `json:"unquiet,omitempty"` // the quiet gate gave up before it
}

// Cell is one volume x layout x source x query x range, run cellRuns times.
type Cell struct {
	Layout, Source, Query, Range string
	Load                         float64
	Runs                         []QueryResult
	Err                          string
	Loads                        []float64 `json:",omitempty"` // per run (step 1b)
	Unquiet                      int       `json:",omitempty"` // runs the quiet gate gave up before
}

type matrix struct {
	root, exe, goBin, srcDir, out string
	log                           io.Writer
	loads                         []float64
	// gate, when set (step 1b), runs before every query run and every
	// rewrite and returns the load it ended at and whether that was quiet.
	gate func(what string) (float64, bool)
}

func matrixCmd(args []string) error {
	fs := flag.NewFlagSet("matrix", flag.ContinueOnError)
	goBin := fs.String("go", "/usr/local/go/bin/go", "go tool, for the size check builds")
	out := fs.String("out", "RESULTS.md", "Markdown file the tables are appended to")
	only := fs.String("volumes", "100k,1m", "volumes to run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: native-bench matrix [-out RESULTS.md] <root>")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	m := &matrix{root: fs.Arg(0), exe: exe, goBin: *goBin, srcDir: wd, out: *out, log: os.Stdout}
	start := time.Now()
	m.logf("matrix: root %s, binary %s", m.root, m.exe)
	if err := m.appendMD(fmt.Sprintf("\n## Task 5: native scans and writes (matrix)\n\n"+
		"Started %s. Every cell is a fresh `native-bench query` process with GOMAXPROCS=2; "+
		"each runs %d times: \"first\" is run 1, \"best\" the minimum of runs 2 to %d (each metric "+
		"on its own). CPU is user+sys of the query alone; peak RSS is the process's VmHWM. "+
		"Files passed are the months overlapping the range (1 for 30 d, 12 for 365 d). Layouts: "+
		"`duckdb` = the original files; `<lib>-1d` / `<lib>-4d` = rewritten by that library "+
		"with row groups of PerMonth/30 / PerMonth*4/30 rows. Rows = result rows; every cell of a "+
		"volume x query x range must hash to the same rows.\n",
		start.UTC().Format(time.RFC3339), cellRuns, cellRuns)); err != nil {
		return err
	}
	for _, v := range volumes {
		if !slices.Contains(strings.Split(*only, ","), v.name) {
			continue
		}
		if err := m.volume(v.name, v.perMonth); err != nil {
			return err
		}
	}
	if err := m.sizes(); err != nil {
		return err
	}
	lo, hi := minMax(m.loads)
	m.logf("matrix: done in %s", time.Since(start).Round(time.Second))
	return m.appendMD(fmt.Sprintf("\nLoad average (1 min) over every cell: %.2f to %.2f. Matrix wall: %s.\n",
		lo, hi, time.Since(start).Round(time.Second)))
}

func (m *matrix) logf(format string, a ...any) {
	fmt.Fprintf(m.log, "%s "+format+"\n", append([]any{time.Now().Format("15:04:05")}, a...)...)
}

func (m *matrix) appendMD(s string) error {
	f, err := os.OpenFile(m.out, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(s); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (m *matrix) env() []string {
	env := slices.DeleteFunc(os.Environ(), func(s string) bool { return strings.HasPrefix(s, "GOMAXPROCS=") })
	return append(env, "GOMAXPROCS=2")
}

// run execs the bench binary, returning stdout and the child's rusage.
func (m *matrix) run(args ...string) ([]byte, *syscall.Rusage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := osexec.CommandContext(ctx, m.exe, args...)
	cmd.Env = m.env()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var ru *syscall.Rusage
	if cmd.ProcessState != nil {
		ru, _ = cmd.ProcessState.SysUsage().(*syscall.Rusage)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[len(msg)-200:]
		}
		return nil, ru, fmt.Errorf("%v: %s", err, msg)
	}
	return stdout.Bytes(), ru, nil
}

func (m *matrix) layoutPath(vol, layout, month string) string {
	if layout == "duckdb" {
		return filepath.Join(m.root, vol, "raw", "1", month+".parquet")
	}
	return filepath.Join(m.root, vol, layout, month+".parquet")
}

// groupRows is a layout's rows per row group: PerMonth/30 for "-1d",
// PerMonth*4/30 for "-4d", PerMonth/90 for "-8h".
func groupRows(layout string, perMonth int) int {
	switch {
	case strings.HasSuffix(layout, "-4d"):
		return perMonth * 4 / 30
	case strings.HasSuffix(layout, "-8h"):
		return perMonth / 90
	}
	return perMonth / 30
}

// writeLayout rewrites the twelve months for one layout, each in its own
// process, or reads the stats of a file made by an earlier run.
func (m *matrix) writeLayout(vol string, perMonth int, layout string) ([]FileWrite, error) {
	var out []FileWrite
	lib, _, _ := strings.Cut(layout, "-")
	for _, mo := range months {
		in := m.layoutPath(vol, "duckdb", mo)
		if layout == "duckdb" {
			b, g, err := fileGroups(in)
			if err != nil {
				return nil, err
			}
			out = append(out, FileWrite{Month: mo, Bytes: b, RowGroups: g})
			continue
		}
		dst := m.layoutPath(vol, layout, mo)
		if _, err := os.Stat(dst); err == nil {
			if b, err := os.ReadFile(dst + ".stats.json"); err == nil {
				var fw FileWrite
				if json.Unmarshal(b, &fw) == nil {
					out = append(out, fw)
					continue
				}
			}
		}
		fw, err := m.rewriteMonth(vol, layout, lib, groupRows(layout, perMonth), mo, in, dst)
		if err != nil {
			return nil, err
		}
		out = append(out, fw)
	}
	return out, nil
}

// rewriteMonth rewrites one month file in its own process, behind the gate
// when there is one, and stores its stats beside it.
func (m *matrix) rewriteMonth(vol, layout, lib string, rows int, mo, in, dst string) (FileWrite, error) {
	fw := FileWrite{Month: mo}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fw, err
	}
	if m.gate != nil {
		var quiet bool
		fw.Load, quiet = m.gate(fmt.Sprintf("rewrite %s %s %s", vol, layout, mo))
		fw.Unquiet = !quiet
	}
	stdout, ru, err := m.run("rewrite", "-lib", lib, "-rows", strconv.Itoa(rows), in, dst)
	if err != nil {
		return fw, fmt.Errorf("rewrite %s %s %s: %w", vol, layout, mo, err)
	}
	for _, f := range strings.Fields(string(stdout)) {
		k, v, _ := strings.Cut(f, "=")
		switch k {
		case "wall":
			d, _ := time.ParseDuration(v)
			fw.WallMS = ms(d)
		case "bytes":
			fw.Bytes, _ = strconv.ParseInt(v, 10, 64)
		case "row_groups":
			fw.RowGroups, _ = strconv.Atoi(v)
		}
	}
	if ru != nil {
		fw.PeakRSSKB = ru.Maxrss // KB on Linux
	}
	b, _ := json.Marshal(fw)
	if err := os.WriteFile(dst+".stats.json", b, 0o644); err != nil {
		return fw, err
	}
	m.logf("rewrite %s %s %s: load %.2f, wall %.0f ms, %d bytes, %d groups, peak %d KB",
		vol, layout, mo, fw.Load, fw.WallMS, fw.Bytes, fw.RowGroups, fw.PeakRSSKB)
	return fw, nil
}

// rangeFiles are the layout's months overlapping [from, to].
func (m *matrix) rangeFiles(vol, layout, from, to string) []string {
	var files []string
	for _, mo := range months {
		if mo >= from[:7] && mo <= to[:7] {
			files = append(files, m.layoutPath(vol, layout, mo))
		}
	}
	return files
}

func (m *matrix) cell(vol, layout, src, q string, r struct{ name, from, to string }) Cell {
	c := Cell{Layout: layout, Source: src, Query: q, Range: r.name, Load: loadAvg1()}
	m.loads = append(m.loads, c.Load)
	args := append([]string{"query", "-src", src, "-q", q, "-from", r.from, "-to", r.to},
		m.rangeFiles(vol, layout, r.from, r.to)...)
	for i := 0; i < cellRuns; i++ {
		stdout, _, err := m.run(args...)
		if err != nil {
			c.Err = fmt.Sprintf("run %d: %v", i+1, err)
			break
		}
		var res QueryResult
		if err := json.Unmarshal(stdout, &res); err != nil {
			c.Err = fmt.Sprintf("run %d: %v in %q", i+1, err, stdout)
			break
		}
		if i > 0 && res.Hash != c.Runs[0].Hash {
			c.Err = fmt.Sprintf("run %d: rows differ from run 1", i+1)
		}
		c.Runs = append(c.Runs, res)
	}
	if len(c.Runs) > 0 {
		m.logf("cell %s %s %s %s %s: load %.2f, first %.1f ms, rows %d, peak %d KB %s",
			vol, layout, src, q, r.name, c.Load, c.Runs[0].WallMS, c.Runs[0].Rows, c.Runs[0].PeakRSSKB, c.Err)
	} else {
		m.logf("cell %s %s %s %s %s: FAILED %s", vol, layout, src, q, r.name, c.Err)
	}
	return c
}

func (m *matrix) volume(vol string, perMonth int) error {
	m.logf("volume %s", vol)
	writes := map[string][]FileWrite{}
	for _, l := range layouts {
		fw, err := m.writeLayout(vol, perMonth, l)
		if err != nil {
			return err
		}
		writes[l] = fw
	}
	var cells []*Cell
	for _, l := range layouts {
		for _, s := range srcNames {
			for _, q := range queryNames {
				for _, r := range ranges {
					c := m.cell(vol, l, s, q, r)
					cells = append(cells, &c)
				}
			}
		}
	}
	checkHashes(cells)
	if b, err := json.MarshalIndent(map[string]any{"writes": writes, "cells": cells}, "", " "); err == nil {
		os.WriteFile(filepath.Join(m.root, "matrix-"+vol+".json"), b, 0o644)
	}
	return m.appendMD(volumeMD(vol, perMonth, writes, cells))
}

// checkHashes fails every cell whose rows differ from the rows most cells
// of its query x range returned.
func checkHashes(cells []*Cell) {
	groups := map[string][]*Cell{}
	for _, c := range cells {
		if len(c.Runs) > 0 {
			groups[c.Query+"|"+c.Range] = append(groups[c.Query+"|"+c.Range], c)
		}
	}
	for _, g := range groups {
		count := map[string]int{}
		for _, c := range g {
			count[c.Runs[0].Hash]++
		}
		ref, best := "", 0
		for h, n := range count {
			if n > best || (n == best && h < ref) {
				ref, best = h, n
			}
		}
		for _, c := range g {
			if c.Runs[0].Hash != ref && c.Err == "" {
				c.Err = fmt.Sprintf("rows differ (hash %s, %d of %d cells agree on %s)",
					c.Runs[0].Hash, best, len(g), ref)
			}
		}
	}
}

func minMax(xs []float64) (float64, float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	return slices.Min(xs), slices.Max(xs)
}

func fmtMS(v float64) string {
	if v < 10 {
		return fmt.Sprintf("%.1f ms", v)
	}
	return fmt.Sprintf("%.0f ms", v)
}

func fmtMB(kb int64) string {
	if kb == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f MB", float64(kb)/1024)
}

// best is each metric's minimum over runs 2.. (run 1 when there is no other).
func best(runs []QueryResult) QueryResult {
	rs := runs
	if len(rs) > 1 {
		rs = rs[1:]
	}
	b := rs[0]
	for _, r := range rs[1:] {
		b.WallMS = min(b.WallMS, r.WallMS)
		if r.UserMS+r.SysMS < b.UserMS+b.SysMS {
			b.UserMS, b.SysMS = r.UserMS, r.SysMS
		}
		b.PeakRSSKB = min(b.PeakRSSKB, r.PeakRSSKB)
	}
	return b
}

func volumeMD(vol string, perMonth int, writes map[string][]FileWrite, cells []*Cell) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n### Volume %s (%d events a month)\n\n", vol, perMonth)
	b.WriteString("Rewrites (12 month files per layout, each by its own process; max = the slowest / largest file):\n\n")
	b.WriteString("| Layout | Rows per group | Total size | Row groups (12 files) | Max wall per file | Max writer peak RSS |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, l := range layouts {
		var bytes int64
		var groups int
		var wall float64
		var rss int64
		for _, f := range writes[l] {
			bytes += f.Bytes
			groups += f.RowGroups
			wall = max(wall, f.WallMS)
			rss = max(rss, f.PeakRSSKB)
		}
		per := "DuckDB's own"
		wallS := "-"
		if l != "duckdb" {
			n := perMonth / 30
			if strings.HasSuffix(l, "4d") {
				n = perMonth * 4 / 30
			}
			per = strconv.Itoa(n)
			wallS = fmtMS(wall)
		}
		fmt.Fprintf(&b, "| %s | %s | %.1f MB | %d | %s | %s |\n", l, per, float64(bytes)/(1<<20), groups, wallS, fmtMB(rss))
	}
	b.WriteString("\nQueries:\n\n| Layout | Source | Query | Range | First wall | Best wall | Best CPU | Peak RSS | Rows |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	var loads []float64
	failed := 0
	for _, c := range cells {
		loads = append(loads, c.Load)
		if len(c.Runs) == 0 {
			failed++
			fmt.Fprintf(&b, "| %s | %s | %s | %s | - | - | - | - | FAILED: %s |\n", c.Layout, c.Source, c.Query, c.Range, c.Err)
			continue
		}
		bst := best(c.Runs)
		rows := strconv.Itoa(c.Runs[0].Rows)
		if c.Err != "" {
			failed++
			rows += " FAILED: " + c.Err
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", c.Layout, c.Source, c.Query, c.Range,
			fmtMS(c.Runs[0].WallMS), fmtMS(bst.WallMS), fmtMS(bst.UserMS+bst.SysMS), fmtMB(bst.PeakRSSKB), rows)
	}
	lo, hi := minMax(loads)
	fmt.Fprintf(&b, "\nFailed cells: %d of %d. Load average (1 min) at cell start: %.2f to %.2f.\n", failed, len(cells), lo, hi)

	b.WriteString("\nThe three queries one after another (sum of walls; peak = the largest of the three):\n\n")
	b.WriteString("| Layout | Source | 30 d first | 30 d best | 365 d first | 365 d best | Peak RSS |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, l := range layouts {
		for _, s := range srcNames {
			var sum [2][2]float64
			var peak int64
			ok := true
			for _, c := range cells {
				if c.Layout != l || c.Source != s {
					continue
				}
				if len(c.Runs) == 0 {
					ok = false
					continue
				}
				ri := 0
				if c.Range == ranges[1].name {
					ri = 1
				}
				sum[ri][0] += c.Runs[0].WallMS
				sum[ri][1] += best(c.Runs).WallMS
				peak = max(peak, best(c.Runs).PeakRSSKB)
			}
			note := ""
			if !ok {
				note = " (a cell failed)"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s%s |\n", l, s,
				fmtMS(sum[0][0]), fmtMS(sum[0][1]), fmtMS(sum[1][0]), fmtMS(sum[1][1]), fmtMB(peak), note)
		}
	}
	return b.String()
}

// sizes builds the size-check mains stripped and records their bytes.
func (m *matrix) sizes() error {
	bin := filepath.Join(m.root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("\n### Binary size\n\n`go build -trimpath -ldflags=\"-s -w\"` (GOOS/GOARCH of this host), a main that opens a file and prints its row count:\n\n")
	b.WriteString("| Main | Bytes | MB | Over the empty main |\n| --- | --- | --- | --- |\n")
	var floor int64
	for _, s := range []struct{ name, pkg string }{
		{"empty `func main(){}`", "./sizecheck/empty"},
		{"arrow-go reader", "./sizecheck/arrow"},
		{"parquet-go reader", "./sizecheck/pqgo"},
		{"native-bench (both, plus the queries)", "."},
	} {
		out := filepath.Join(bin, "size-"+filepath.Base(s.pkg))
		if s.pkg == "." {
			out = filepath.Join(bin, "size-native-bench")
		}
		cmd := osexec.Command(m.goBin, "build", "-trimpath", "-ldflags=-s -w", "-o", out, s.pkg)
		cmd.Dir = m.srcDir
		cmd.Stderr = m.log
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s: %w", s.pkg, err)
		}
		fi, err := os.Stat(out)
		if err != nil {
			return err
		}
		if floor == 0 {
			floor = fi.Size()
		}
		m.logf("size %s: %d bytes", s.pkg, fi.Size())
		fmt.Fprintf(&b, "| %s | %d | %.1f MB | %.1f MB |\n", s.name, fi.Size(), float64(fi.Size())/(1<<20), float64(fi.Size()-floor)/(1<<20))
	}
	return m.appendMD(b.String())
}

// waitQuiet polls load every poll until it reads at most maxLoad, or limit
// has elapsed. It returns the last load read (-1 if it never read one) and
// whether it was quiet; a load that fails to read counts as not quiet.
func waitQuiet(maxLoad float64, poll, limit time.Duration, load func() (float64, error)) (float64, bool) {
	deadline := time.Now().Add(limit)
	last := -1.0
	for {
		if l, err := load(); err == nil {
			last = l
			if l <= maxLoad {
				return l, true
			}
		}
		left := time.Until(deadline)
		if left <= 0 {
			return last, false
		}
		time.Sleep(min(poll, left))
	}
}
