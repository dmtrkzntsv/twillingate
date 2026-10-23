package reporting

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
)

// The parity harness holds each system widget the native engine answers
// (a .query) to the SQL it replaced, kept under testdata/system-sql as
// <dashboard dir>/<widget>.sql: on the system fixture, for parityPresets,
// both must return the same column names in the same order and the same
// rows in the same order, numbers compared as numbers. A difference is
// accepted only where parityAllowances names it, with its gap id.

// parityPresets are the presets the harness compares on.
var parityPresets = []string{"7d", "30d", "90d"}

// allowKind is one way a native answer may differ from the old SQL's.
type allowKind int

const (
	// blankStat (G2): in Columns, the native cell is blank where the
	// SQL's is 0 or blank, and the SQL may return no row where native
	// returns one.
	blankStat allowKind = iota + 1
	// fillZero (G11): native has extra rows whose Columns (the metrics)
	// are all 0 and whose other columns (the key: a day, a series) match
	// no SQL row's: a missing day or series filled, never a row replaced.
	fillZero
	// approx (G14): Columns are within ±2% of the SQL's.
	approx
	// reshape (G6-G9 only): the widget changed; nothing is compared.
	reshape
	// columnsOnly: only the column names and their order are compared.
	columnsOnly
)

// reshape and columnsOnly are for the reshapes (G6-G9) alone. A
// difference of another gap (G10's labels, G13's series cap, G15's
// daily counts) needs a kind of its own, as narrow as the ones above,
// rather than either of these.

func (k allowKind) String() string {
	switch k {
	case blankStat:
		return "blank-stat"
	case fillZero:
		return "fill-zero"
	case approx:
		return "approx"
	case reshape:
		return "reshape"
	case columnsOnly:
		return "columns-only"
	}
	return fmt.Sprintf("allowKind(%d)", int(k))
}

// allowance is one accepted difference of one widget: the gap it is
// (G2, G6-G11, G13-G15), its kind, the columns it covers (by the
// widget's column names, after rename) and why.
type allowance struct {
	Gap     string
	Kind    allowKind
	Columns []string // blankStat: nil covers every column; fillZero and approx: required
	Reason  string   // required for reshape and columnsOnly
}

// parityGaps are the gap ids an allowance may name.
var parityGaps = []string{"G2", "G6", "G7", "G8", "G9", "G10", "G11", "G13", "G14", "G15"}

// parityAllowances names, per system widget ("<dashboard dir>/<widget>"),
// the differences from its old SQL the harness accepts. An allowance no
// preset needs fails the harness, so none outlives its reason.
//
//	"views/visitors": {{Gap: "G2", Kind: blankStat, Columns: []string{"value"}}},
var parityAllowances = map[string][]allowance{}

// newSystemWidgets names the system query widgets that have no old SQL
// to compare with (one split from another, say), each with the reason.
var newSystemWidgets = map[string]string{}

// TestSystemWidgetParity runs the harness on this release's system
// widgets and the old SQL in testdata/system-sql.
func TestSystemWidgetParity(t *testing.T) {
	system, err := fs.Sub(systemFS, "system")
	if err != nil {
		t.Fatal(err)
	}
	cfg := parityConfig{system: system, oldSQL: os.DirFS("testdata/system-sql"), allow: parityAllowances, news: newSystemWidgets}
	for _, p := range runParity(t, cfg, newSystemFixture) {
		t.Error(p)
	}
}

// parityConfig is one run of the harness: the system definition, the
// old SQL beside it, and the accepted differences and new widgets.
type parityConfig struct {
	system, oldSQL fs.FS
	allow          map[string][]allowance
	news           map[string]string
}

// parityWidget is one system query widget to compare with its old SQL.
type parityWidget struct {
	key, name string
	dashboard int64
	sql       string
}

