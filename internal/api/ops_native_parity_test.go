// The parity harness for the analytics tools: each tool's native answer
// checked against the answer the SQL it ran before gave over the same
// files, frozen in testdata/<Test>.golden.json (oldTable). The SQL stays
// as the goldens' key and record; it is no longer run.

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/reporting/sqlbind"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// The analytics tools' SQL before the native engine answered them, whose
// answers the goldens keep to check the native answers against
// (TestToolsMatchTheirOldSQL).

// oldViewsOverviewSQL is views_overview's SQL; kind is its kind filter
// (` AND kind = :kind`) or "".
func oldViewsOverviewSQL(kind string) string {
	return `WITH v AS (
  SELECT day, count(DISTINCT actor_id) AS visitors, count(*) AS views
  FROM views WHERE project_id = :project AND day BETWEEN :from AND :to` + kind + `
  GROUP BY day, kind
), vd AS (
  SELECT day, SUM(visitors) AS visitors, SUM(views) AS views FROM v GROUP BY day
), sd AS (
  SELECT day, count(*) AS sessions, count(*) FILTER (WHERE views = 1) AS bounces,
         COALESCE(SUM(duration_sec), 0) AS duration_sec
  FROM sessions WHERE project_id = :project AND day BETWEEN :from AND :to` + kind + `
  GROUP BY day
)
SELECT vd.day, vd.visitors, vd.views, COALESCE(sd.sessions, 0) AS sessions,
       COALESCE(sd.bounces, 0) AS bounces, COALESCE(sd.duration_sec, 0) AS duration_sec,
       ROUND(COALESCE(sd.bounces, 0)::DOUBLE / greatest(COALESCE(sd.sessions, 0), 1), 3) AS bounce_rate,
       CAST(COALESCE(sd.duration_sec, 0) // greatest(COALESCE(sd.sessions, 0), 1) AS BIGINT) AS avg_session_sec
FROM vd LEFT JOIN sd ON sd.day = vd.day
ORDER BY vd.day`
}

// overviewPerDaySQL is the visitors views_overview answers without a kind
// since the native engine (G15): distinct per day, not per day and kind.
const overviewPerDaySQL = `SELECT day, count(DISTINCT actor_id) FROM views
WHERE project_id = :project AND day BETWEEN :from AND :to GROUP BY day ORDER BY day`

// oldViewsDimension is views_breakdown's old dimension: its columns, each
// with its key expression, and the views it leaves out.
type oldViewsDimension struct {
	cols  []string
	exprs []string
	where string
}

func oldOrEmpty(col string) string { return "COALESCE(" + col + ", '')" }

var oldViewsDimensions = map[string]oldViewsDimension{
	"kinds":     {cols: []string{"kind"}, exprs: []string{oldOrEmpty("kind")}},
	"paths":     {cols: []string{"path"}, exprs: []string{oldOrEmpty("path")}},
	"hosts":     {cols: []string{"host"}, exprs: []string{oldOrEmpty("host")}},
	"referrers": {cols: []string{"source"}, exprs: []string{oldOrEmpty("referrer_source")}},
	"utm": {cols: []string{"utm_source", "utm_medium", "utm_campaign"},
		exprs: []string{oldOrEmpty("utm_source"), oldOrEmpty("utm_medium"), oldOrEmpty("utm_campaign")},
		where: "(utm_source <> '' OR utm_medium <> '' OR utm_campaign <> '')"},
	"countries":    {cols: []string{"country"}, exprs: []string{oldOrEmpty("country")}},
	"platforms":    {cols: []string{"platform"}, exprs: []string{oldOrEmpty("platform")}},
	"os":           {cols: []string{"os", "os_version"}, exprs: []string{oldOrEmpty("os"), oldOrEmpty("os_version")}},
	"browsers":     {cols: []string{"browser", "browser_version"}, exprs: []string{oldOrEmpty("browser"), oldOrEmpty("browser_version")}},
	"app_versions": {cols: []string{"platform", "app_version"}, exprs: []string{oldOrEmpty("platform"), "app_version"}, where: "app_version <> ''"},
	"devices":      {cols: []string{"device", "device_model"}, exprs: []string{oldOrEmpty("device"), oldOrEmpty("device_model")}},
	"displays": {cols: []string{"display"}, exprs: []string{"concat(display_width, 'x', display_height)"},
		where: "display_width > 0 AND display_height > 0"},
	"consent": {cols: []string{"consent"},
		exprs: []string{"CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END"}},
	"locales": {cols: []string{"browser_locale", "app_locale"}, exprs: []string{oldOrEmpty("browser_locale"), oldOrEmpty("app_locale")},
		where: "(browser_locale <> '' OR app_locale <> '')"},
}

