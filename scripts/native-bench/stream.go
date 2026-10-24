package main

import (
	"fmt"
	"strings"
	"time"
)

// ---- dashboards -stream-md: step 4c, a dashboard loaded through one stream ----

// fair2a are step 4d's 2a cells (the harness without its child poll), the
// fair "before" where they exist: 1M, 30 days, by dashboard and open log.
var fair2a = map[string]float64{
	"views|false": 1610, "views|true": 7680, "product|false": 1190, "product|true": 4200,
}

// dashboardsStreamMD is the cells of a step 4c run (stream: one
// WidgetDataStream; native: per widget, the same binary; duckdb: the old
// SQL on the same files) against before (step 4b's native cells).
func dashboardsStreamMD(all, before []dashCell, start time.Time, passes int) string {
	var sb strings.Builder
	kind := func(c dashCell) string {
		if c.Mode == "stream" {
			return "stream"
		}
		return c.Engine
	}
	key := func(vol, kind, dir string, days int, log bool) string {
		return fmt.Sprintf("%s|%s|%s|%d|%v", vol, kind, dir, days, log)
	}
	cells, old := map[string]dashCell{}, map[string]dashCell{}
	var loads []float64
	unquiet := 0
	for _, c := range all {
		cells[key(c.Volume, kind(c), c.Dashboard, c.Days, c.OpenLog)] = c
		loads = append(loads, c.GateLoad)
		if !c.Quiet {
			unquiet++
		}
	}
	for _, c := range before {
		old[key(c.Volume, c.Engine, c.Dashboard, c.Days, c.OpenLog)] = c
	}
	lo, hi := minMax(loads)
	fmt.Fprintf(&sb, "\n## Step 4c: the dashboards loaded through one stream\n\n"+
		"Started %s. One fresh process a cell (`native-bench dashboards -engines stream,native,duckdb`, "+
		"`TestMeasureSystemDashboard`, GOMAXPROCS=2), a first load then %d more, \"best\" the fastest. "+
		"Stream = the dashboard's query widgets as one `reporting.Service.WidgetDataStream` (`MEASURE_STREAM=1`), "+
		"as the console loads a dashboard since the stream route: the misses (the reporting cache is off) answered "+
		"together by `native.Engine.AnswerMany`; \"first widget\" is the first emit, \"last\" the call's return. "+
		"Per widget = the same binary's per-widget `WidgetData`, every widget at once, as before the stream "+
		"(first widget = the fastest). DuckDB = the old SQL through `Service.Preview` on the same files. "+
		"Before = step 4b's native best at T = 32,768 (the harness then polled /proc, 14 to 16%% of a native "+
		"cell's wall), but step 4d's 2a cells (no poll) for Views and Product at 30 days and 1M (marked †). "+
		"CPU is the process's (user + system) over the best warm load; peak is VmHWM (DuckDB: Go + child). "+
		"\"+ log\" rows add an open log of today (2026-10-01) to a range ending today. "+
		"‡ marks a cell where widgets were refused in every load (per widget / stream), so their time is a refusal's. "+
		"Before every cell the bench waits until the 1-minute load is at most %.1f.\n",
		start.UTC().Format(time.RFC3339), passes, quietLoad)
	type row struct {
		dir, title string
		days       int
		log        bool
	}
	var rows []row
	for _, d := range dashboards3f {
		for _, days := range []int{7, 30, 90, 365} {
			rows = append(rows, row{d.dir, d.title, days, false})
			if days == 30 && (d.dir == "views" || d.dir == "product") {
				rows = append(rows, row{d.dir, d.title, days, true})
			}
		}
	}
	for _, v := range volumes {
		has := false
		for _, c := range all {
			has = has || c.Volume == v.name
		}
		if !has {
			continue
		}
		fmt.Fprintf(&sb, "\n### Step 4c, volume %s (%d events a month)\n\n", v.name, v.perMonth)
		sb.WriteString("| Dashboard | Range | Before best | Per widget first / best | Stream first load: first widget / last | Stream best: first widget / last | Change (stream best / before) | CPU per load: per widget / stream | DuckDB first / best | Peak: before / per widget / stream | DuckDB peak | Slowest stream widget (best) | Load at start |\n")
		sb.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for _, r := range rows {
			s, ok := cells[key(v.name, "stream", r.dir, r.days, r.log)]
			if !ok {
				continue
			}
			n, okN := cells[key(v.name, "native", r.dir, r.days, r.log)]
			q, okQ := cells[key(v.name, "duckdb", r.dir, r.days, r.log)]
			o, okO := old[key(v.name, "native", r.dir, r.days, r.log)]
			rng := fmt.Sprintf("%d d", r.days)
			if r.log {
				rng += " + log"
			}
			was, change, oldPeak := "–", "–", "–"
			wasMS := 0.0
			// Retention's warm loads reuse the identities the engine keeps,
			// so its first load is the one compared.
			first := r.dir == "retention"
			if okO {
				wasMS, oldPeak = o.BestMS, mb(o.SelfPeak)
				if first {
					wasMS = o.FirstMS
				}
				was = dashMS(wasMS)
			}
			if f, ok := fair2a[fmt.Sprintf("%s|%v", r.dir, r.log)]; ok && v.name == "1m" && r.days == 30 {
				wasMS, was = f, dashMS(f)+" †"
			}
			if wasMS > 0 && first {
				was += " (first load)"
				change = fmt.Sprintf("%.2fx (first load)", s.FirstMS/wasMS)
			} else if wasMS > 0 {
				change = fmt.Sprintf("%.2fx", s.BestMS/wasMS)
			}
			per, cpu, duck, duckPeak, nPeak := "–", "–", "–", "–", "–"
			best := func(c dashCell) float64 {
				for i, ms := range c.WarmMS {
					if ms == c.BestMS && i < len(c.WarmCPU) {
						return c.WarmCPU[i]
					}
				}
				return 0
			}
			if okN {
				per = dashMS(n.FirstMS) + " / " + dashMS(n.BestMS)
				nPeak = mb(n.SelfPeak)
				cpu = dashMS(best(n)) + " / " + dashMS(best(s))
			} else {
				cpu = "– / " + dashMS(best(s))
			}
			if okQ {
				duck = dashMS(q.FirstMS) + " / " + dashMS(q.BestMS)
				duckPeak = mb(q.SelfPeak + q.ChildPeak)
			}
			load := fmt.Sprintf("%.2f", s.GateLoad)
			if !s.Quiet {
				load += " (unquiet)"
			}
			title := r.title
			if len(s.Errors)+len(n.Errors) > 0 {
				title += fmt.Sprintf(" ‡ %d / %d refused", len(n.Errors), len(s.Errors))
			}
			fmt.Fprintf(&sb, "| %s (%d) | %s | %s | %s | %s / %s | %s / %s | %s | %s | %s | %s / %s / %s | %s | %s | %s |\n",
				title, s.Widgets, rng, was, per, dashMS(s.FirstOne), dashMS(s.FirstMS), dashMS(s.BestOne), dashMS(s.BestMS),
				change, cpu, duck, oldPeak, nPeak, mb(s.SelfPeak), duckPeak, slowest(s), load)
		}
	}
	fmt.Fprintf(&sb, "\nLoad average (1 min) at cell start: %.2f to %.2f; cells that started above %.1f (the gate gave up): %d of %d.\n",
		lo, hi, quietLoad, unquiet, len(all))
	return sb.String()
}
