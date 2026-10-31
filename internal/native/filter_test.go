package native

import (
	"context"
	"slices"
	"strconv"
	"testing"
)

// prefix "" passes every text value but NULL, the empty one included:
// "not NULL", which is_set is not (it leaves "" out too). internal/api's
// measures keeps a custom key's empty value with it, as its old SQL's
// IS NOT NULL did.
func TestPrefixEmptyIsNotNull(t *testing.T) {
	b := &batch{N: 4, Cols: map[string]*column{"attr_k": {Dict: []string{"", "x"}, Idx: []int32{0, 1, -1, 0}}}}
	for _, c := range []struct {
		f    Filter
		want []bool
	}{
		{Filter{Column: "attr_k", Op: "prefix", Values: []string{""}}, []bool{true, true, false, true}},
		{Filter{Column: "attr_k", Op: "is_set"}, []bool{false, true, false, false}},
	} {
		if err := c.f.validate("measures", "where[0]"); err != nil {
			t.Fatalf("%s: %v", c.f.Op, err)
		}
		fs, err := compileFilters([]Filter{c.f}, "where")
		if err != nil {
			t.Fatal(err)
		}
		keep, err := passRows(b, fs, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(keep, c.want) {
			t.Errorf("%s = %v, want %v", c.f.Op, keep, c.want)
		}
	}
}

// TestAnyOfIsAnOr: an any_of keeps a row when one of its filters does,
// so its count is the inclusion–exclusion of its two filters' counts,
// in where and in a metric's where alike.
func TestAnyOfIsAnOr(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	a := Filter{Column: "path", Op: "prefix", Values: []string{"/q"}}
	b := Filter{Column: "device_model", Op: "is_set"}
	count := func(where ...Filter) int64 {
		t.Helper()
		res, err := e.Answer(context.Background(), Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count"}}, Where: where},
			Params{Project: 1, From: date(t, engineFrom), To: date(t, today)}, engineRules)
		if err != nil {
			t.Fatal(err)
		}
		n, err := strconv.ParseInt(res.Rows[0][0], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	or := Filter{Op: "any_of", Any: []Filter{a, b}}
	got, want := count(or), count(a)+count(b)-count(a, b)
	if got != want || got == count(a) || got == count(b) {
		t.Errorf("any_of count = %d, want %d (a %d, b %d)", got, want, count(a), count(b))
	}
	res, err := e.Answer(context.Background(), Query{Kind: "stat", Table: "views",
		Metrics: []Metric{{Op: "count", Where: []Filter{or}}}},
		Params{Project: 1, From: date(t, engineFrom), To: date(t, today)}, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows[0][0] != strconv.FormatInt(want, 10) {
		t.Errorf("any_of in a metric's where = %s, want %d", res.Rows[0][0], want)
	}
}