// oldViewsBreakdownSQL is views_breakdown's SQL for dim, its limit bound
// as :limit.
func oldViewsBreakdownSQL(dim oldViewsDimension) string {
	keys := make([]string, len(dim.cols))
	pos := make([]string, len(dim.cols))
	for i, c := range dim.cols {
		keys[i] = dim.exprs[i] + " AS " + c
		pos[i] = strconv.Itoa(i + 1)
	}
	where := ""
	if dim.where != "" {
		where = " AND " + dim.where
	}
	cols := strings.Join(dim.cols, ", ")
	return `WITH d AS (
  SELECT ` + strings.Join(keys, ", ") + `, count(DISTINCT actor_id) AS visitors, count(*) AS views
  FROM views WHERE project_id = :project AND day BETWEEN :from AND :to` + where + `
  GROUP BY day, ` + strings.Join(pos, ", ") + `
)
SELECT ` + cols + `, SUM(visitors) AS visitors, SUM(views) AS views
FROM d GROUP BY ` + cols + `
ORDER BY SUM(visitors) DESC, SUM(views) DESC, ` + cols + `
LIMIT :limit`
}

// oldProductEventsSQL is product_events' per-event SQL; cond its event
// filter (` AND event_name = :event`) or "", order its ORDER BY.
func oldProductEventsSQL(cond, order string) string {
	return `SELECT day, event_name, count(*) AS "count", count(DISTINCT actor_id) AS unique_users
FROM product WHERE project_id = :project AND day BETWEEN :from AND :to` + cond + `
GROUP BY day, event_name
ORDER BY ` + order
}

// oldProductTotalsSQL is product_events' totals SQL.
const oldProductTotalsSQL = `SELECT day, count(*) AS total_events, count(DISTINCT actor_id) AS active_users
FROM product WHERE project_id = :project AND day BETWEEN :from AND :to
GROUP BY day
ORDER BY day`

// oldEventNamesSQL is event_names' SQL.
const oldEventNamesSQL = `SELECT family, event_name, count(*) FROM events WHERE family IN ('product','measures') AND project_id = :project AND day BETWEEN :from AND :to AND event_name NOT LIKE '$%' GROUP BY ALL ORDER BY 3 DESC, 2 LIMIT 200`

// filesTable runs SQL on the event files through the host's SQL engine
// (runFiles), scoped to in's project, in mode text: q's :project, :from
// and :to are bound as BIGINT and DATEs, any other :name from vals.
func (h *host) filesTable(ctx context.Context, in rangeIn, q string, vals map[string]sqlbind.Value) (tableOut, error) {
	all := map[string]sqlbind.Value{
		":project": {Type: "BIGINT", Text: strconv.FormatInt(in.ProjectID, 10)},
		":from":    {Type: "DATE", Text: in.From},
		":to":      {Type: "DATE", Text: in.To},
	}
	for k, v := range vals {
		all[k] = v
	}
	bound, args, err := sqlbind.Bind(q, all)
	if err != nil {
		return tableOut{}, err
	}
	res, err := h.runFiles(ctx, sqlengine.Request{SQL: bound, Args: args, MaxRows: h.queryRows, Mode: sqlengine.ModeText, Scope: &in.ProjectID})
	if err != nil {
		return tableOut{}, err
	}
	out := tableOut{Columns: res.Columns, Rows: cellRows(res.Rows), Truncated: res.Truncated}
	if res.Truncated {
		out.Note = fmt.Sprintf("truncated to %d rows; results are PARTIAL — narrow the range or raise the limit", h.queryRows)
	}
	return out, nil
}

