package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// memSource serves hand-built batches. It honours the Source contract's
// "attr_*" pattern by keeping every attr_ column of a batch, and keeps only
// the requested columns.
type memSource struct{ batches []*Batch }

func (m memSource) Name() string { return "mem" }

func (m memSource) Scan(_ []string, cols []string, _ string, _, _ int64, fn func(*Batch) error) error {
	for _, b := range m.batches {
		out := &Batch{N: b.N, Cols: map[string]*Column{}}
		for _, want := range cols {
			if prefix, ok := strings.CutSuffix(want, "*"); ok {
				for name, c := range b.Cols {
					if strings.HasPrefix(name, prefix) {
						out.Cols[name] = c
					}
				}
			} else if c, ok := b.Cols[want]; ok {
				out.Cols[want] = c
			}
		}
		if err := fn(out); err != nil {
			return err
		}
	}
	return nil
}

// strCol dictionary-encodes values; "\x00" is null.
func strCol(vals ...string) *Column {
	c := &Column{}
	ids := map[string]int32{}
	for _, v := range vals {
		if v == "\x00" {
			c.Idx = append(c.Idx, -1)
			continue
		}
		id, ok := ids[v]
		if !ok {
			id = int32(len(c.Dict))
			ids[v] = id
			c.Dict = append(c.Dict, v)
		}
		c.Idx = append(c.Idx, id)
	}
	return c
}

func intCol(vals ...int64) *Column { return &Column{Ints: vals} }

type r struct {
	day            int64
	country, actor string
}

func countryBatch(rows ...r) *Batch {
	var days []int64
	var cs, as []string
	for _, x := range rows {
		days = append(days, x.day)
		cs = append(cs, x.country)
		as = append(as, x.actor)
	}
	return &Batch{N: len(rows), Cols: map[string]*Column{
		"day": intCol(days...), "country": strCol(cs...), "actor_id": strCol(as...),
	}}
}

func TestCountriesSumsDailyDistinct(t *testing.T) {
	src := memSource{[]*Batch{countryBatch(
		r{1, "DE", "a"}, r{2, "DE", "a"}, r{2, "DE", "a"}, r{1, "DE", "b"},
		r{1, "US", "c"}, r{1, "US", "c"},
	)}}
	got, err := Countries(src, nil, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{{"DE", "3"}, {"US", "1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCountriesExcludesEmpty(t *testing.T) {
	src := memSource{[]*Batch{countryBatch(r{1, "", "a"}, r{1, "\x00", "b"}, r{1, "FR", "c"})}}
	got, _ := Countries(src, nil, 0, 10)
	want := []Row{{"FR", "1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCountriesTop20Order(t *testing.T) {
	var rows []r
	for i := 0; i < 25; i++ {
		// country i has i/5+1 visitors, so ties force the label order.
		for a := 0; a <= i/5; a++ {
			rows = append(rows, r{1, fmt.Sprintf("C%02d", i), fmt.Sprintf("a%d", a)})
		}
	}
	got, _ := Countries(memSource{[]*Batch{countryBatch(rows...)}}, nil, 0, 10)
	if len(got) != 20 {
		t.Fatalf("got %d rows", len(got))
	}
	if !reflect.DeepEqual(got[0], Row{"C20", "5"}) || !reflect.DeepEqual(got[4], Row{"C24", "5"}) ||
		!reflect.DeepEqual(got[5], Row{"C15", "4"}) || !reflect.DeepEqual(got[19], Row{"C09", "2"}) {
		t.Fatalf("order: %v", got)
	}
}

func TestDictRemapAcrossBatches(t *testing.T) {
	b1 := &Batch{N: 2, Cols: map[string]*Column{
		"day": intCol(1, 1), "country": {Dict: []string{"DE", "US"}, Idx: []int32{0, 1}},
		"actor_id": strCol("a", "b"),
	}}
	b2 := &Batch{N: 2, Cols: map[string]*Column{
		"day": intCol(1, 1), "country": {Dict: []string{"US", "DE"}, Idx: []int32{0, 1}},
		"actor_id": strCol("c", "d"),
	}}
	got, _ := Countries(memSource{[]*Batch{b1, b2}}, nil, 0, 10)
	want := []Row{{"DE", "2"}, {"US", "2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// Same actor on both sides of a batch boundary counts once.
	b3 := countryBatch(r{1, "DE", "a"})
	b4 := countryBatch(r{1, "DE", "a"})
	got, _ = Countries(memSource{[]*Batch{b3, b4}}, nil, 0, 10)
	if !reflect.DeepEqual(got, []Row{{"DE", "1"}}) {
		t.Fatalf("got %v", got)
	}
}

func TestDAUPerDay(t *testing.T) {
	// 2026-09-01 is day 20697 since the epoch.
	b := &Batch{N: 4, Cols: map[string]*Column{
		"day":      intCol(20698, 20697, 20697, 20697),
		"actor_id": strCol("a", "a", "b", "a"),
	}}
	got, err := DAU(memSource{[]*Batch{b}}, nil, 0, 99999)
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{{"2026-09-01", "2"}, {"2026-09-02", "1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestAttributeValuesSkipsNull(t *testing.T) {
	b := &Batch{N: 4, Cols: map[string]*Column{
		"actor_id":  strCol("a", "a", "b", "c"),
		"group_id":  strCol("g1", "", "g1", "\x00"),
		"attr_plan": strCol("pro", "pro", "free", "\x00"),
		"attr_ab":   strCol("x", "\x00", "x", "y"),
		"day":       intCol(1, 1, 1, 1), // not an attribute
	}}
	got, err := AttributeValues(memSource{[]*Batch{b}}, nil, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{
		{"ab", "x", "2", "2", "1"},
		{"ab", "y", "1", "1", "0"},
		{"plan", "pro", "2", "1", "1"},
		{"plan", "free", "1", "1", "1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCountriesNullActorOnlyGroupIsZero(t *testing.T) {
	src := memSource{[]*Batch{countryBatch(r{1, "DE", "\x00"}, r{1, "US", "a"}, r{1, "US", "\x00"})}}
	got, _ := Countries(src, nil, 0, 10)
	want := []Row{{"US", "1"}, {"DE", "0"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestAttributeValuesNullActorCountsAsUser(t *testing.T) {
	b := &Batch{N: 3, Cols: map[string]*Column{
		"actor_id":  strCol("a", "\x00", "\x00"),
		"group_id":  strCol("", "", ""),
		"attr_plan": strCol("pro", "pro", "free"),
	}}
	got, _ := AttributeValues(memSource{[]*Batch{b}}, nil, 0, 10)
	want := []Row{{"plan", "pro", "2", "2", "0"}, {"plan", "free", "1", "1", "0"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestAttributeValuesTieBreaksByValue(t *testing.T) {
	b := &Batch{N: 3, Cols: map[string]*Column{
		"actor_id":  strCol("a", "b", "c"),
		"group_id":  strCol("", "", ""),
		"attr_plan": strCol("zeta", "alpha", "mid"),
	}}
	got, _ := AttributeValues(memSource{[]*Batch{b}}, nil, 0, 10)
	if got[0][1] != "alpha" || got[1][1] != "mid" || got[2][1] != "zeta" {
		t.Fatalf("got %v", got)
	}
}
