package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ---- matrix1b: step 1b, the optimised scans on a quiet host ----

type range1b struct{ name, from, to string }

var (
	layouts1b = []string{"duckdb", "pqgo-1d", "pqgo-4d", "pqgo-8h", "arrow-1d"}
	// combos1b: pqgo on every layout, arrow on arrow-1d and pqgo-1d (the
	// library re-check).
	combos1b = []struct{ layout, src string }{
		{"duckdb", "pqgo"}, {"pqgo-1d", "pqgo"}, {"pqgo-1d", "arrow"}, {"pqgo-4d", "pqgo"},
		{"pqgo-8h", "pqgo"}, {"arrow-1d", "pqgo"}, {"arrow-1d", "arrow"},
	}
	queries1b = []string{"countries", "dau", "attrvalues", "attrvalues-approx"}
	ranges1b  = []range1b{
		{"1d", "2026-09-30", "2026-09-30"},
		{"7d", "2026-09-08", "2026-09-14"},
		{"30d-mid", "2026-08-16", "2026-09-14"},
		{"30d", "2026-09-01", "2026-09-30"},
		{"365d", "2025-10-01", "2026-09-30"},
	}
	// recheck1b are the rewrites re-timed at 1M behind the gate, into
	// recheck-<layout>/, so step 1's files stay as they are.
	recheck1b = []string{"pqgo-1d", "arrow-1d"}
)

const (
	quietLoad  = 1.0
	quietPoll  = 10 * time.Second
	quietLimit = 10 * time.Minute
)

func readLoad() (float64, error) {
	if l := loadAvg1(); l >= 0 {
		return l, nil
	}
	return 0, errors.New("unreadable /proc/loadavg")
}

func matrix1bCmd(args []string) error {
	fs := flag.NewFlagSet("matrix1b", flag.ContinueOnError)
	out := fs.String("out", "RESULTS.md", "Markdown file the tables are appended to")
	only := fs.String("volumes", "100k,1m", "volumes to run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: native-bench matrix1b [-out RESULTS.md] [-volumes 100k,1m] <root>")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	m := &matrix{root: fs.Arg(0), exe: exe, out: *out, log: os.Stdout}
	m.gate = func(what string) (float64, bool) {
		l, ok := waitQuiet(quietLoad, quietPoll, quietLimit, readLoad)
		if !ok {
			m.logf("gate: gave up before %s at load %.2f", what, l)
		}
		return l, ok
	}
	start := time.Now()
	m.logf("matrix1b: root %s, binary %s", m.root, m.exe)
	if err := m.appendMD(fmt.Sprintf("\n## Step 1b: optimised scans on a quiet host (matrix1b)\n\n"+
		"Started %s. Every run is a fresh `native-bench query` process with GOMAXPROCS=2 and "+
		"`-workers 2`. Before every run and every rewrite the matrix waits until the 1-minute load "+
		"average is at most %.1f (polling every %s, giving up after %s and flagging the run); the "+
		"load each started at is recorded. The loop order is range, query, run index, then layout "+
		"and source, so load drift spreads across layouts. Each cell runs %d times: \"first\" is "+
		"run 1, \"best\" the minimum of runs 2 to %d (each metric on its own). Files passed are the "+
		"months overlapping the range (`30d-mid` spans two). Layouts: `duckdb` = the original files; "+
		"`pqgo-1d` / `pqgo-4d` / `arrow-1d` = step 1's rewrites (PerMonth/30, PerMonth*4/30 rows "+
		"per group); `pqgo-8h` = new, PerMonth/90 rows per group. Sources: `pqgo` on every layout, "+
		"`arrow` on `arrow-1d` and `pqgo-1d`. Ranges: 1d = 2026-09-30; 7d = 2026-09-08..14; "+
		"30d-mid = 2026-08-16..09-14; 30d = 2026-09-01..30; 365d = 2025-10-01..2026-09-30. "+
		"Exact queries must hash-equal across layouts and sources; `attrvalues-approx` is compared "+
		"value by value with `attrvalues` of the same layout, source and range.\n",
		start.UTC().Format(time.RFC3339), quietLoad, quietPoll, quietLimit, cellRuns, cellRuns)); err != nil {
		return err
	}
	for _, v := range volumes {
		if !slices.Contains(strings.Split(*only, ","), v.name) {
			continue
		}
		if err := m.volume1b(v.name, v.perMonth); err != nil {
			return err
		}
	}
	lo, hi := minMax(m.loads)
	m.logf("matrix1b: done in %s", time.Since(start).Round(time.Second))
	return m.appendMD(fmt.Sprintf("\nStep 1b: load average (1 min) at the start of every run: %.2f to %.2f. Matrix wall: %s.\n",
		lo, hi, time.Since(start).Round(time.Second)))
}

