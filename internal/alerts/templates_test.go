package alerts

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestTemplates(t *testing.T) {
	pathEq := func(p string) native.Filter { return native.Filter{Column: "path", Op: "eq", Values: []string{p}} }
	metric, silence := []string{"threshold", "change"}, []string{"silence"}
	for _, tc := range []struct {
		name    string
		fields  map[string]string
		query   native.Query
		read    store.AlertRead
		silent  bool
		offered []string
	}{
		{
			name:    "event_count",
			fields:  map[string]string{"event": "signup"},
			query:   native.Query{Kind: "stat", Table: "events", Metrics: []native.Metric{{Op: "count"}}, Where: []native.Filter{evt("signup")}},
			read:    store.AlertRead{Metric: "count"},
			offered: metric,
		},
		{
			name:    "event_count",
			fields:  map[string]string{"event": "$page_view", "path": "/pricing"},
			query:   native.Query{Kind: "stat", Table: "events", Metrics: []native.Metric{{Op: "count"}}, Where: []native.Filter{evt("$page_view"), pathEq("/pricing")}},
			read:    store.AlertRead{Metric: "count"},
			offered: metric,
		},
		{
			name:    "unique_people",
			fields:  map[string]string{},
			query:   native.Query{Kind: "stat", Table: "events", Metrics: []native.Metric{{Op: "actors"}}},
			read:    store.AlertRead{Metric: "actors"},
			offered: metric,
		},
		{
			name:    "unique_people",
			fields:  map[string]string{"event": "login", "who": "users"},
			query:   native.Query{Kind: "stat", Table: "events", Metrics: []native.Metric{{Op: "users"}}, Where: []native.Filter{evt("login")}},
			read:    store.AlertRead{Metric: "users"},
			offered: metric,
		},
		{
			name:   "conversion",
			fields: map[string]string{"from_event": "$page_view", "from_path": "/pricing", "to_event": "paid"},
			query: native.Query{Kind: "funnel", Table: "events", By: "actor", Steps: []native.Step{
				{Label: "from", Where: []native.Filter{evt("$page_view"), pathEq("/pricing")}},
				{Label: "to", Where: []native.Filter{evt("paid")}},
			}},
			read:    store.AlertRead{Step: "to", From: "start"},
			offered: metric,
		},
		{
			name:   "conversion",
			fields: map[string]string{"from_event": "$page_view", "from_path": "/a", "to_event": "$page_view", "to_path": "/b", "people": "user"},
			query: native.Query{Kind: "funnel", Table: "events", By: "user", Steps: []native.Step{
				{Label: "from", Where: []native.Filter{evt("$page_view"), pathEq("/a")}},
				{Label: "to", Where: []native.Filter{evt("$page_view"), pathEq("/b")}},
			}},
			read:    store.AlertRead{Step: "to", From: "start"},
			offered: metric,
		},
		{
			name:    "no_events",
			fields:  map[string]string{"event": "$page_view", "path": "/health"},
			query:   native.Query{Kind: "stat", Table: "events", Metrics: []native.Metric{{Op: "max", Column: "received_at"}}, Where: []native.Filter{evt("$page_view"), pathEq("/health")}},
			silent:  true,
			offered: silence,
		},
	} {
		q, read, where, silent, offered, err := fromTemplate(tc.name, tc.fields)
		if err != nil {
			t.Fatalf("%s %v: %v", tc.name, tc.fields, err)
		}
		if !reflect.DeepEqual(q, tc.query) {
			t.Errorf("%s %v: query\n%+v\nwant\n%+v", tc.name, tc.fields, q, tc.query)
		}
		if read != tc.read || silent != tc.silent || !reflect.DeepEqual(offered, tc.offered) {
			t.Errorf("%s %v: read %+v silent %v offered %v", tc.name, tc.fields, read, silent, offered)
		}
		if !reflect.DeepEqual(where, tc.query.Where) {
			t.Errorf("%s %v: where %+v, want the query's", tc.name, tc.fields, where)
		}
		if err := q.Validate(); err != nil {
			t.Errorf("%s %v: the built query is invalid: %v", tc.name, tc.fields, err)
		}
	}
}