// runParity returns every problem cfg's run finds: a difference no
// allowance accepts, an allowance no preset needed, a system query widget
// with neither old SQL nor a place in news, and an entry of oldSQL,
// allow or news that names no system query widget. fixture is called only
// when there is a widget to compare, with cfg.system synced in.
func runParity(t *testing.T, cfg parityConfig, fixture func(*testing.T) systemFixture) []string {
	t.Helper()
	ws, problems := parityWidgets(t, cfg)
	if len(ws) == 0 {
		return problems
	}
	f := fixture(t)
	ctx := context.Background()
	if err := migrateFrom(ctx, f.st, f.files, cfg.system, Manifest()); err != nil {
		t.Fatalf("sync %v", err)
	}
	for _, w := range ws {
		allows := cfg.allow[w.key]
		used := make([]bool, len(allows))
		id := systemWidgetOf(t, f.svc, w.dashboard, w.name)
		for _, preset := range parityPresets {
			from, to := presetDates(preset, f.today)
			got, err := f.svc.WidgetData(ctx, DataRequest{WidgetID: id, ProjectID: f.project, From: from, To: to})
			if err != nil {
				t.Fatalf("%s %s: native: %v", w.key, preset, err)
			}
			nat, ok := got.Data.(readsql.Result)
			if !ok {
				t.Fatalf("%s %s: native returned %T", w.key, preset, got.Data)
			}
			old, err := f.svc.sources["sql"].Load(ctx, w.sql, Params{ProjectID: f.project, From: from, To: to})
			if err != nil {
				t.Fatalf("%s %s: old SQL: %v", w.key, preset, err)
			}
			oldRes, ok := old.(readsql.Result)
			if !ok {
				t.Fatalf("%s %s: old SQL returned %T", w.key, preset, old)
			}
			diffs, u := compareParity(oldRes, nat, allows)
			for _, d := range diffs {
				problems = append(problems, fmt.Sprintf("%s %s: %s", w.key, preset, d))
			}
			for i := range used {
				used[i] = used[i] || u[i]
			}
		}
		for i, a := range allows {
			if !used[i] {
				problems = append(problems, fmt.Sprintf("%s: allowance %s %s is needed on no preset", w.key, a.Gap, a.Kind))
			}
		}
	}
	return problems
}

// parityWidgets lists cfg.system's query widgets that have old SQL, and
// the problems with cfg found without running anything: a query widget
// with no old SQL that news does not name, a file under oldSQL other than
// a query widget's <dir>/<name>.sql, a news entry for a widget that is
// no query widget or has old SQL, and an allowance for no widget compared
// or that is malformed.
func parityWidgets(t *testing.T, cfg parityConfig) ([]parityWidget, []string) {
	t.Helper()
	old := map[string]string{}
	err := fs.WalkDir(cfg.oldSQL, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(cfg.oldSQL, p)
		if err != nil {
			return err
		}
		old[p] = string(b)
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}

	entries, err := fs.ReadDir(cfg.system, ".")
	if err != nil {
		t.Fatal(err)
	}
	var ws []parityWidget
	var problems []string
	queries := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		fd, err := LoadDashboard(cfg.system, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, fw := range fd.Widgets {
			if fw.SourceType != "query" {
				continue
			}
			key := e.Name() + "/" + fw.Name
			queries[key] = true
			sql, ok := old[key+".sql"]
			_, isNew := cfg.news[key]
			switch {
			case ok && isNew:
				problems = append(problems, fmt.Sprintf("%s: listed as new, but has old SQL to compare with", key))
			case ok:
				ws = append(ws, parityWidget{key: key, name: fw.Name, dashboard: fd.ID, sql: sql})
			case !isNew:
				problems = append(problems, fmt.Sprintf("%s: a query widget with no testdata/system-sql/%s.sql and not listed as new", key, key))
			}
		}
	}
	for p := range old {
		if key, ok := strings.CutSuffix(p, ".sql"); !ok || !queries[key] {
			problems = append(problems, fmt.Sprintf("testdata/system-sql/%s: no system query widget %s", p, strings.TrimSuffix(p, ".sql")))
		}
	}
	for key, reason := range cfg.news {
		if !queries[key] {
			problems = append(problems, fmt.Sprintf("%s: listed as new, but is no system query widget", key))
		}
		if strings.TrimSpace(reason) == "" {
			problems = append(problems, fmt.Sprintf("%s: listed as new with no reason", key))
		}
	}
	compared := map[string]bool{}
	for _, w := range ws {
		compared[w.key] = true
	}
	for key, allows := range cfg.allow {
		if !compared[key] {
			problems = append(problems, fmt.Sprintf("%s: has allowances, but is no system query widget with old SQL", key))
		}
		for i, a := range allows {
			if p := a.check(allows[:i]); p != "" {
				problems = append(problems, fmt.Sprintf("%s: allowance %s %s: %s", key, a.Gap, a.Kind, p))
			}
		}
	}
	sort.Strings(problems)
	return ws, problems
}