func cellKey(layout, src, q, r string) string { return layout + "|" + src + "|" + q + "|" + r }

func (m *matrix) rowsPath(vol string, c *Cell) string {
	return filepath.Join(m.root, vol, "rows1b", c.Layout+"_"+c.Source+"_"+c.Query+"_"+c.Range+".json")
}

// run1b runs one cell's i-th run behind the gate.
func (m *matrix) run1b(vol string, c *Cell, i int, r range1b) {
	load, quiet := m.gate(fmt.Sprintf("%s %s %s %s %s run %d", vol, c.Layout, c.Source, c.Query, c.Range, i+1))
	c.Loads = append(c.Loads, load)
	m.loads = append(m.loads, load)
	if !quiet {
		c.Unquiet++
	}
	if i == 0 {
		c.Load = load
	}
	args := []string{"query", "-src", c.Source, "-q", c.Query, "-from", r.from, "-to", r.to, "-workers", "2"}
	if i == 0 && strings.HasPrefix(c.Query, "attrvalues") {
		args = append(args, "-rows", m.rowsPath(vol, c))
	}
	args = append(args, m.rangeFiles(vol, c.Layout, r.from, r.to)...)
	stdout, _, err := m.run(args...)
	if err != nil {
		c.Err = fmt.Sprintf("run %d: %v", i+1, err)
		m.logf("run %s %s %s %s %s %d: load %.2f FAILED %s", vol, c.Layout, c.Source, c.Query, c.Range, i+1, load, c.Err)
		return
	}
	var res QueryResult
	if err := json.Unmarshal(stdout, &res); err != nil {
		c.Err = fmt.Sprintf("run %d: %v in %q", i+1, err, stdout)
		return
	}
	if i > 0 && res.Hash != c.Runs[0].Hash && c.Err == "" {
		c.Err = fmt.Sprintf("run %d: rows differ from run 1", i+1)
	}
	c.Runs = append(c.Runs, res)
	m.logf("run %s %s %s %s %s %d: load %.2f%s, wall %.1f ms, cpu %.1f ms, peak %d KB, rows %d",
		vol, c.Layout, c.Source, c.Query, c.Range, i+1, load, map[bool]string{true: "", false: " (unquiet)"}[quiet],
		res.WallMS, res.UserMS+res.SysMS, res.PeakRSSKB, res.Rows)
}

// writeFresh re-times a layout's twelve rewrites into recheck-<layout>/,
// deleting what an earlier run left there.
func (m *matrix) writeFresh(vol string, perMonth int, layout string) ([]FileWrite, error) {
	dir := filepath.Join(m.root, vol, "recheck-"+layout)
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	lib, _, _ := strings.Cut(layout, "-")
	var out []FileWrite
	for _, mo := range months {
		fw, err := m.rewriteMonth(vol, "recheck-"+layout, lib, groupRows(layout, perMonth), mo,
			m.layoutPath(vol, "duckdb", mo), filepath.Join(dir, mo+".parquet"))
		if err != nil {
			return nil, err
		}
		out = append(out, fw)
	}
	return out, nil
}

// ApproxStats compares one attrvalues-approx cell with its attrvalues cell.
type ApproxStats struct {
	Layout, Source, Range string
	Err                   string  `json:",omitempty"` // rows that are not the same apart from users and groups
	Values                int     // users and groups values compared
	Outside               int     // values outside the tolerance
	MaxRelErr             float64 // over values whose exact count is at least 50
	Over1024              int     // values whose exact count is above 1,024
	Over1024Outside       int
	MaxUserRelErr         float64
	UsersOutside          int
}

// approxWithin is step 1b's tolerance: within 2%, or within 1 where the
// exact value is under 50.
func approxWithin(got, want uint64) bool {
	if want < 50 {
		return got+1 >= want && got <= want+1
	}
	return relErrU(got, want) <= 0.02
}

func relErrU(got, want uint64) float64 {
	if want == 0 {
		if got == 0 {
			return 0
		}
		return math.Inf(1)
	}
	return math.Abs(float64(got)-float64(want)) / float64(want)
}

