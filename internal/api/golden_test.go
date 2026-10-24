package api

import (
	"cmp"
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/shared/golden"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/parquet-go/parquet-go"
)

// -update-golden rewrites testdata/*.golden.json from native's answers,
// only for a deliberate change a review reads in the diff. The goldens
// were frozen from the old query engine's answers (step 6a); they are the
// only reference now.
var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/*.golden.json from native's answers")

// frozen returns the golden kept under key in t's test's file, keeping
// got (native's answer) there first with -update-golden, and fails t
// unless got equals it.
func frozen[T any](t testing.TB, key string, got T) T {
	t.Helper()
	want := golden.Get(t, golden.For(t, *updateGolden), key, got)
	if d := golden.Diff(got, want); d != "" {
		t.Errorf("%s: the answer differs from its golden at %s", key, d)
	}
	return want
}

// keep returns the golden kept under key, keeping got there first with
// -update-golden unless the golden held when the run began is alike, in
// which case that one stays as it was (an answer the old SQL gave beside
// the one an allowance takes from it, say).
func keep[T any](t testing.TB, key string, got T, alike func(prev T) bool) T {
	t.Helper()
	s := golden.For(t, *updateGolden)
	if *updateGolden {
		if prev, ok := golden.Previous[T](t, s, key); ok && alike(prev) {
			got = prev
		}
	}
	return golden.Get(t, s, key, got)
}

// oldAnswer is an old query's answer as its golden keeps it, beside the
// SQL and the values bound.
type oldAnswer struct {
	SQL  string   `json:"sql"`
	Vals string   `json:"vals,omitempty"`
	Out  tableOut `json:"out"`
}

// oldTable returns the golden answer of one of the tools' old queries over
// in's project and range (q, any other :name bound from vals), keyed by
// the range and a hash of the SQL and values: the old query engine's,
// its cells as text (NULL as ""). got is native's answer read as that
// table, and adjust (nil for none) what the test does to the golden
// before it compares (an allowance): with -update-golden, a golden that
// adjusted equals got (numbers within 1e-9) is kept as it was, else got
// is kept.
func oldTable(t *testing.T, h *host, in rangeIn, q string, vals map[string]enginesql.Value, got tableOut, adjust func(tableOut) tableOut) tableOut {
	t.Helper()
	v := ""
	if len(vals) > 0 {
		v = fmt.Sprint(vals)
	}
	sum := sha256.Sum256([]byte(q + "\x00" + v))
	key := fmt.Sprintf("%d %s..%s %x", in.ProjectID, in.From, in.To, sum[:6])
	if h.queryRows > 0 {
		key += fmt.Sprintf(" rows %d", h.queryRows)
	}
	if adjust == nil {
		adjust = func(o tableOut) tableOut { return o }
	}
	return keep(t, key, oldAnswer{SQL: q, Vals: v, Out: got}, func(prev oldAnswer) bool {
		a := adjust(prev.Out)
		return prev.SQL == q && prev.Vals == v && tablesAlike(a, got, 1e-9) && a.Truncated == got.Truncated && a.Note == got.Note
	}).Out
}

// tablesAlike says whether got's columns and rows are want's, cell for
// cell as text or as numbers within tol relative to want's (sameNumbers,
// without failing).
func tablesAlike(got, want tableOut, tol float64) bool {
	cell := func(a, b string) bool {
		if a == b {
			return true
		}
		x, err1 := strconv.ParseFloat(a, 64)
		y, err2 := strconv.ParseFloat(b, 64)
		return tol > 0 && err1 == nil && err2 == nil && math.Abs(x-y) <= tol*max(1, math.Abs(y))
	}
	return slices.Equal(got.Columns, want.Columns) &&
		slices.EqualFunc(got.Rows, want.Rows, func(a, b []string) bool { return slices.EqualFunc(a, b, cell) })
}

// cloneTable is o with rows of its own, for an adjust that edits them.
func cloneTable(o tableOut) tableOut {
	rows := make([][]string, len(o.Rows))
	for i, r := range o.Rows {
		rows[i] = slices.Clone(r)
	}
	o.Rows = rows
	return o
}

// measuresColumns is the columns of the measures table the old SQL read
// (DESCRIBE SELECT * FROM measures): id, the fixed columns, then every
// other column of the files under dir in the order a union by name first
// sees it, names matched regardless of case (the open logs, by project,
// then the Parquet files, each file's in its order), then each rename
// target no file holds, then bucket.
func measuresColumns(t *testing.T, h *host) []string {
	t.Helper()
	out := []string{"id"}
	seen := map[string]bool{"id": true, "v": true}
	add := func(names ...string) {
		for _, n := range names {
			if !seen[strings.ToLower(n)] {
				seen[strings.ToLower(n)] = true
				out = append(out, n)
			}
		}
	}
	logNames := func(keys map[string]bool) []string {
		var names []string
		for _, c := range archive.LogColumns(keys) {
			names = append(names, c.Name)
		}
		return names
	}
	add(logNames(nil)...)
	all, err := archive.ListFiles(h.eventsDir)
	if err != nil {
		t.Fatal(err)
	}
	ids := slices.Sorted(maps.Keys(all))
	for _, id := range ids {
		for _, p := range all[id].Logs {
			keys, err := archive.NewKeyScanner().Keys(p)
			if err != nil {
				t.Fatal(err)
			}
			add(logNames(keys)...)
		}
	}
	for _, id := range ids {
		for _, p := range all[id].Parquet {
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			st, _ := f.Stat()
			pf, err := parquet.OpenFile(f, st.Size())
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range pf.Schema().Columns() {
				add(c[0])
			}
			f.Close()
		}
	}
	_, attrs := h.reg.Snapshot(context.Background()).RuleLists()
	attrs = slices.Clone(attrs)
	slices.SortFunc(attrs, func(a, b store.AttributeRule) int {
		return cmp.Or(cmp.Compare(a.ProjectID, b.ProjectID), cmp.Compare(a.Key, b.Key))
	})
	blocked := map[string]bool{}
	for _, r := range attrs {
		if r.Action == store.EventRuleBlock {
			blocked[fmt.Sprint(r.ProjectID, r.Key)] = true
		}
	}
	for _, r := range attrs {
		if r.Action == store.EventRuleRename && seen[strings.ToLower("attr_"+r.Key)] && !blocked[fmt.Sprint(r.ProjectID, r.To)] {
			add("attr_" + r.To)
		}
	}
	return append(out, "bucket")
}