// reshapeGaps are the gaps a reshape or columnsOnly allowance may name.
var reshapeGaps = []string{"G6", "G7", "G8", "G9"}

// check returns what is wrong with a, given the widget's allowances
// before it (earlier), or "".
func (a allowance) check(earlier []allowance) string {
	if !slices.Contains(parityGaps, a.Gap) {
		return "unknown gap; there are " + strings.Join(parityGaps, ", ")
	}
	if slices.ContainsFunc(earlier, func(e allowance) bool { return e.Kind == a.Kind }) {
		return "a second allowance of this kind on the widget; merge them"
	}
	switch a.Kind {
	case blankStat:
	case fillZero, approx:
		if len(a.Columns) == 0 {
			return "names no columns"
		}
	case reshape, columnsOnly:
		if !slices.Contains(reshapeGaps, a.Gap) {
			return "only the reshapes (" + strings.Join(reshapeGaps, ", ") + ") may skip rows; give this gap a narrow kind of its own"
		}
		if strings.TrimSpace(a.Reason) == "" {
			return "gives no reason"
		}
	default:
		return "unknown kind"
	}
	return ""
}

// compareParity compares a widget's native answer (got) with its old
// SQL's (want) under allows: it returns the differences no allowance
// accepts, and for each of allows whether it accepted one here (reshape
// and columnsOnly always do).
func compareParity(want, got readsql.Result, allows []allowance) ([]string, []bool) {
	used := make([]bool, len(allows))
	find := func(k allowKind) int {
		return slices.IndexFunc(allows, func(a allowance) bool { return a.Kind == k })
	}
	if i := find(reshape); i >= 0 {
		used[i] = true
		return nil, used
	}
	if !slices.Equal(want.Columns, got.Columns) {
		return []string{fmt.Sprintf("columns %q, the old SQL's %q", got.Columns, want.Columns)}, used
	}
	if i := find(columnsOnly); i >= 0 {
		used[i] = true
		return nil, used
	}
	// covers says whether allowance i covers column c.
	covers := func(i, c int) bool {
		if i < 0 {
			return false
		}
		cols := allows[i].Columns
		return cols == nil || slices.Contains(cols, want.Columns[c])
	}
	blank, fill, near := find(blankStat), find(fillZero), find(approx)

	wantRows, gotRows := want.Rows, got.Rows
	if blank >= 0 && len(wantRows) == 0 && len(gotRows) == 1 {
		wantRows = [][]string{make([]string, len(want.Columns))}
		used[blank] = true
	}
	if fill >= 0 {
		kept := gotRows[:0:0]
		for _, r := range gotRows {
			zero := true
			for c := range r {
				if covers(fill, c) && r[c] != "0" {
					zero = false
				}
			}
			// The key is every column fill does not cover.
			sameKey := func(w []string) bool {
				for c := range r {
					if !covers(fill, c) && (c >= len(w) || !sameNumber(w[c], r[c])) {
						return false
					}
				}
				return true
			}
			if zero && !slices.ContainsFunc(wantRows, sameKey) {
				used[fill] = true
				continue
			}
			kept = append(kept, r)
		}
		gotRows = kept
	}
	if len(wantRows) != len(gotRows) {
		return []string{fmt.Sprintf("%d rows, the old SQL's %d:\n native %q\n    sql %q", len(gotRows), len(wantRows), gotRows, wantRows)}, used
	}
	var diffs []string
	for i := range wantRows {
		w, g := wantRows[i], gotRows[i]
		if len(w) != len(g) {
			diffs = append(diffs, fmt.Sprintf("row %d: %d cells, the old SQL's %d", i, len(g), len(w)))
			continue
		}
		for c := range w {
			switch {
			case sameNumber(w[c], g[c]):
			case covers(blank, c) && g[c] == "" && w[c] == "0":
				used[blank] = true
			case covers(near, c) && within(w[c], g[c], 0.02):
				used[near] = true
			default:
				diffs = append(diffs, fmt.Sprintf("row %d %s: %q, the old SQL's %q\n native %q\n    sql %q", i, want.Columns[c], g[c], w[c], g, w))
			}
		}
	}
	return diffs, used
}