// compareApprox checks approx against exact: the same rows in the same
// order, the same attribute, value and count, and users (column 3) and
// groups (column 4) within the tolerance.
func compareApprox(approx, exact []Row) ApproxStats {
	var st ApproxStats
	if len(approx) != len(exact) {
		st.Err = fmt.Sprintf("%d rows, exact has %d", len(approx), len(exact))
		return st
	}
	for i := range exact {
		a, e := approx[i], exact[i]
		if len(a) != 5 || len(e) != 5 || a[0] != e[0] || a[1] != e[1] || a[2] != e[2] {
			st.Err = fmt.Sprintf("row %d: %q, exact %q", i, a, e)
			return st
		}
		for col := 3; col <= 4; col++ {
			g, err1 := strconv.ParseUint(a[col], 10, 64)
			w, err2 := strconv.ParseUint(e[col], 10, 64)
			if err1 != nil || err2 != nil {
				st.Err = fmt.Sprintf("row %d: unparsable %q, exact %q", i, a, e)
				return st
			}
			st.Values++
			ok := approxWithin(g, w)
			if !ok {
				st.Outside++
			}
			if w >= 50 {
				st.MaxRelErr = max(st.MaxRelErr, relErrU(g, w))
				if col == 3 {
					st.MaxUserRelErr = max(st.MaxUserRelErr, relErrU(g, w))
				}
			}
			if col == 3 && !ok {
				st.UsersOutside++
			}
			if w > 1024 {
				st.Over1024++
				if !ok {
					st.Over1024Outside++
				}
			}
		}
	}
	return st
}

// approxFails is the gate on the statistical bound (~2.5 sigma): misses are
// reported, and fail only when more than 1% of the values above 1,024 miss.
func (st ApproxStats) approxFails() bool {
	return st.Err != "" || float64(st.Over1024Outside) > 0.01*float64(st.Over1024)
}

func readRows(path string) ([]Row, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []Row
	return rows, json.Unmarshal(b, &rows)
}

func (m *matrix) volume1b(vol string, perMonth int) error {
	m.logf("volume %s", vol)
	writes := map[string][]FileWrite{}
	for _, l := range layouts1b {
		fw, err := m.writeLayout(vol, perMonth, l)
		if err != nil {
			return err
		}
		writes[l] = fw
	}
	rechecks := map[string][]FileWrite{}
	if vol == "1m" {
		for _, l := range recheck1b {
			fw, err := m.writeFresh(vol, perMonth, l)
			if err != nil {
				return err
			}
			rechecks[l] = fw
		}
	}
	if err := os.MkdirAll(filepath.Join(m.root, vol, "rows1b"), 0o755); err != nil {
		return err
	}
	cells := map[string]*Cell{}
	var order []*Cell
	for _, r := range ranges1b {
		for _, q := range queries1b {
			for _, cb := range combos1b {
				c := &Cell{Layout: cb.layout, Source: cb.src, Query: q, Range: r.name}
				cells[cellKey(cb.layout, cb.src, q, r.name)] = c
				order = append(order, c)
			}
		}
	}
	for _, r := range ranges1b {
		for _, q := range queries1b {
			for i := 0; i < cellRuns; i++ {
				for _, cb := range combos1b {
					c := cells[cellKey(cb.layout, cb.src, q, r.name)]
					if len(c.Runs) < i { // an earlier run failed
						continue
					}
					m.run1b(vol, c, i, r)
				}
			}
		}
	}
	var exact []*Cell
	for _, c := range order {
		if c.Query != "attrvalues-approx" {
			exact = append(exact, c)
		}
	}
	checkHashes(exact)
	var approx []ApproxStats
	for _, cb := range combos1b {
		for _, r := range ranges1b {
			ac := cells[cellKey(cb.layout, cb.src, "attrvalues-approx", r.name)]
			ec := cells[cellKey(cb.layout, cb.src, "attrvalues", r.name)]
			st := ApproxStats{Layout: cb.layout, Source: cb.src, Range: r.name}
			a, err1 := readRows(m.rowsPath(vol, ac))
			e, err2 := readRows(m.rowsPath(vol, ec))
			if err := errors.Join(err1, err2); err != nil {
				st.Err = err.Error()
			} else {
				st = compareApprox(a, e)
				st.Layout, st.Source, st.Range = cb.layout, cb.src, r.name
			}
			if st.approxFails() && ac.Err == "" {
				ac.Err = fmt.Sprintf("approx out of tolerance: %d of %d values above 1,024 miss %s",
					st.Over1024Outside, st.Over1024, st.Err)
			}
			approx = append(approx, st)
		}
	}
	step1 := m.loadStep1(vol)
	if b, err := json.MarshalIndent(map[string]any{"writes": writes, "rechecks": rechecks, "cells": order, "approx": approx}, "", " "); err == nil {
		os.WriteFile(filepath.Join(m.root, "matrix1b-"+vol+".json"), b, 0o644)
	}
	return m.appendMD(volume1bMD(vol, perMonth, writes, rechecks, m.step1Writes(vol), cells, order, approx, step1))
}

