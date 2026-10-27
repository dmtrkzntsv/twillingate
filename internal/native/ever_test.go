package native

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// TestEverAndNever: ever keeps the rows of the actors with one event
// passing its where, over every day up to to or the range's; never the
// others'; together every row with an actor. In a metric's where too,
// and answered alone by AnswerMany as by Answer.
func TestEverAndNever(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	p := Params{Project: 1, From: date(t, "2026-09-01"), To: date(t, today)}
	ask := func(q Query, p Params) Result {
		t.Helper()
		res, err := e.Answer(context.Background(), q, p, engineRules)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	num := func(s string) int64 {
		n, _ := strconv.ParseInt(s, 10, 64)
		return n
	}
	de := []Filter{{Column: "country", Op: "eq", Values: []string{"DE"}}}
	for _, over := range []string{"all", "range"} {
		from := date(t, "1970-01-01")
		if over == "range" {
			from = p.From
		}
		// The actors with a German event in the days over reads, from a breakdown.
		ids := ask(Query{Kind: "breakdown", Table: "events", Dimensions: []string{"actor_id"}, Limit: 1000,
			Where: append([]Filter{{Column: "actor_id", Op: "is_set"}}, de...), Metrics: []Metric{{Op: "count"}}},
			Params{Project: 1, From: from, To: p.To})
		var actors []string
		for _, r := range ids.Rows {
			actors = append(actors, r[0])
		}
		if len(actors) == 0 {
			t.Fatal("no German actors in the fixture")
		}
		stat := func(where ...Filter) int64 {
			return num(ask(Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count"}}, Where: where}, p).Rows[0][0])
		}
		ever := Filter{Op: "ever", Where: de, Over: over}
		never := Filter{Op: "never", Where: de, Over: over}
		want := stat(Filter{Column: "actor_id", Op: "in", Values: actors})
		if got := stat(ever); got != want {
			t.Errorf("over %s: ever = %d, want %d", over, got, want)
		}
		if got, all := stat(never), stat(Filter{Column: "actor_id", Op: "is_set"}); got+want != all || got == 0 {
			t.Errorf("over %s: never = %d, ever %d, every row with an actor %d", over, got, want, all)
		}
		// In a metric's where, and through AnswerMany.
		q := Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count", As: "n", Where: []Filter{ever}}}}
		if got := num(ask(q, p).Rows[0][0]); got != want {
			t.Errorf("over %s: a metric's ever = %d, want %d", over, got, want)
		}
		var many Answered
		e.AnswerMany(context.Background(), []Request{{Query: q, Params: p, Rules: engineRules}}, func(_ int, a Answered) { many = a })
		if many.Err != nil || num(many.Result.Rows[0][0]) != want {
			t.Errorf("over %s: AnswerMany = %+v, want %d", over, many, want)
		}
	}
}

func TestValidateEver(t *testing.T) {
	de := []Filter{{Column: "country", Op: "eq", Values: []string{"DE"}}}
	st := func(table string, f Filter) Query {
		return Query{Kind: "stat", Table: table, Metrics: []Metric{{Op: "count"}}, Where: []Filter{f}}
	}
	for _, q := range []Query{
		st("views", Filter{Op: "ever", Where: de}),
		st("product", Filter{Op: "never", Where: de, By: "user", Over: "range"}),
		st("sessions", Filter{Op: "ever", Where: de, By: "actor"}),
		{Kind: "retention", Output: "grid", Where: []Filter{{Op: "never", Where: de, By: "group"}}},
	} {
		if err := q.Validate(); err != nil {
			t.Errorf("%+v: %v", q.Where[0], err)
		}
	}
	for _, tc := range []struct {
		name string
		q    Query
		path string
	}{
		{"no where", st("views", Filter{Op: "ever"}), "where[0].where"},
		{"column", st("views", Filter{Op: "ever", Column: "path", Where: de}), "where[0].column"},
		{"bad by", st("views", Filter{Op: "ever", Where: de, By: "session"}), "where[0].by"},
		{"bad over", st("views", Filter{Op: "ever", Where: de, Over: "week"}), "where[0].over"},
		{"user on sessions", st("sessions", Filter{Op: "ever", Where: de, By: "user"}), "where[0].by"},
		{"on identities", st("identities", Filter{Op: "ever", Where: de}), "where[0].op"},
		{"nested", st("views", Filter{Op: "ever", Where: []Filter{{Op: "never", Where: de}}}), "where[0].where[0].op"},
		{"in an any_of", st("views", Filter{Op: "any_of", Any: []Filter{de[0], {Op: "ever", Where: de}}}), "where[0].any[1].op"},
		{"bad inner", st("views", Filter{Op: "ever", Where: []Filter{{Column: "nope", Op: "is_set"}}}), "where[0].where[0].column"},
		{"where without ever", st("views", Filter{Column: "path", Op: "is_set", Where: de}), "where[0].where"},
	} {
		err := tc.q.Validate()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.path+":") {
			t.Errorf("%s: %v, want a refusal at %s", tc.name, err, tc.path)
		}
	}
}

// TestEverComparesItsOwnPeriod: a stat's previous period collects its own
// ids over its days, so its previous_ value is the stat of that period.
func TestEverComparesItsOwnPeriod(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	ever := []Filter{{Op: "ever", Over: "range", Where: []Filter{{Column: "country", Op: "eq", Values: []string{"DE"}}}}}
	q := Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count"}}, Where: ever}
	cmp := q
	cmp.Compare = "previous"
	got, err := e.Answer(context.Background(), cmp, Params{Project: 1, From: date(t, "2026-08-22"), To: date(t, "2026-08-31")}, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	prev, err := e.Answer(context.Background(), q, Params{Project: 1, From: date(t, "2026-08-12"), To: date(t, "2026-08-21")}, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rows[0][1] != prev.Rows[0][0] || got.Rows[0][1] == "0" {
		t.Errorf("previous_count = %s, want the previous period's own %s", got.Rows[0][1], prev.Rows[0][0])
	}
}