// sameNumber says whether two cells are equal: as strings, or as numbers
// within a rounding error (sums of doubles depend on their order), as
// the native engine's own parity tests compare them.
func sameNumber(a, b string) bool {
	if a == b {
		return true
	}
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	return err1 == nil && err2 == nil && math.Abs(x-y) <= 1e-9*math.Max(1, math.Abs(y))
}

// within says whether got is a number within frac of want, relative to
// want: ±2% of 3 is ±0.06, not ±0.02 of a floor of 1. Only a want of 0,
// which has no relative band, takes frac as an absolute one.
func within(want, got string, frac float64) bool {
	x, err1 := strconv.ParseFloat(want, 64)
	y, err2 := strconv.ParseFloat(got, 64)
	if err1 != nil || err2 != nil {
		return false
	}
	if x == 0 {
		return math.Abs(y) <= frac
	}
	return math.Abs(x-y) <= frac*math.Abs(x)
}

// fakeSystem is a system definition of one converted widget,
// views/count, a stat of the views in the range answered by the native
// engine.
var fakeSystem = mapFS(map[string]string{
	"views/dashboard.json": `{"id": 1, "title": "Views", "group_title": "Reports", "range": "7d", "sidebar": true, "project_tab": true, "layout": [{"widget": "count"}]}`,
	"views/count.json":     `{"component": "stat", "title": "Views", "props": {"format": "number"}}`,
	"views/count.query":    `{"query":{"kind":"stat","table":"views","metrics":[{"op":"count","as":"value"}]}}`,
})

// countSQL is views/count as the old SQL would have it, with value
// standing for the expression it returns.
func countSQL(value string) string {
	return `SELECT ` + value + ` FROM views WHERE project_id = :project AND day BETWEEN :from AND :to`
}