// loadStep1 reads step 1's cells for the comparison line; nil if absent.
func (m *matrix) loadStep1(vol string) map[string]*Cell {
	b, err := os.ReadFile(filepath.Join(m.root, "matrix-"+vol+".json"))
	if err != nil {
		return nil
	}
	var doc struct{ Cells []*Cell }
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	out := map[string]*Cell{}
	for _, c := range doc.Cells {
		out[cellKey(c.Layout, c.Source, c.Query, c.Range)] = c
	}
	return out
}

// step1Writes are step 1's stats for the re-checked layouts.
func (m *matrix) step1Writes(vol string) map[string][]FileWrite {
	out := map[string][]FileWrite{}
	for _, l := range recheck1b {
		for _, mo := range months {
			b, err := os.ReadFile(m.layoutPath(vol, l, mo) + ".stats.json")
			if err != nil {
				continue
			}
			var fw FileWrite
			if json.Unmarshal(b, &fw) == nil {
				out[l] = append(out[l], fw)
			}
		}
	}
	return out
}

type writeSum struct {
	bytes            int64
	groups           int
	sumWall, maxWall float64
	maxRSS           int64
	loadLo, loadHi   float64
	unquiet          int
}

func sumWrites(ws []FileWrite) writeSum {
	var s writeSum
	var loads []float64
	for _, f := range ws {
		s.bytes += f.Bytes
		s.groups += f.RowGroups
		s.sumWall += f.WallMS
		s.maxWall = max(s.maxWall, f.WallMS)
		s.maxRSS = max(s.maxRSS, f.PeakRSSKB)
		loads = append(loads, f.Load)
		if f.Unquiet {
			s.unquiet++
		}
	}
	s.loadLo, s.loadHi = minMax(loads)
	return s
}

