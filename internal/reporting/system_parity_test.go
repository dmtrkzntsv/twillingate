// The parity harness for the system dashboards: each built-in widget's
// native answer checked against the answer its old SQL
// (testdata/system-sql/, kept as the record) gave, frozen in
// testdata/TestSystemWidgetParity.golden.json, on the system fixture as
// of systemToday, under the allowances below.

package reporting

import (
	"cmp"
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

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
)

// The parity harness holds each system widget the native engine answers
// (a .query) to the SQL it replaced, kept under testdata/system-sql as
// <dashboard dir>/<widget>.sql: on the system fixture, for parityPresets,
// native must return the column names and rows that SQL's golden holds,
// in the same order, numbers compared as numbers. A difference is
// accepted only where parityAllowances names it, with its gap id.
//
// The old SQL's COALESCE(x, '') (top-pages' path, campaigns' medium and
// campaign) merges NULL with '' in one group; the native engine keeps
// them apart. Both forms never occur: every event reaches the files
// through archive's line, which leaves an empty field out, so a field not
// sent reads NULL and '' is never stored. Only the order of a tie can
// differ: the SQL sorts that group's '' first, native its NULL last.

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
	// sameRows (G21 only): the same rows, every value compared exactly,
	// in another order: a trend's rows within a day by series, where the
	// SQL ordered them by value.
	sameRows
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
	case sameRows:
		return "same-rows"
	}
	return fmt.Sprintf("allowKind(%d)", int(k))
}

// allowance is one accepted difference of one widget: the gap it is
// (G2, G6-G11, G13-G15, G21), its kind, the columns it covers (by the
// widget's column names, after rename) and why.
type allowance struct {
	Gap     string
	Kind    allowKind
	Columns []string // blankStat: nil covers every column; fillZero and approx: required
	Reason  string   // required for reshape and columnsOnly
}

// parityGaps are the gap ids an allowance may name.
//
// G21 is the harness's own: a trend's rows within a day are ordered by
// series, not by value.
var parityGaps = []string{"G2", "G6", "G7", "G8", "G9", "G10", "G11", "G13", "G14", "G15", "G21"}

// parityAllowances names, per system widget ("<dashboard dir>/<widget>"),
// the differences from its old SQL the harness accepts. An allowance no
// preset needs fails the harness, so none outlives its reason.
//
//	"views/visitors": {{Gap: "G2", Kind: blankStat, Columns: []string{"value"}}},
var parityAllowances = map[string][]allowance{
	"product/app-versions": {{Gap: "G6", Kind: reshape,
		Reason: "true distinct users and groups per version over the range, where the SQL showed the largest per (day, event) count, headed \"(at least)\"; TestAppVersionsAtLeastTheOldFloors holds them to it"}},
	// G6's true per-day distinct users equal the SQL's floors on the
	// fixture; TestAppVersionsAtLeastTheOldFloors holds them to at least.
	"product/users-by-app-version": {{Gap: "G21", Kind: sameRows}},
}

// newSystemWidgets names the system query widgets that have no old SQL
// to compare with (one split from another, say), each with the reason.
var newSystemWidgets = map[string]string{
	"vitals/by-device":  "G9: split from by-device-browser; TestVitalsByDeviceAndBrowserSplitTheOldTable holds it to the old table's device rows",
	"vitals/by-browser": "G9: split from by-device-browser; TestVitalsByDeviceAndBrowserSplitTheOldTable holds it to the old table's browser rows",
}

// TestSystemWidgetParity runs the harness on this release's system
// widgets and the goldens of the old SQL in testdata/system-sql.
func TestSystemWidgetParity(t *testing.T) {
	system, err := fs.Sub(systemFS, "system")
	if err != nil {
		t.Fatal(err)
	}
	cfg := parityConfig{system: system, oldSQL: os.DirFS("testdata/system-sql"), allow: parityAllowances, news: newSystemWidgets,
		old: func(t *testing.T, key, preset string, nat readsql.Result, allows []allowance) readsql.Result {
			return frozenResult(t, key+" "+preset, nat, allows)
		}}
	for _, p := range runParity(t, cfg, newSystemFixture) {
		t.Error(p)
	}
}