// TestParityHarnessOnAFakeWidget proves the harness on fakeSystem: it
// passes the same answer, finds a different one, honours an approx
// allowance and refuses one no preset needs, and checks that every query
// widget has old SQL or is listed as new.
func TestParityHarnessOnAFakeWidget(t *testing.T) {
	// Built once, on the parent test, the first time a case needs it:
	// a subtest's cleanup would close it under the next.
	var f *systemFixture
	fixture := func(*testing.T) systemFixture {
		if f == nil {
			nf := newSystemFixture(t)
			f = &nf
		}
		return *f
	}
	old := func(sql string) fs.FS { return mapFS(map[string]string{"views/count.sql": sql}) }
	approx2 := map[string][]allowance{"views/count": {{Gap: "G14", Kind: approx, Columns: []string{"value"}}}}
	cases := []struct {
		name   string
		cfg    parityConfig
		wantIn []string // each a substring of some problem; none: no problems
	}{
		{"same answer", parityConfig{oldSQL: old(countSQL("count(*) AS value"))}, nil},
		{"different value", parityConfig{oldSQL: old(countSQL("count(*) * 2 AS value"))},
			[]string{"views/count 7d: row 0 value", "views/count 30d: row 0 value", "views/count 90d: row 0 value"}},
		{"different column", parityConfig{oldSQL: old(countSQL("count(*) AS total"))},
			[]string{`views/count 7d: columns ["value"], the old SQL's ["total"]`}},
		{"within 2% without approx", parityConfig{oldSQL: old(countSQL("count(*) * 1.01 AS value"))},
			[]string{"views/count 7d: row 0 value"}},
		{"within 2% with approx", parityConfig{oldSQL: old(countSQL("count(*) * 1.01 AS value")), allow: approx2}, nil},
		{"past 2% with approx", parityConfig{oldSQL: old(countSQL("count(*) * 1.05 AS value")), allow: approx2},
			[]string{"views/count 7d: row 0 value"}},
		{"approx never needed", parityConfig{oldSQL: old(countSQL("count(*) AS value")), allow: approx2},
			[]string{"views/count: allowance G14 approx is needed on no preset"}},
		{"no old SQL", parityConfig{oldSQL: mapFS(nil)},
			[]string{"views/count: a query widget with no testdata/system-sql/views/count.sql and not listed as new"}},
		{"no old SQL, listed as new", parityConfig{oldSQL: mapFS(nil), news: map[string]string{"views/count": "split from views/old"}}, nil},
		{"new with old SQL", parityConfig{oldSQL: old(countSQL("count(*) AS value")), news: map[string]string{"views/count": "split"}},
			[]string{"views/count: listed as new, but has old SQL"}},
		{"old SQL of no widget", parityConfig{oldSQL: mapFS(map[string]string{"views/count.sql": countSQL("count(*) AS value"), "views/gone.sql": "SELECT 1"})},
			[]string{"testdata/system-sql/views/gone.sql: no system query widget views/gone"}},
		{"allowance of no widget", parityConfig{oldSQL: mapFS(nil), news: map[string]string{"views/count": "new"},
			allow: map[string][]allowance{"views/gone": {{Gap: "G2", Kind: blankStat}}}},
			[]string{"views/gone: has allowances, but is no system query widget with old SQL"}},
		{"malformed allowance", parityConfig{oldSQL: old(countSQL("count(*) AS value")),
			allow: map[string][]allowance{"views/count": {{Gap: "G99", Kind: columnsOnly, Reason: "x"}, {Gap: "G6", Kind: reshape}}}},
			[]string{"allowance G99 columns-only: unknown gap", "allowance G6 reshape: gives no reason"}},
		{"reshape or columns only off the reshapes", parityConfig{oldSQL: old(countSQL("count(*) AS value")),
			allow: map[string][]allowance{"views/count": {{Gap: "G10", Kind: reshape, Reason: "labels"}, {Gap: "G13", Kind: columnsOnly, Reason: "50 series"}}}},
			[]string{"allowance G10 reshape: only the reshapes", "allowance G13 columns-only: only the reshapes"}},
		{"two allowances of a kind", parityConfig{oldSQL: old(countSQL("count(*) * 1.01 AS value")),
			allow: map[string][]allowance{"views/count": {{Gap: "G14", Kind: approx, Columns: []string{"value"}}, {Gap: "G14", Kind: approx, Columns: []string{"previous"}}}}},
			[]string{"allowance G14 approx: a second allowance of this kind"}},
		// A stat's one row has no key, so it always matches the SQL's:
		// fill-zero never stands in for a different value.
		{"fill-zero on a replaced row", parityConfig{oldSQL: old(countSQL("count(*) * 2 AS value")),
			allow: map[string][]allowance{"views/count": {{Gap: "G11", Kind: fillZero, Columns: []string{"value"}}}}},
			[]string{"views/count 7d: row 0 value", "views/count: allowance G11 fill-zero is needed on no preset"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.cfg.system = fakeSystem
			problems := runParity(t, c.cfg, fixture)
			if len(c.wantIn) == 0 && len(problems) > 0 {
				t.Fatalf("problems:\n%s", strings.Join(problems, "\n"))
			}
			for _, w := range c.wantIn {
				if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, w) }) {
					t.Errorf("no problem says %q; problems:\n%s", w, strings.Join(problems, "\n"))
				}
			}
		})
	}
}