func volume1bMD(vol string, perMonth int, writes, rechecks, step1Writes map[string][]FileWrite,
	cells map[string]*Cell, order []*Cell, approx []ApproxStats, step1 map[string]*Cell) string {
	var b strings.Builder
	get := func(l, s, q, r string) *Cell { return cells[cellKey(l, s, q, r)] }
	bestOf := func(c *Cell) (QueryResult, bool) {
		if c == nil || len(c.Runs) == 0 {
			return QueryResult{}, false
		}
		return best(c.Runs), true
	}
	fmt.Fprintf(&b, "\n### Step 1b, volume %s (%d events a month)\n\n", vol, perMonth)

	b.WriteString("Layouts (12 month files each; rewrites of step 1's layouts were made then and are reused here):\n\n")
	b.WriteString("| Layout | Rows per group | Total size | Row groups (12 files) | Max wall per file | Max writer peak RSS | Load at the rewrites |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, l := range layouts1b {
		s := sumWrites(writes[l])
		per, wall, load := "DuckDB's own", "-", "-"
		if l != "duckdb" {
			per = strconv.Itoa(groupRows(l, perMonth))
			wall = fmtMS(s.maxWall)
			if l == "pqgo-8h" && s.loadHi > 0 {
				load = fmt.Sprintf("%.2f to %.2f (%d unquiet)", s.loadLo, s.loadHi, s.unquiet)
			} else {
				load = "step 1"
			}
		}
		fmt.Fprintf(&b, "| %s | %s | %.1f MB | %d | %s | %s | %s |\n", l, per, float64(s.bytes)/(1<<20), s.groups, wall, fmtMB(s.maxRSS), load)
	}

	fmt.Fprintf(&b, "\nPer layout, `pqgo` source: best wall / best CPU / peak RSS per range and query.\n")
	for _, l := range layouts1b {
		fmt.Fprintf(&b, "\n`%s`:\n\n| Range | countries | dau | attrvalues | attrvalues-approx |\n| --- | --- | --- | --- | --- |\n", l)
		for _, r := range ranges1b {
			fmt.Fprintf(&b, "| %s |", r.name)
			for _, q := range queries1b {
				if bst, ok := bestOf(get(l, "pqgo", q, r.name)); ok {
					fmt.Fprintf(&b, " %s / %s / %s |", fmtMS(bst.WallMS), fmtMS(bst.UserMS+bst.SysMS), fmtMB(bst.PeakRSSKB))
				} else {
					b.WriteString(" FAILED |")
				}
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\nBest wall side by side (`pqgo` source; first run in brackets):\n\n| Range | Query |")
	for _, l := range layouts1b {
		fmt.Fprintf(&b, " %s |", l)
	}
	b.WriteString("\n| --- | --- |" + strings.Repeat(" --- |", len(layouts1b)) + "\n")
	for _, r := range ranges1b {
		for _, q := range queries1b {
			fmt.Fprintf(&b, "| %s | %s |", r.name, q)
			for _, l := range layouts1b {
				c := get(l, "pqgo", q, r.name)
				if bst, ok := bestOf(c); ok {
					fmt.Fprintf(&b, " %s (%s) |", fmtMS(bst.WallMS), fmtMS(c.Runs[0].WallMS))
				} else {
					b.WriteString(" FAILED |")
				}
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\nDashboard trio per range (countries + dau + attrvalues-approx, sum of best walls; first runs in brackets; peak = the largest of the three), `pqgo` source:\n\n| Range |")
	for _, l := range layouts1b {
		fmt.Fprintf(&b, " %s |", l)
	}
	b.WriteString("\n| --- |" + strings.Repeat(" --- |", len(layouts1b)) + "\n")
	for _, r := range ranges1b {
		fmt.Fprintf(&b, "| %s |", r.name)
		for _, l := range layouts1b {
			var sb, sf float64
			var peak int64
			ok := true
			for _, q := range []string{"countries", "dau", "attrvalues-approx"} {
				c := get(l, "pqgo", q, r.name)
				bst, good := bestOf(c)
				if !good {
					ok = false
					continue
				}
				sb += bst.WallMS
				sf += c.Runs[0].WallMS
				peak = max(peak, bst.PeakRSSKB)
			}
			if ok {
				fmt.Fprintf(&b, " %s (%s), %s |", fmtMS(sb), fmtMS(sf), fmtMB(peak))
			} else {
				b.WriteString(" (a cell failed) |")
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("\nMemory: peak RSS per range, `pqgo` source with 2 workers, the largest over the four queries (and over the trio):\n\n| Range |")
	for _, l := range layouts1b {
		fmt.Fprintf(&b, " %s |", l)
	}
	b.WriteString("\n| --- |" + strings.Repeat(" --- |", len(layouts1b)) + "\n")
	for _, r := range ranges1b {
		fmt.Fprintf(&b, "| %s |", r.name)
		for _, l := range layouts1b {
			var all, trio int64
			for _, q := range queries1b {
				if bst, ok := bestOf(get(l, "pqgo", q, r.name)); ok {
					all = max(all, bst.PeakRSSKB)
					if q != "attrvalues" {
						trio = max(trio, bst.PeakRSSKB)
					}
				}
			}
			fmt.Fprintf(&b, " %s (%s) |", fmtMB(all), fmtMB(trio))
		}
		b.WriteString("\n")
	}

	b.WriteString("\nLibrary re-check: best wall / best CPU / peak RSS, the two sources on the two 1-day layouts:\n\n| Range | Query | pqgo-1d, pqgo | pqgo-1d, arrow | arrow-1d, pqgo | arrow-1d, arrow |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, r := range ranges1b {
		for _, q := range queries1b {
			fmt.Fprintf(&b, "| %s | %s |", r.name, q)
			for _, ls := range [][2]string{{"pqgo-1d", "pqgo"}, {"pqgo-1d", "arrow"}, {"arrow-1d", "pqgo"}, {"arrow-1d", "arrow"}} {
				if bst, ok := bestOf(get(ls[0], ls[1], q, r.name)); ok {
					fmt.Fprintf(&b, " %s / %s / %s |", fmtMS(bst.WallMS), fmtMS(bst.UserMS+bst.SysMS), fmtMB(bst.PeakRSSKB))
				} else {
					b.WriteString(" FAILED |")
				}
			}
			b.WriteString("\n")
		}
	}
	if len(rechecks) > 0 {
		b.WriteString("\nWrites re-timed behind the gate (12 month files each, a fresh directory; step 1's unguarded numbers beside them):\n\n" +
			"| Layout | Total wall | Max wall per file | Max writer peak RSS | Size | Load at the rewrites | Step 1 total wall | Step 1 max per file |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for _, l := range recheck1b {
			s := sumWrites(rechecks[l])
			s1 := sumWrites(step1Writes[l])
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %.1f MB | %.2f to %.2f (%d unquiet) | %s | %s |\n", l,
				fmtMS(s.sumWall), fmtMS(s.maxWall), fmtMB(s.maxRSS), float64(s.bytes)/(1<<20),
				s.loadLo, s.loadHi, s.unquiet, fmtMS(s1.sumWall), fmtMS(s1.maxWall))
		}
	}

	if step1 != nil {
		b.WriteString("\nAgainst step 1 (serial scans, maps; `pqgo-1d`, `pqgo` source, best wall; step 1's approx column is its exact attrvalues):\n\n" +
			"| Range | Query | Step 1 best | Step 1b best | Speedup | Step 1 peak RSS | Step 1b peak RSS |\n| --- | --- | --- | --- | --- | --- | --- |\n")
		for _, rr := range [][2]string{{"30d", "30 d"}, {"365d", "365 d"}} {
			for _, q := range queries1b {
				q1 := q
				if q == "attrvalues-approx" {
					q1 = "attrvalues"
				}
				old, ok1 := bestOf(step1[cellKey("pqgo-1d", "pqgo", q1, rr[1])])
				nw, ok2 := bestOf(get("pqgo-1d", "pqgo", q, rr[0]))
				if !ok1 || !ok2 {
					continue
				}
				fmt.Fprintf(&b, "| %s | %s | %s | %s | %.1fx | %s | %s |\n", rr[0], q, fmtMS(old.WallMS), fmtMS(nw.WallMS),
					old.WallMS/nw.WallMS, fmtMB(old.PeakRSSKB), fmtMB(nw.PeakRSSKB))
			}
		}
	}

	b.WriteString("\n`attrvalues-approx` against `attrvalues` (users and groups values; tolerance 2%, or 1 under 50; the bound is statistical, about 2.5 sigma, so rare misses are expected; a cell fails only when more than 1% of the values above 1,024 miss):\n\n" +
		"| Range | Cells | Values per cell | Max rel. error (exact >= 50) | Values outside (max per cell) | Users outside (max per cell) | Values > 1,024 | Of those outside (max) | Failed |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, r := range ranges1b {
		var n, vals, out, uout, over, overOut, failed int
		var maxErr float64
		for _, st := range approx {
			if st.Range != r.name {
				continue
			}
			n++
			vals = max(vals, st.Values)
			out = max(out, st.Outside)
			uout = max(uout, st.UsersOutside)
			over = max(over, st.Over1024)
			overOut = max(overOut, st.Over1024Outside)
			maxErr = max(maxErr, st.MaxRelErr)
			if st.approxFails() {
				failed++
			}
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %.2f%% | %d | %d | %d | %d | %d |\n", r.name, n, vals, 100*maxErr, out, uout, over, overOut, failed)
	}

	var loads []float64
	var unquiet []string
	failed := 0
	runs := 0
	for _, c := range order {
		loads = append(loads, c.Loads...)
		runs += len(c.Loads)
		if c.Err != "" || len(c.Runs) < cellRuns {
			failed++
			fmt.Fprintf(&b, "\nFAILED: %s / %s / %s / %s: %s\n", c.Layout, c.Source, c.Query, c.Range, c.Err)
		}
		var over []string
		for i, l := range c.Loads {
			if l > quietLoad {
				over = append(over, fmt.Sprintf("run %d %.2f", i+1, l))
			}
		}
		if len(over) > 0 {
			unquiet = append(unquiet, fmt.Sprintf("%s/%s/%s/%s (%s)", c.Layout, c.Source, c.Query, c.Range, strings.Join(over, ", ")))
		}
	}
	lo, hi := minMax(loads)
	fmt.Fprintf(&b, "\nFailed cells: %d of %d (%d runs). Load average (1 min) at run start: %.2f to %.2f. Runs that started above %.1f (the gate gave up): %d cells",
		failed, len(order), runs, lo, hi, quietLoad, len(unquiet))
	if len(unquiet) > 0 {
		fmt.Fprintf(&b, ": %s", strings.Join(unquiet, "; "))
	}
	b.WriteString(".\n")
	return b.String()
}