// runFiles sends req to the external SQL engine (the test engine for the
// host's files) under CONSOLE_QUERY_TIMEOUT, with a table spec of req's
// scope (every project when nil) and the registry's rules: a failure
// reads as the query tool's sqlError words it.
func (h *host) runFiles(ctx context.Context, req sqlengine.Request) (sqlengine.Result, error) {
	if h.sql == nil {
		return sqlengine.Result{}, invalidf("the event files cannot be queried by this process")
	}
	ctx, cancel := context.WithTimeout(ctx, h.queryTimeout)
	defer cancel()
	events, attrs := h.reg.Snapshot(ctx).RuleLists()
	var projects []int64
	if req.Scope != nil {
		projects = []int64{*req.Scope}
	}
	tables, err := archive.TableSpec(h.eventsDir, projects, archive.Rules{Events: events, Attributes: attrs})
	if err != nil {
		return sqlengine.Result{}, err
	}
	req.Tables = tables
	if req.Mode == "" {
		req.Mode = sqlengine.ModeRows
	}
	res, err := h.sql.Query(ctx, req)
	if err != nil {
		return sqlengine.Result{}, h.sqlError(ctx, err)
	}
	return res, nil
}

// writeParityEdges adds to blog's files the cases the tools' presets
// must agree on with their old SQL:
//   - on 2026-08-22, views carrying only a UTM medium, only a UTM
//     campaign, only an app locale, and a cli view (a kind of its own);
//   - on 2026-08-27, paths whose visitors tie (one each) with different
//     views (/tie-b 3, /tie-c 2, /tie-a 1) under /tie-d (2 visitors);
//     product events whose counts tie within the day (a_evt and b_evt 2
//     each) under c_evt (3); measures whose samples tie with a product
//     event's count (m_evt 2), and a $ measure, left out of event_names.
func writeParityEdges(t *testing.T, h *host) {
	t.Helper()
	at := func(day string, sec int) time.Time {
		d, err := time.Parse(time.DateOnly, day)
		if err != nil {
			t.Fatal(err)
		}
		return d.Add(10*time.Hour + time.Duration(sec)*time.Second)
	}
	view := func(day string, sec int, actor string, e store.Event) store.Event {
		e.ProjectID, e.Family, e.EventName, e.TS, e.ActorID = 1, store.FamilyViews, "$page_view", at(day, sec), actor
		if e.Kind == "" {
			e.Kind = "web"
		}
		e.Platform = "web"
		return e
	}
	evs := []store.Event{
		view("2026-08-22", 0, "e1", store.Event{UTMMedium: "cpc", Path: "/edge"}),
		view("2026-08-22", 10, "e2", store.Event{UTMCampaign: "fall", Path: "/edge"}),
		view("2026-08-22", 20, "e3", store.Event{AppLocale: "fr", Path: "/edge"}),
		view("2026-08-22", 30, "e4", store.Event{Kind: "cli", Path: "/edge"}),
		view("2026-08-27", 0, "t1", store.Event{Path: "/tie-a"}),
		view("2026-08-27", 0, "t4", store.Event{Path: "/tie-d"}),
		view("2026-08-27", 0, "t5", store.Event{Path: "/tie-d"}),
	}
	for i := range 3 {
		evs = append(evs, view("2026-08-27", 60*i, "t2", store.Event{Path: "/tie-b"}))
	}
	for i := range 2 {
		evs = append(evs, view("2026-08-27", 60*i, "t3", store.Event{Path: "/tie-c"}))
	}
	product := func(name, actor string, sec int) store.Event {
		return store.Event{ProjectID: 1, Family: store.FamilyProduct, EventName: name, TS: at("2026-08-27", sec), ActorID: actor, Kind: "web"}
	}
	evs = append(evs, product("b_evt", "p1", 0), product("b_evt", "p2", 1), product("a_evt", "p1", 2), product("a_evt", "p1", 3),
		product("c_evt", "p1", 4), product("c_evt", "p2", 5), product("c_evt", "p3", 6))
	v := 12.5
	for i, name := range []string{"m_evt", "m_evt", "$lcp"} {
		evs = append(evs, store.Event{ProjectID: 1, Family: store.FamilyMeasures, EventName: name, TS: at("2026-08-27", 10+i),
			ActorID: "p1", Kind: "web", Measure: store.MeasureTime, Value: &v})
	}
	writeHostEvents(t, h, evs...)
}

// noSessions is a native engine whose sessions table is empty.
type noSessions struct{ Native }

func (n noSessions) Answer(ctx context.Context, q native.Query, p native.Params, rules archive.Rules) (native.Result, error) {
	if q.Table == "sessions" {
		q.Where = append(slices.Clone(q.Where), native.Filter{Column: "day", Op: "lt", Values: []string{"1970-01-02"}})
	}
	return n.Native.Answer(ctx, q, p, rules)
}