// parityConfig is one run of the harness: the system definition, the
// old SQL beside it (which widgets have one), the old SQL's answers, and
// the accepted differences and new widgets.
type parityConfig struct {
	system, oldSQL fs.FS
	// old is the old SQL's answer for widget key on preset, nat being
	// native's (the golden's in the release's run; the harness's own
	// tests on fake widgets derive one from nat).
	old   func(t *testing.T, key, preset string, nat readsql.Result, allows []allowance) readsql.Result
	allow map[string][]allowance
	news  map[string]string
}

// parityWidget is one system query widget to compare with its old SQL.
type parityWidget struct {
	key, name string
	dashboard int64
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
	if err := migrateFrom(ctx, f.st, f.files, f.files.options(Options{}), cfg.system, Manifest()); err != nil {
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
			diffs, u := compareParity(cfg.old(t, w.key, preset, nat, allows), nat, allows)
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
	old := map[string]bool{}
	err := fs.WalkDir(cfg.oldSQL, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		old[p] = true
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
			ok := old[key+".sql"]
			_, isNew := cfg.news[key]
			switch {
			case ok && isNew:
				problems = append(problems, fmt.Sprintf("%s: listed as new, but has old SQL to compare with", key))
			case ok:
				ws = append(ws, parityWidget{key: key, name: fw.Name, dashboard: fd.ID})
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
	case sameRows:
		if a.Gap != "G21" {
			return "only G21 may reorder rows"
		}
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
	if i := find(sameRows); i >= 0 && len(wantRows) == len(gotRows) {
		if r, moved := inWantOrder(wantRows, gotRows); r != nil {
			gotRows, used[i] = r, moved
		}
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

// inWantOrder returns got's rows in want's order when both hold the same
// rows (cells compared by sameNumber), and whether that moved any; nil
// when they do not, so the rows are compared as they came.
func inWantOrder(want, got [][]string) ([][]string, bool) {
	taken := make([]bool, len(got))
	out := make([][]string, 0, len(want))
	moved := false
	for wi, w := range want {
		j := -1
		for gi, g := range got {
			if !taken[gi] && slices.EqualFunc(w, g, sameNumber) {
				j = gi
				break
			}
		}
		if j < 0 {
			return nil, false
		}
		taken[j], moved = true, moved || j != wi
		out = append(out, got[j])
	}
	return out, moved
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

// fakeTrendSystem is a Views dashboard of one trend, views/devices: views
// per day and device, rows within a day by device.
var fakeTrendSystem = mapFS(map[string]string{
	"views/dashboard.json": `{"id": 1, "title": "Views", "group_title": "Reports", "range": "7d", "sidebar": true, "project_tab": true, "layout": [{"widget": "devices"}]}`,
	"views/devices.json":   `{"component": "line", "title": "Devices", "props": {"format": "number"}}`,
	"views/devices.query":  `{"query":{"kind":"trend","table":"views","series":"device","where":[{"column":"device","op":"is_set"}],"metrics":[{"op":"count","as":"y"}]},"rename":{"day":"x"}}`,
})

// oldAnswer is a fake widget's old answer, made from native's: what a
// fake old SQL would have returned.
type oldAnswer func(nat readsql.Result) readsql.Result

// asIs is the old answer native's own.
func asIs(nat readsql.Result) readsql.Result { return nat }

// scaled is native's answer with column col's numbers times by.
func scaled(col string, by float64) oldAnswer {
	return func(nat readsql.Result) readsql.Result {
		out := readsql.Result{Columns: nat.Columns}
		c := slices.Index(nat.Columns, col)
		for _, r := range nat.Rows {
			r = slices.Clone(r)
			if v, err := strconv.ParseFloat(r[c], 64); err == nil {
				r[c] = strconv.FormatFloat(v*by, 'f', -1, 64)
			}
			out.Rows = append(out.Rows, r)
		}
		return out
	}
}

// withColumn is native's answer with column from named to.
func withColumn(from, to string) oldAnswer {
	return func(nat readsql.Result) readsql.Result {
		out := readsql.Result{Columns: slices.Clone(nat.Columns), Rows: nat.Rows}
		out.Columns[slices.Index(out.Columns, from)] = to
		return out
	}
}

// byValue is a trend's old answer with the rows of each x ordered by y,
// then by series descending, as an old SQL ordering by value had them.
func byValue(old oldAnswer) oldAnswer {
	return func(nat readsql.Result) readsql.Result {
		out := old(nat)
		x, y, series := slices.Index(out.Columns, "x"), slices.Index(out.Columns, "y"), slices.Index(out.Columns, "series")
		rows := slices.Clone(out.Rows)
		slices.SortStableFunc(rows, func(a, b []string) int {
			av, _ := strconv.ParseFloat(a[y], 64)
			bv, _ := strconv.ParseFloat(b[y], 64)
			return cmp.Or(strings.Compare(a[x], b[x]), cmp.Compare(av, bv), strings.Compare(b[series], a[series]))
		})
		out.Rows = rows
		return out
	}
}

// TestParityHarnessOnAFakeWidget proves the harness on fakeSystem: it
// passes the same answer, finds a different one, honours an approx
// allowance and refuses one no preset needs, and checks that every query
// widget has old SQL or is listed as new. Each case's old answer is made
// from native's, as a fake old SQL would have returned it.
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
	count := mapFS(map[string]string{"views/count.sql": ""})
	devices := mapFS(map[string]string{"views/devices.sql": ""})
	approx2 := map[string][]allowance{"views/count": {{Gap: "G14", Kind: approx, Columns: []string{"value"}}}}
	sameRows21 := map[string][]allowance{"views/devices": {{Gap: "G21", Kind: sameRows}}}
	cases := []struct {
		name   string
		cfg    parityConfig
		old    oldAnswer
		wantIn []string // each a substring of some problem; none: no problems
	}{
		{"same answer", parityConfig{oldSQL: count}, asIs, nil},
		{"different value", parityConfig{oldSQL: count}, scaled("value", 2),
			[]string{"views/count 7d: row 0 value", "views/count 30d: row 0 value", "views/count 90d: row 0 value"}},
		{"different column", parityConfig{oldSQL: count}, withColumn("value", "total"),
			[]string{`views/count 7d: columns ["value"], the old SQL's ["total"]`}},
		{"within 2% without approx", parityConfig{oldSQL: count}, scaled("value", 1.01),
			[]string{"views/count 7d: row 0 value"}},
		{"within 2% with approx", parityConfig{oldSQL: count, allow: approx2}, scaled("value", 1.01), nil},
		{"past 2% with approx", parityConfig{oldSQL: count, allow: approx2}, scaled("value", 1.05),
			[]string{"views/count 7d: row 0 value"}},
		{"approx never needed", parityConfig{oldSQL: count, allow: approx2}, asIs,
			[]string{"views/count: allowance G14 approx is needed on no preset"}},
		{"no old SQL", parityConfig{oldSQL: mapFS(nil)}, asIs,
			[]string{"views/count: a query widget with no testdata/system-sql/views/count.sql and not listed as new"}},
		{"no old SQL, listed as new", parityConfig{oldSQL: mapFS(nil), news: map[string]string{"views/count": "split from views/old"}}, asIs, nil},
		{"new with old SQL", parityConfig{oldSQL: count, news: map[string]string{"views/count": "split"}}, asIs,
			[]string{"views/count: listed as new, but has old SQL"}},
		{"old SQL of no widget", parityConfig{oldSQL: mapFS(map[string]string{"views/count.sql": "", "views/gone.sql": "SELECT 1"})}, asIs,
			[]string{"testdata/system-sql/views/gone.sql: no system query widget views/gone"}},
		{"allowance of no widget", parityConfig{oldSQL: mapFS(nil), news: map[string]string{"views/count": "new"},
			allow: map[string][]allowance{"views/gone": {{Gap: "G2", Kind: blankStat}}}}, asIs,
			[]string{"views/gone: has allowances, but is no system query widget with old SQL"}},
		{"malformed allowance", parityConfig{oldSQL: count,
			allow: map[string][]allowance{"views/count": {{Gap: "G99", Kind: columnsOnly, Reason: "x"}, {Gap: "G6", Kind: reshape}}}}, asIs,
			[]string{"allowance G99 columns-only: unknown gap", "allowance G6 reshape: gives no reason"}},
		{"reshape or columns only off the reshapes", parityConfig{oldSQL: count,
			allow: map[string][]allowance{"views/count": {{Gap: "G10", Kind: reshape, Reason: "labels"}, {Gap: "G13", Kind: columnsOnly, Reason: "50 series"}}}}, asIs,
			[]string{"allowance G10 reshape: only the reshapes", "allowance G13 columns-only: only the reshapes"}},
		{"two allowances of a kind", parityConfig{oldSQL: count,
			allow: map[string][]allowance{"views/count": {{Gap: "G14", Kind: approx, Columns: []string{"value"}}, {Gap: "G14", Kind: approx, Columns: []string{"previous"}}}}},
			scaled("value", 1.01),
			[]string{"allowance G14 approx: a second allowance of this kind"}},
		// A stat's one row has no key, so it always matches the SQL's:
		// fill-zero never stands in for a different value.
		{"fill-zero on a replaced row", parityConfig{oldSQL: count,
			allow: map[string][]allowance{"views/count": {{Gap: "G11", Kind: fillZero, Columns: []string{"value"}}}}}, scaled("value", 2),
			[]string{"views/count 7d: row 0 value", "views/count: allowance G11 fill-zero is needed on no preset"}},
		// same-rows on a trend: a reorder passes, a changed value does not.
		{"rows by value, without same-rows", parityConfig{system: fakeTrendSystem, oldSQL: devices}, byValue(asIs),
			[]string{"views/devices 30d: row"}},
		{"rows by value, with same-rows", parityConfig{system: fakeTrendSystem, oldSQL: devices, allow: sameRows21}, byValue(asIs), nil},
		{"a changed value, with same-rows", parityConfig{system: fakeTrendSystem, oldSQL: devices, allow: sameRows21}, byValue(scaled("y", 2)),
			[]string{"views/devices 30d: row 0 y", "views/devices: allowance G21 same-rows is needed on no preset"}},
		{"same-rows never needed", parityConfig{system: fakeTrendSystem, oldSQL: devices, allow: sameRows21}, asIs,
			[]string{"views/devices: allowance G21 same-rows is needed on no preset"}},
		{"same-rows off G21", parityConfig{oldSQL: count,
			allow: map[string][]allowance{"views/count": {{Gap: "G15", Kind: sameRows}}}}, asIs,
			[]string{"allowance G15 same-rows: only G21 may reorder rows"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.cfg.system == nil {
				c.cfg.system = fakeSystem
			}
			c.cfg.old = func(_ *testing.T, _, _ string, nat readsql.Result, _ []allowance) readsql.Result { return c.old(nat) }
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
		{"same rows reordered", res(trend, []string{"a", "4"}, []string{"b", "1"}), res(trend, []string{"b", "1"}, []string{"a", "4"}),
			[]allowance{{Gap: "G21", Kind: sameRows}}, 0, []bool{true}},
		{"same rows in order", res(trend, []string{"a", "4"}, []string{"b", "1"}), res(trend, []string{"a", "4"}, []string{"b", "1"}),
			[]allowance{{Gap: "G21", Kind: sameRows}}, 0, []bool{false}},
		{"same rows, a value changed", res(trend, []string{"a", "4"}, []string{"b", "1"}), res(trend, []string{"b", "2"}, []string{"a", "4"}),
			[]allowance{{Gap: "G21", Kind: sameRows}}, 4, []bool{false}},
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

// TestAppVersionsAtLeastTheOldFloors: G6's reshape replaced the app
// version widgets' floors (the largest distinct count of any one event on
// a day) with the true distinct counts, so on every preset each row the old
// SQL drew (its golden) is still there, by its key, with numbers at least
// the floor's.
func TestAppVersionsAtLeastTheOldFloors(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)
	cases := []struct {
		name string
		keys int               // the leading key columns, the same in both
		cols map[string]string // native column: the old SQL's
	}{
		{"app-versions", 1, map[string]string{"Events": "Events", "Users": "Users (at least)", "Groups": "Groups (at least)"}},
		{"users-by-app-version", 2, map[string]string{"y": "y"}},
	}
	for _, c := range cases {
		id := systemWidgetOf(t, f.svc, 2, c.name)
		above := 0
		for _, preset := range parityPresets {
			from, to := presetDates(preset, f.today)
			got, err := f.svc.WidgetData(ctx, DataRequest{WidgetID: id, ProjectID: f.project, From: from, To: to})
			if err != nil {
				t.Fatal(err)
			}
			nat := got.Data.(readsql.Result)
			// atLeast is what old, the old SQL's answer, says of nat, and
			// the cells of nat above their floor.
			atLeast := func(old readsql.Result) ([]string, int) {
				var problems []string
				if len(old.Rows) == 0 || len(nat.Rows) != len(old.Rows) {
					return []string{fmt.Sprintf("native %d rows, old SQL %d; want the same, and some", len(nat.Rows), len(old.Rows))}, 0
				}
				byKey := map[string][]string{}
				for _, r := range nat.Rows {
					byKey[strings.Join(r[:c.keys], "\x00")] = r
				}
				above := 0
				for _, r := range old.Rows {
					n, ok := byKey[strings.Join(r[:c.keys], "\x00")]
					if !ok {
						problems = append(problems, fmt.Sprintf("the old SQL's %v has no native row", r[:c.keys]))
						continue
					}
					for natCol, oldCol := range c.cols {
						want, _ := strconv.ParseFloat(r[slices.Index(old.Columns, oldCol)], 64)
						have, err := strconv.ParseFloat(n[slices.Index(nat.Columns, natCol)], 64)
						if err != nil || have < want {
							problems = append(problems, fmt.Sprintf("%v: %s %q, below the old floor %v", r[:c.keys], natCol, n[slices.Index(nat.Columns, natCol)], want))
						}
						if have > want {
							above++
						}
					}
				}
				return problems, above
			}
			// With -update-golden the old answer stays as long as it holds;
			// native's own has its columns, not the old SQL's.
			old := keep(t, c.name+" "+preset, nat, func(prev readsql.Result) bool {
				p, _ := atLeast(prev)
				return len(p) == 0
			})
			problems, n := atLeast(old)
			for _, p := range problems {
				t.Errorf("%s %s: %s", c.name, preset, p)
			}
			above += n
		}
		t.Logf("%s: %d cells above the old floor", c.name, above)
	}
}

// TestVitalsByDeviceAndBrowserSplitTheOldTable: Web Vitals' by-device and
// by-browser tables (G9) are the old by-device-browser table split by its
// Dimension column, which they drop: the same rows, values and order, on
// every preset. The old SQL is testdata/vitals-by-device-browser.sql, kept
// out of testdata/system-sql because no one widget answers it; its golden
// is the reference.
func TestVitalsByDeviceAndBrowserSplitTheOldTable(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)
	splits := []struct{ widget, dimension, column string }{{"by-device", "device", "Device"}, {"by-browser", "browser", "Browser"}}
	for _, preset := range parityPresets {
		from, to := presetDates(preset, f.today)
		nats := make([]readsql.Result, len(splits))
		// joined is the old table as native's two answer it, device rows
		// first: what -update-golden keeps when the old one no longer holds.
		joined := readsql.Result{Columns: []string{"Dimension", "Value", "Samples", "LCP p75 (ms)", "INP p75 (ms)", "CLS p75"}}
		for i, c := range splits {
			got, err := f.svc.WidgetData(ctx, DataRequest{WidgetID: systemWidgetOf(t, f.svc, 6, c.widget), ProjectID: f.project, From: from, To: to})
			if err != nil {
				t.Fatalf("%s %s: %v", preset, c.widget, err)
			}
			nat, ok := got.Data.(readsql.Result)
			if !ok {
				t.Fatalf("%s %s: native returned %T", preset, c.widget, got.Data)
			}
			nats[i] = nat
			for _, r := range nat.Rows {
				joined.Rows = append(joined.Rows, append([]string{c.dimension}, r...))
			}
		}
		// split is old's rows of c's dimension, as c's widget shows them.
		split := func(old readsql.Result, i int) readsql.Result {
			c := splits[i]
			want := readsql.Result{Columns: append([]string{c.column}, old.Columns[2:]...)}
			for _, r := range old.Rows {
				if r[0] == c.dimension {
					want.Rows = append(want.Rows, r[1:])
				}
			}
			return want
		}
		old := keep(t, preset, joined, func(prev readsql.Result) bool {
			for i := range splits {
				if diffs, _ := compareParity(split(prev, i), nats[i], nil); len(diffs) > 0 {
					return false
				}
			}
			return true
		})
		if !slices.Equal(old.Columns, joined.Columns) {
			t.Fatalf("%s: the golden's columns %v", preset, old.Columns)
		}
		for i, c := range splits {
			want := split(old, i)
			if len(want.Rows) < 2 {
				t.Fatalf("%s %s: the old SQL has %d rows: a weak case", preset, c.widget, len(want.Rows))
			}
			diffs, _ := compareParity(want, nats[i], nil)
			for _, d := range diffs {
				t.Errorf("%s %s: %s", preset, c.widget, d)
			}
		}
	}
}

// TestSystemStatsOverNoEvents pins what the system dashboards' stats show
// for a project with no events. The differences from their old SQL are
// G2's: an empty metric is blank, where the old SQL read 0 or returned no
// row (the stat component draws a blank figure, not "0").
func TestSystemStatsOverNoEvents(t *testing.T) {
	ctx := context.Background()
	st, files := newTestStoreAndFiles(t, 1000)
	if err := Migrate(ctx, st, files, files.options(Options{})); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pid := mustCreateProject(t, st, "empty")
	svc := newFilesService(st, files, Options{Native: native.New(native.Options{Dir: files.Dir})})
	from, to := "2026-10-01", "2026-10-07"
	cases := []struct {
		name   string   // <dashboard dir>/<widget>
		native []string // the native row
		old    []string // the old SQL's row, nil for no row: the record
		why    string
	}{
		{"views/visitors", []string{"", ""}, []string{"0", "0"}, "G2: daily_sum over no rows is empty; the SQL COALESCEd it to 0"},
		{"views/bounce-rate", []string{""}, []string{"0"}, "G2: bounce_rate over no sessions is empty; the SQL wrote 0"},
		{"views/avg-session", []string{""}, []string{"0"}, "G2: avg over no sessions is empty; the SQL wrote 0"},
		{"views/consent-share", []string{""}, []string{""}, "none: a ratio over a 0 denominator is empty, as the SQL's NULL"},
		{"product/total-events", []string{"0", "0"}, []string{"0", "0"}, "none: a count over no rows is 0 in both"},
		{"product/peak-dau", []string{""}, []string{""}, "none: daily_max over no days is empty, as the SQL's MAX over no rows"},
		{"product/avg-dau", []string{""}, []string{""}, "none: daily_avg over no days is empty, as the SQL's AVG over no rows"},
		{"users/active-users", []string{"0"}, []string{"0"}, "none: users over no rows is 0 in both"},
		{"users/new-users", []string{"0"}, []string{"0"}, "none: users over no rows is 0 in both"},
		{"users/actions", []string{"0"}, []string{"0"}, "none: a count over no rows is 0 in both"},
		{"users/actions-per-user", []string{""}, []string{"0"}, "G2: a ratio over no users is empty; the SQL wrote 0"},
		{"groups/active-groups", []string{"0"}, []string{"0"}, "none: groups over no rows is 0 in both"},
		{"groups/new-groups", []string{"0"}, []string{"0"}, "none: groups over no rows is 0 in both"},
		{"groups/actions", []string{"0"}, []string{"0"}, "none: a count over no rows is 0 in both"},
		{"groups/actions-per-group", []string{""}, []string{"0"}, "G2: a ratio over no groups is empty; the SQL wrote 0"},
		{"vitals/lcp", []string{""}, nil, "G2: a percentile over no samples is empty; the SQL returned no row"},
		{"vitals/inp", []string{""}, nil, "G2: a percentile over no samples is empty; the SQL returned no row"},
		{"vitals/cls", []string{""}, nil, "G2: a percentile over no samples is empty; the SQL returned no row"},
		{"vitals/fcp", []string{""}, nil, "G2: a percentile over no samples is empty; the SQL returned no row"},
		{"vitals/ttfb", []string{""}, nil, "G2: a percentile over no samples is empty; the SQL returned no row"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, name, _ := strings.Cut(c.name, "/")
			got, err := svc.WidgetData(ctx, DataRequest{WidgetID: systemWidgetOf(t, svc, map[string]int64{"views": 1, "product": 2, "users": 3, "groups": 4, "vitals": 6}[dir], name), ProjectID: pid, From: from, To: to})
			if err != nil {
				t.Fatal(err)
			}
			nat, ok := got.Data.(readsql.Result)
			if !ok || len(nat.Rows) != 1 || !slices.Equal(nat.Rows[0], c.native) {
				t.Errorf("native %v, want one row %q (%s)", got.Data, c.native, c.why)
			}
		})
	}
}