// TestCompareParityAllowances feeds compareParity pairs for the kinds
// the fake widget cannot show: each kind accepts its own difference
// only, on its own columns.
func TestCompareParityAllowances(t *testing.T) {
	res := func(cols []string, rows ...[]string) readsql.Result {
		return readsql.Result{Columns: cols, Rows: rows}
	}
	stat := []string{"value", "previous"}
	trend := []string{"x", "y"}
	blank := allowance{Gap: "G2", Kind: blankStat}
	blankValue := allowance{Gap: "G2", Kind: blankStat, Columns: []string{"value"}}
	fill := allowance{Gap: "G11", Kind: fillZero, Columns: []string{"y"}}
	cases := []struct {
		name      string
		want, got readsql.Result
		allow     []allowance
		diffs     int
		used      []bool
	}{
		{"floats in their tail", res(stat, []string{"0.30000000000000004", "1"}), res(stat, []string{"0.3", "1.0"}), nil, 0, []bool{}},
		{"blank for 0", res(stat, []string{"0", "0"}), res(stat, []string{"", ""}), []allowance{blank}, 0, []bool{true}},
		{"blank for 0 unallowed", res(stat, []string{"0", "0"}), res(stat, []string{"", ""}), nil, 2, []bool{}},
		{"blank for no row", res(stat), res(stat, []string{"", ""}), []allowance{blank}, 0, []bool{true}},
		{"blank for 0 on another column", res(stat, []string{"0", "0"}), res(stat, []string{"", ""}), []allowance{blankValue}, 1, []bool{true}},
		{"blank for a number", res(stat, []string{"3", "0"}), res(stat, []string{"", ""}), []allowance{blank}, 1, []bool{true}},
		{"blank not needed", res(stat, []string{"3", "1"}), res(stat, []string{"3", "1"}), []allowance{blank}, 0, []bool{false}},
		{"zero days filled", res(trend, []string{"2026-10-01", "4"}),
			res(trend, []string{"2026-09-30", "0"}, []string{"2026-10-01", "4"}, []string{"2026-10-02", "0"}), []allowance{fill}, 0, []bool{true}},
		{"a filled day is not zero", res(trend, []string{"2026-10-01", "4"}),
			res(trend, []string{"2026-09-30", "1"}, []string{"2026-10-01", "4"}), []allowance{fill}, 1, []bool{false}},
		{"a replaced day", res(trend, []string{"2026-10-01", "4"}),
			res(trend, []string{"2026-10-01", "0"}), []allowance{fill}, 1, []bool{false}},
		{"a replaced day among filled ones", res(trend, []string{"2026-10-01", "4"}),
			res(trend, []string{"2026-09-30", "0"}, []string{"2026-10-01", "0"}), []allowance{fill}, 1, []bool{true}},
		{"a filled series", res([]string{"x", "series", "y"}, []string{"2026-10-01", "a", "4"}),
			res([]string{"x", "series", "y"}, []string{"2026-10-01", "a", "4"}, []string{"2026-10-01", "b", "0"}), []allowance{fill}, 0, []bool{true}},
		{"approx is relative", res(stat, []string{"0.5", "1"}), res(stat, []string{"0.515", "1"}),
			[]allowance{{Gap: "G14", Kind: approx, Columns: []string{"value"}}}, 1, []bool{false}},
		{"approx within 2%", res(stat, []string{"0.5", "1"}), res(stat, []string{"0.509", "1"}),
			[]allowance{{Gap: "G14", Kind: approx, Columns: []string{"value"}}}, 0, []bool{true}},
		{"approx at 0", res(stat, []string{"0", "1"}), res(stat, []string{"0.01", "1"}),
			[]allowance{{Gap: "G14", Kind: approx, Columns: []string{"value"}}}, 0, []bool{true}},
		{"columns only", res(stat, []string{"1", "2"}), res(stat, []string{"5", "6"}), []allowance{{Gap: "G7", Kind: columnsOnly, Reason: "r"}}, 0, []bool{true}},
		{"columns only, other columns", res(stat), res(trend), []allowance{{Gap: "G7", Kind: columnsOnly, Reason: "r"}}, 1, []bool{false}},
		{"reshape", res(stat), res(trend, []string{"a", "b"}), []allowance{{Gap: "G6", Kind: reshape, Reason: "r"}}, 0, []bool{true}},
		{"approx on another column", res(stat, []string{"100", "100"}), res(stat, []string{"100", "101"}),
			[]allowance{{Gap: "G14", Kind: approx, Columns: []string{"value"}}}, 1, []bool{false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			diffs, used := compareParity(c.want, c.got, c.allow)
			if len(diffs) != c.diffs {
				t.Errorf("%d differences, want %d: %q", len(diffs), c.diffs, diffs)
			}
			if !slices.Equal(used, c.used) {
				t.Errorf("used %v, want %v", used, c.used)
			}
		})
	}
}

// mapFS is an fs.FS holding files, path to content.
func mapFS(files map[string]string) fstest.MapFS {
	out := fstest.MapFS{}
	for p, content := range files {
		out[p] = mapFile(content)
	}
	return out
}