// toolTable is a tool's answer as a table: tableOut's JSON.
func toolTable(t *testing.T, v any) tableOut {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out tableOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// sameNumbers fails unless got's columns and rows are want's, cell for
// cell as text or, with tol above 0, as numbers within tol relative to
// want's (DuckDB prints a DOUBLE 200.0, the engine 200). A tol of 0 holds
// every cell to its text.
func sameNumbers(t *testing.T, name string, got, want tableOut, tol float64) {
	t.Helper()
	if !slices.Equal(got.Columns, want.Columns) {
		t.Errorf("%s: columns %q, want %q", name, got.Columns, want.Columns)
		return
	}
	cell := func(a, b string) bool {
		if a == b {
			return true
		}
		if tol <= 0 {
			return false
		}
		x, err1 := strconv.ParseFloat(a, 64)
		y, err2 := strconv.ParseFloat(b, 64)
		return err1 == nil && err2 == nil && math.Abs(x-y) <= tol*max(1, math.Abs(y))
	}
	if !slices.EqualFunc(got.Rows, want.Rows, func(a, b []string) bool { return slices.EqualFunc(a, b, cell) }) {
		t.Errorf("%s:\n got %q\nwant %q", name, got.Rows, want.Rows)
	}
}

// TestToolsMatchTheirOldSQL runs views_overview, views_breakdown,
// product_events and event_names natively and checks each answer against
// the golden of the tool's old SQL over the same files, on the host's
// events and writeParityEdges'. The one difference allowed is named where
// it is checked: views_overview without kind counts visitors per day, not
// per day and kind (G15).
func TestToolsMatchTheirOldSQL(t *testing.T) {
	h, _ := newTestHost(t)
	writeParityEdges(t, h)
	ctx := context.Background()
	old := func(in rangeIn, q string, vals map[string]sqlbind.Value, got tableOut) tableOut {
		t.Helper()
		return oldTable(t, h, in, q, vals, got, nil)
	}
	// g15 is the overview without kind as the golden of q keeps it (the
	// old SQL's visitors per day and kind), its visitors then taken from
	// the golden of overviewPerDaySQL: the native answer to compare with.
	g15 := func(in rangeIn, q string, got tableOut) (tableOut, bool) {
		t.Helper()
		day := tableOut{Columns: []string{"day", "count(DISTINCT actor_id)"}}
		for _, row := range got.Rows {
			day.Rows = append(day.Rows, row[:2])
		}
		perDay := old(in, overviewPerDaySQL, nil, day)
		saw := false
		perDayVisitors := func(o tableOut) tableOut {
			o = cloneTable(o)
			for i, row := range o.Rows {
				if i < len(perDay.Rows) {
					saw = saw || row[1] != perDay.Rows[i][1]
					row[1] = perDay.Rows[i][1]
				}
			}
			return o
		}
		want := perDayVisitors(oldTable(t, h, in, q, nil, got, perDayVisitors))
		return want, saw
	}
	ranges := []rangeIn{
		{ProjectID: 1, From: "2026-08-01", To: "2026-08-31"},
		{ProjectID: 1, From: "2026-08-20", To: "2026-08-22"},
		{ProjectID: 1, From: "2026-08-27", To: "2026-08-27"},
		{ProjectID: 1, From: "2026-07-01", To: "2026-07-31"}, // no data
		{ProjectID: 1, From: "2026-08-21", To: "2026-08-20"}, // to before from
	}
	kind := func(k string) map[string]sqlbind.Value {
		return map[string]sqlbind.Value{":kind": {Type: "VARCHAR", Text: k}}
	}

	t.Run("views_overview", func(t *testing.T) {
		sawG15 := false
		for _, r := range ranges {
			for _, k := range []string{"", "web", "app", "cli", "tv"} {
				got, err := h.viewsOverview(ctx, overviewIn{rangeIn: r, Kind: k})
				if err != nil {
					t.Fatal(err)
				}
				name := fmt.Sprintf("%s..%s kind %q", r.From, r.To, k)
				var want tableOut
				if k == "" {
					// G15: without kind, the visitors are distinct per day
					// (an actor seen under two kinds on a day counts once).
					var saw bool
					want, saw = g15(r, oldViewsOverviewSQL(""), toolTable(t, got))
					sawG15 = sawG15 || saw
				} else {
					want = old(r, oldViewsOverviewSQL(` AND kind = :kind`), kind(k), toolTable(t, got))
				}
				sameNumbers(t, name, toolTable(t, got), want, 0)
			}
		}
		if !sawG15 {
			t.Error("no day of the fixture shows G15: the allowance checks nothing")
		}
	})

	// A day with views but no session: the files derive a session from
	// every view, so the sessions are taken away, from the native engine's
	// answers and from the old SQL's sessions alike.
	t.Run("views_overview without sessions", func(t *testing.T) {
		nat := h.native
		h.native = noSessions{nat}
		defer func() { h.native = nat }()
		r := ranges[0]
		for _, k := range []string{"", "app"} {
			got, err := h.viewsOverview(ctx, overviewIn{rangeIn: r, Kind: k})
			if err != nil {
				t.Fatal(err)
			}
			cond, vals := "", map[string]sqlbind.Value(nil)
			if k != "" {
				cond, vals = ` AND kind = :kind`, kind(k)
			}
			q := strings.Replace(oldViewsOverviewSQL(cond), "FROM sessions WHERE", "FROM sessions WHERE false AND", 1)
			var want tableOut
			if k == "" { // G15, as above
				want, _ = g15(r, q, toolTable(t, got))
			} else {
				want = old(r, q, vals, toolTable(t, got))
			}
			if len(want.Rows) == 0 || want.Rows[0][3] != "0" {
				t.Fatalf("the old SQL still counts sessions: %q", want.Rows)
			}
			sameNumbers(t, fmt.Sprintf("kind %q without sessions", k), toolTable(t, got), want, 0)
		}
	})

	t.Run("views_breakdown", func(t *testing.T) {
		for name, dim := range oldViewsDimensions {
			for _, r := range ranges {
				for _, limit := range []int{1, 2, 3, 20} {
					got, err := h.viewsBreakdown(ctx, breakdownIn{rangeIn: r, Dimension: name, Limit: limit})
					if err != nil {
						t.Fatal(err)
					}
					want := old(r, oldViewsBreakdownSQL(dim), map[string]sqlbind.Value{":limit": {Type: "BIGINT", Text: strconv.Itoa(limit)}}, toolTable(t, got))
					sameNumbers(t, fmt.Sprintf("%s %s..%s limit %d", name, r.From, r.To, limit), toolTable(t, got), want, 0)
				}
			}
		}
	})

	t.Run("product_events", func(t *testing.T) {
		for _, r := range ranges {
			for _, event := range []string{"", "signup", "c_evt", "nope"} {
				got, err := h.productEvents(ctx, productEventsIn{rangeIn: r, Event: event})
				if err != nil {
					t.Fatal(err)
				}
				name := fmt.Sprintf("%s..%s event %q", r.From, r.To, event)
				if event == "" {
					sameNumbers(t, name+" events", got.Events, old(r, oldProductEventsSQL("", `day, "count" DESC, event_name`), nil, got.Events), 0)
					sameNumbers(t, name+" totals", got.Totals, old(r, oldProductTotalsSQL, nil, got.Totals), 0)
					continue
				}
				sameNumbers(t, name+" events", got.Events, old(r, oldProductEventsSQL(` AND event_name = :event`, `day`),
					map[string]sqlbind.Value{":event": {Type: "VARCHAR", Text: event}}, got.Events), 0)
				if got.Totals.Columns != nil || got.Totals.Rows != nil {
					t.Errorf("%s: totals %v, want none with an event", name, got.Totals)
				}
			}
		}
	})

	t.Run("event_names", func(t *testing.T) {
		for _, r := range ranges {
			got, err := h.eventNames(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			table := tableOut{Columns: []string{"family", "event_name", "count_star()"}}
			for _, e := range got.Events {
				table.Rows = append(table.Rows, []string{e.Family, e.Name, strconv.FormatInt(e.Events, 10)})
			}
			want := eventNamesOut{Events: []eventNameOut{}}
			for _, row := range old(r, oldEventNamesSQL, nil, table).Rows {
				n, _ := strconv.ParseInt(row[2], 10, 64)
				want.Events = append(want.Events, eventNameOut{Family: row[0], Name: row[1], Events: n})
			}
			if !slices.Equal(got.Events, want.Events) {
				t.Errorf("%s..%s:\n got %v\nwant %v", r.From, r.To, got.Events, want.Events)
			}
		}
	})
}

// Under a row cap below their answers the tools say the table is cut, as
// the old SQL did, and event_names refuses a partial list.
func TestNativeToolsUnderTheRowCap(t *testing.T) {
	h, _ := newTestHost(t)
	setGuards(t, h, h.queryTimeout, 2)
	ctx := context.Background()
	blog := rangeIn{ProjectID: 1, From: "2026-08-01", To: "2026-08-31"}
	partial := func(name string, out tableOut) {
		t.Helper()
		if !out.Truncated || !strings.Contains(out.Note, "PARTIAL") || len(out.Rows) != h.queryRows {
			t.Errorf("%s = %+v, want %d rows, truncated, noted", name, out, h.queryRows)
		}
	}
	ov, err := h.viewsOverview(ctx, overviewIn{rangeIn: blog})
	if err != nil {
		t.Fatal(err)
	}
	partial("views_overview", ov)
	if ov.Rows[0][0] != "2026-08-01" || ov.Rows[1][0] != "2026-08-08" {
		t.Errorf("views_overview kept %q, want the earliest days", ov.Rows)
	}
	bd, err := h.viewsBreakdown(ctx, breakdownIn{rangeIn: blog, Dimension: "paths", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	partial("views_breakdown", bd)
	if bd, err = h.viewsBreakdown(ctx, breakdownIn{rangeIn: blog, Dimension: "paths", Limit: 2}); err != nil || bd.Truncated {
		t.Errorf("views_breakdown at its own limit = %+v, %v; want not truncated", bd, err)
	}
	setGuards(t, h, h.queryTimeout, 1)
	pe, err := h.productEvents(ctx, productEventsIn{rangeIn: blog})
	if err != nil {
		t.Fatal(err)
	}
	partial("product_events' events", pe.Events)
	partial("product_events' totals", pe.Totals)
	if _, err := h.eventNames(ctx, blog); err == nil || !strings.Contains(err.Error(), "too many rows") {
		t.Errorf("event_names over the cap = %v, want the refusal", err)
	}
}

// A column a sketch estimated is named in the note.
func TestMarkPartialNamesTheEstimates(t *testing.T) {
	var h host
	out := tableOut{Rows: [][]string{{"1"}}}
	h.markPartial(&out, false, []string{"visitors", "active_users", "visitors"})
	if out.Truncated || out.Note != "active_users, visitors past 1,024 distinct ids are estimated (about ±2%)" {
		t.Errorf("out = %+v", out)
	}
}

// Without the native engine the tools refuse.
func TestNativeToolsWithoutTheEngine(t *testing.T) {
	h, _ := newTestHost(t)
	h.native = nil
	blog := rangeIn{ProjectID: 1, From: "2026-08-01", To: "2026-08-31"}
	if _, err := h.viewsOverview(context.Background(), overviewIn{rangeIn: blog}); err == nil {
		t.Error("views_overview answered without an engine")
	}
	if _, err := h.retention(context.Background(), retentionIn{rangeIn: blog, Actor: "user"}); err == nil {
		t.Error("retention answered without an engine")
	}
	if _, err := h.usage(context.Background(), usageIn{ProjectID: 1}); err == nil {
		t.Error("usage answered without an engine")
	}
}

// CONSOLE_QUERY_ROWS_MAX at 0 (or below) is no cap: the tools answer in
// full, within the engine's 1,000.
func TestNativeToolsWithoutARowCap(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := context.Background()
	blog := rangeIn{ProjectID: 1, From: "2026-08-01", To: "2026-08-31"}
	for _, cap := range []int{0, -1} {
		h.queryRows = cap
		ov, err := h.viewsOverview(ctx, overviewIn{rangeIn: blog})
		if err != nil || ov.Truncated || len(ov.Rows) != 9 {
			t.Errorf("cap %d: views_overview = %+v, %v; want 9 days, whole", cap, ov, err)
		}
		bd, err := h.viewsBreakdown(ctx, breakdownIn{rangeIn: blog, Dimension: "paths", Limit: 2})
		if err != nil || bd.Truncated || len(bd.Rows) != 2 {
			t.Errorf("cap %d: views_breakdown = %+v, %v; want 2 rows, whole", cap, bd, err)
		}
		pe, err := h.productEvents(ctx, productEventsIn{rangeIn: blog})
		if err != nil || pe.Events.Truncated || len(pe.Events.Rows) != 2 || len(pe.Totals.Rows) != 2 {
			t.Errorf("cap %d: product_events = %+v, %v; want 2 rows each, whole", cap, pe, err)
		}
		names, err := h.eventNames(ctx, blog)
		if err != nil || len(names.Events) == 0 {
			t.Errorf("cap %d: event_names = %+v, %v; want the names", cap, names, err)
		}
	}
}