func TestTemplatesBuild(t *testing.T) {
	// event_count takes a change, not a silence.
	ec := map[string]string{"event": "signup"}
	def := mustBuild(t, Input{Template: "event_count", Fields: ec, Condition: drop30})
	if def.Template != "event_count" || !reflect.DeepEqual(def.Fields, ec) || def.Read != (store.AlertRead{Metric: "count"}) {
		t.Errorf("event_count def = %+v", def)
	}
	_, err := Build(Input{Template: "event_count", Fields: ec, Condition: silent6})
	mustInvalid(t, err, "event_count", "silence")

	// no_events takes only a silence, and builds silence's stat.
	def = mustBuild(t, Input{Template: "no_events", Fields: map[string]string{"event": "heartbeat"}, Condition: silent6})
	var q native.Query
	if err := json.Unmarshal(def.Query, &q); err != nil {
		t.Fatal(err)
	}
	if len(q.Metrics) != 1 || !reflect.DeepEqual(q.Metrics[0], native.Metric{Op: "max", Column: "received_at"}) || !reflect.DeepEqual(q.Where, []native.Filter{evt("heartbeat")}) {
		t.Errorf("no_events query = %+v", q)
	}
	_, err = Build(Input{Template: "no_events", Fields: map[string]string{"event": "heartbeat"}, Condition: below5})
	mustInvalid(t, err, "no_events", "threshold")

	// A conversion step on a product event may filter on its path.
	conv := Input{Template: "conversion", Fields: map[string]string{"from_event": "$form_submit", "from_path": "/contact", "to_event": "paid"}, Condition: below5}
	p := mustParse(t, conv)
	if want := []native.Filter{evt("$form_submit"), {Column: "path", Op: "eq", Values: []string{"/contact"}}}; !reflect.DeepEqual(p.Query.Steps[0].Where, want) {
		t.Errorf("conversion from step = %+v, want %+v", p.Query.Steps[0].Where, want)
	}

	// conversion stores 7 days by default.
	conv = Input{Template: "conversion", Fields: map[string]string{"from_event": "signup", "to_event": "paid"}, Condition: below5}
	if def := mustBuild(t, conv); def.WindowDays != 7 || def.Read != (store.AlertRead{Step: "to", From: "start"}) {
		t.Errorf("conversion def = %+v", def)
	}

	// Empty fields are stored as none, so a rebuild compares equal.
	def = mustBuild(t, Input{Template: "unique_people", Fields: map[string]string{}, Condition: below5})
	if def.Fields != nil {
		t.Errorf("empty fields stored as %#v", def.Fields)
	}
}

func TestTemplatesRefuse(t *testing.T) {
	for name, tc := range map[string]struct {
		template string
		fields   map[string]string
	}{
		"unknown template":           {"spikes", map[string]string{}},
		"event_count no event":       {"event_count", map[string]string{}},
		"event_count path off views": {"event_count", map[string]string{"event": "signup", "path": "/x"}},
		"event_count unknown field":  {"event_count", map[string]string{"event": "signup", "who": "users"}},
		"unique_people who":          {"unique_people", map[string]string{"who": "everyone"}},
		"unique_people path":         {"unique_people", map[string]string{"event": "$page_view", "path": "/x"}},
		"conversion no to_event":     {"conversion", map[string]string{"from_event": "signup"}},
		"conversion no from_event":   {"conversion", map[string]string{"to_event": "paid"}},
		"conversion people":          {"conversion", map[string]string{"from_event": "a", "to_event": "b", "people": "group"}},
		"no_events no event":         {"no_events", map[string]string{"path": "/x"}},
		"no_events path off views":   {"no_events", map[string]string{"event": "beat", "path": "/x"}},
	} {
		if _, _, _, _, _, err := fromTemplate(tc.template, tc.fields); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}
