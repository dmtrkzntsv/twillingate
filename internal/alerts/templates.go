package alerts

import (
	"slices"
	"sort"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// pageView is the event a page view is stored under: event_count and
// no_events narrow it alone by path (a product event carries its last
// view's path too, which a conversion step may filter on).
const pageView = "$page_view"

// templateFields are each template's fields; a field not listed is
// refused.
var templateFields = map[string][]string{
	"event_count":   {"event", "path"},
	"unique_people": {"event", "who"},
	"conversion":    {"from_event", "from_path", "to_event", "to_path", "people"},
	"no_events":     {"event", "path"},
}

// The conditions each kind of template offers.
var (
	metricConditions  = []string{condThreshold, condChange}
	silenceConditions = []string{condSilence}
)

// fromTemplate builds template name's query and read from its fields f
// (an empty value is an absent one): the query, the read, the where it
// filters on (the query's own; a silence alert's events), whether it is
// a silence alert, and the condition types it offers.
func fromTemplate(name string, f map[string]string) (native.Query, store.AlertRead, []native.Filter, bool, []string, error) {
	fail := func(err error) (native.Query, store.AlertRead, []native.Filter, bool, []string, error) {
		return native.Query{}, store.AlertRead{}, nil, false, nil, err
	}
	allowed, ok := templateFields[name]
	if !ok {
		names := make([]string, 0, len(templateFields))
		for n := range templateFields {
			names = append(names, n)
		}
		sort.Strings(names)
		return fail(invalid("template: %q is not one of %s", name, strings.Join(names, ", ")))
	}
	f = cleanFields(f)
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !slices.Contains(allowed, k) {
			return fail(invalid("fields.%s: template %s takes %s", k, name, strings.Join(allowed, ", ")))
		}
	}

	switch name {
	case "event_count", "no_events":
		where, err := eventWhere(name, "event", "path", f, true)
		if err != nil {
			return fail(err)
		}
		if name == "no_events" {
			return silenceQuery(where), store.AlertRead{}, where, true, silenceConditions, nil
		}
		q := native.Query{Kind: "stat", Table: "events", Metrics: []native.Metric{{Op: "count"}}, Where: where}
		return q, store.AlertRead{Metric: "count"}, where, false, metricConditions, nil
	case "unique_people":
		op := f["who"]
		switch op {
		case "":
			op = "actors"
		case "actors", "users":
		default:
			return fail(invalid("fields.who: %q is not actors (visitors) or users (signed-in users)", op))
		}
		var where []native.Filter
		if ev := f["event"]; ev != "" {
			where = []native.Filter{eventIs(ev)}
		}
		q := native.Query{Kind: "stat", Table: "events", Metrics: []native.Metric{{Op: op}}, Where: where}
		return q, store.AlertRead{Metric: op}, where, false, metricConditions, nil
	}
	// conversion
	by := f["people"]
	switch by {
	case "":
		by = "actor"
	case "actor", "user":
	default:
		return fail(invalid("fields.people: %q is not actor or user", by))
	}
	from, err := eventWhere(name, "from_event", "from_path", f, false)
	if err != nil {
		return fail(err)
	}
	to, err := eventWhere(name, "to_event", "to_path", f, false)
	if err != nil {
		return fail(err)
	}
	q := native.Query{Kind: "funnel", Table: "events", By: by, Steps: []native.Step{
		{Label: "from", Where: from},
		{Label: "to", Where: to},
	}}
	return q, store.AlertRead{Step: "to", From: "start"}, nil, false, metricConditions, nil
}

// eventWhere is the filters on f's event field, and on its path field
// when set, which with viewsOnly only a page view takes.
func eventWhere(template, eventField, pathField string, f map[string]string, viewsOnly bool) ([]native.Filter, error) {
	ev := f[eventField]
	if ev == "" {
		return nil, invalid("fields.%s: template %s needs an event", eventField, template)
	}
	where := []native.Filter{eventIs(ev)}
	if path := f[pathField]; path != "" {
		if viewsOnly && ev != pageView {
			return nil, invalid("fields.%s: a path narrows %s only, and %s is not one", pathField, pageView, ev)
		}
		where = append(where, native.Filter{Column: "path", Op: "eq", Values: []string{path}})
	}
	return where, nil
}

func eventIs(name string) native.Filter {
	return native.Filter{Column: "event_name", Op: "eq", Values: []string{name}}
}
