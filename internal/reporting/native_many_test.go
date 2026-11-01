package reporting

import (
	"context"
	"io/fs"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// equalityPresets are the presets TestSystemWidgetsAnswerManyEqualsAnswer
// and TestWidgetDataStreamEqualsWidgetData run: every one, but under the
// race detector 30d alone, to keep the package's race run short; make
// check, without race, runs them all.
func equalityPresets() []string {
	if raceEnabled {
		return []string{"30d"}
	}
	return systemPresets
}

// TestSystemWidgetsAnswerManyEqualsAnswer holds the native engine's
// AnswerMany to Answer over every built-in query widget on the system
// fixture, for every preset: each dashboard's widgets in one call, and
// every dashboard's at once.
func TestSystemWidgetsAnswerManyEqualsAnswer(t *testing.T) {
	system, err := fs.Sub(systemFS, "system")
	if err != nil {
		t.Fatal(err)
	}
	type widget struct {
		key string
		q   native.Query
	}
	byDashboard := map[string][]widget{}
	var dashboards []string
	err = fs.WalkDir(system, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".query" {
			return err
		}
		content, err := fs.ReadFile(system, p)
		if err != nil {
			return err
		}
		pq, err := parseQuery(string(content))
		if err != nil {
			t.Errorf("%s: %v", p, err)
			return nil
		}
		qc := pq.qc
		dir := path.Dir(p)
		if _, ok := byDashboard[dir]; !ok {
			dashboards = append(dashboards, dir)
		}
		byDashboard[dir] = append(byDashboard[dir], widget{key: strings.TrimSuffix(p, ".query"), q: qc.Query})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboards) < 5 {
		t.Fatalf("%d dashboards with query widgets", len(dashboards))
	}
	f := newSystemFixture(t)
	e := native.New(native.Options{Dir: f.files.Dir})
	ctx := context.Background()
	for _, preset := range equalityPresets() {
		from, to := presetDates(preset, f.today)
		p := native.Params{Project: f.project, From: mustDate(t, from), To: mustDate(t, to)}
		var all []native.Request
		var keys []string
		for _, d := range dashboards {
			for _, w := range byDashboard[d] {
				all = append(all, native.Request{Query: w.q, Params: p, Rules: archive.Rules{}})
				keys = append(keys, w.key)
			}
		}
		want := make([]native.Answered, len(all))
		for i, r := range all {
			res, err := e.Answer(ctx, r.Query, r.Params, r.Rules)
			want[i] = native.Answered{Result: res, Err: err}
		}
		check := func(what string, idx []int, got []native.Answered) {
			for k, i := range idx {
				if d := diffAnswered(got[k], want[i]); d != "" {
					t.Errorf("%s %s (%s): %s", keys[i], preset, what, d)
				}
			}
		}
		at := 0
		for _, d := range dashboards {
			n := len(byDashboard[d])
			idx := make([]int, n)
			for k := range idx {
				idx[k] = at + k
			}
			check("its dashboard", idx, e.AnswerAll(ctx, all[at:at+n]))
			at += n
		}
		idx := make([]int, len(all))
		for i := range idx {
			idx[i] = i
		}
		check("every dashboard", idx, e.AnswerAll(ctx, all))
	}
}

func mustDate(t *testing.T, s string) civil.Date {
	t.Helper()
	d, err := civil.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// diffAnswered is how got differs from want: "" when it does not, the
// errors by their text, numbers within a rounding error.
func diffAnswered(got, want native.Answered) string {
	if got.Err != nil || want.Err != nil {
		if (got.Err == nil) != (want.Err == nil) || got.Err.Error() != want.Err.Error() {
			return "error " + errText(got.Err) + ", want " + errText(want.Err)
		}
		return ""
	}
	g, w := got.Result, want.Result
	if !slices.Equal(g.Columns, w.Columns) || g.Truncated != w.Truncated || !slices.Equal(g.Approximate, w.Approximate) ||
		len(g.Rows) != len(w.Rows) {
		return "got " + strconv.Quote(strings.Join(g.Columns, ",")) + " " + strconv.Itoa(len(g.Rows)) + " rows, want " +
			strconv.Quote(strings.Join(w.Columns, ",")) + " " + strconv.Itoa(len(w.Rows))
	}
	for i := range w.Rows {
		ok := len(g.Rows[i]) == len(w.Rows[i])
		for j := 0; ok && j < len(w.Rows[i]); j++ {
			ok = sameNumberOrText(g.Rows[i][j], w.Rows[i][j])
		}
		if !ok {
			return "row " + strconv.Itoa(i) + ": " + strings.Join(g.Rows[i], ",") + ", want " + strings.Join(w.Rows[i], ",")
		}
	}
	return ""
}

func errText(err error) string {
	if err == nil {
		return "none"
	}
	return err.Error()
}

// sameNumberOrText says whether two cells are equal: as text, or as
// numbers within a rounding error (sums of doubles depend on their order).
func sameNumberOrText(a, b string) bool {
	if a == b {
		return true
	}
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	return err1 == nil && err2 == nil && math.Abs(x-y) <= 1e-9*math.Max(1, math.Abs(y))
}
