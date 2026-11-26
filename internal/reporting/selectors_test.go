package reporting

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// TestCheckSelectors: a selector names a lower-case parameter, unique on
// the dashboard and not one of the switchers', over a text filter column
// of events (attr_<key> for any key); at most five; an empty label is the
// column without attr_.
func TestCheckSelectors(t *testing.T) {
	sel := func(name, column string) Selector { return Selector{Name: name, Column: column} }
	for _, col := range []string{"attr_deployment", "utm_campaign", "host", "device_label", "attr_no_event_sent_this"} {
		if _, err := checkSelectors([]Selector{sel("x", col)}); err != nil {
			t.Errorf("column %s: %v, want accepted", col, err)
		}
	}
	got, err := checkSelectors([]Selector{sel("deployment", "attr_deployment"), {Name: "campaign", Label: " Campaign ", Column: "utm_campaign"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []Selector{{Name: "deployment", Label: "deployment", Column: "attr_deployment"}, {Name: "campaign", Label: "Campaign", Column: "utm_campaign"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("checked = %+v, want %+v", got, want)
	}
	if got, err := checkSelectors(nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("none = %#v, %v; want an empty, non-nil list", got, err)
	}

	six := make([]Selector, 6)
	for i := range six {
		six[i] = sel(string(rune('a'+i)), "host")
	}
	for name, c := range map[string]struct {
		in  []Selector
		msg string
	}{
		"day":             {[]Selector{sel("x", "day")}, "selectors[0].column: day is a day column; a selector picks text values"},
		"user_first_seen": {[]Selector{sel("x", "user_first_seen")}, "selectors[0].column: user_first_seen is a day column; a selector picks text values"},
		"display_width":   {[]Selector{sel("x", "display_width")}, "selectors[0].column: display_width is a number column; a selector picks text values"},
		"def_":            {[]Selector{sel("x", "def_x")}, "selectors[0].column: def_x is a column a query defines; a selector reads a column of events"},
		"unknown":         {[]Selector{sel("x", "nope")}, `selectors[0].column: "nope" is not a filter column of events`},
		"empty column":    {[]Selector{sel("x", "")}, "selectors[0].column is required"},
		"duplicate":       {[]Selector{sel("a", "host"), sel("a", "utm_campaign")}, "selectors[1].name: a is already selectors[0]'s"},
		"project":         {[]Selector{sel("project", "host")}, "selectors[0].name: :project is the project switcher's; pick another name"},
		"from":            {[]Selector{sel("from", "host")}, "selectors[0].name: :from is the date range's; pick another name"},
		"to":              {[]Selector{sel("to", "host")}, "selectors[0].name: :to is the date range's; pick another name"},
		"Bad-Name":        {[]Selector{sel("Bad-Name", "host")}, `selectors[0].name: "Bad-Name" is not a lower-case letter followed by up to 31 lower-case letters, digits or _`},
		"33 characters":   {[]Selector{sel("a"+strings.Repeat("b", 32), "host")}, `selectors[0].name: "a` + strings.Repeat("b", 32) + `" is not a lower-case letter followed by up to 31 lower-case letters, digits or _`},
		"six":             {six, "selectors: a dashboard takes at most 5, got 6"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := checkSelectors(c.in)
			wantRefusal(t, err, store.ErrInvalid, c.msg)
		})
	}
	if _, err := checkSelectors([]Selector{sel("a"+strings.Repeat("b", 31), "host")}); err != nil {
		t.Errorf("a 32-character name: %v, want accepted", err)
	}
}

// TestParseSelectors: the stored JSON reads back; "" and "[]" read as
// none, an empty non-nil list.
func TestParseSelectors(t *testing.T) {
	for _, raw := range []string{"", "[]"} {
		got, err := parseSelectors(raw)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("parseSelectors(%q) = %#v, %v; want an empty list", raw, got, err)
		}
	}
	got, err := parseSelectors(`[{"name":"d","label":"D","column":"attr_d"}]`)
	if err != nil || !reflect.DeepEqual(got, []Selector{{Name: "d", Label: "D", Column: "attr_d"}}) {
		t.Errorf("parsed = %+v, %v", got, err)
	}
	if _, err := parseSelectors("{"); err == nil {
		t.Error("malformed JSON: err = nil")
	}
}

// TestFollowedSelectors: a query widget follows a selector when its
// table takes the filter (sessions only the UTM and referrer columns and
// host, through entry_host; identities none), a sql widget the selectors
// whose :name it reads, md none.
func TestFollowedSelectors(t *testing.T) {
	sels := []Selector{
		{Name: "deployment", Label: "Deployment", Column: "attr_deployment"},
		{Name: "campaign", Label: "Campaign", Column: "utm_campaign"},
		{Name: "host", Label: "Host", Column: "host"},
	}
	query := func(table string) store.Widget {
		q := native.Query{Kind: "stat", Table: table, Metrics: []native.Metric{{Op: "count"}}}
		b, err := json.Marshal(queryContent{Query: q})
		if err != nil {
			t.Fatal(err)
		}
		return store.Widget{SourceType: "query", Source: string(b)}
	}
	// The native engine is the oracle for measures: whatever it takes.
	measures := []string{}
	for _, s := range sels {
		q := native.Query{Kind: "stat", Table: "measures", Metrics: []native.Metric{{Op: "count"}},
			Where: []native.Filter{{Column: s.Column, Op: "in", Values: []string{"x"}}}}
		if q.Validate() == nil {
			measures = append(measures, s.Name)
		}
	}
	if len(measures) == 0 || measures[0] != "deployment" {
		t.Fatalf("measures takes %v; the test expects it to take attr_deployment", measures)
	}
	for name, c := range map[string]struct {
		w    store.Widget
		want []string
	}{
		"events":       {query("events"), []string{"deployment", "campaign", "host"}},
		"views":        {query("views"), []string{"deployment", "campaign", "host"}},
		"sessions":     {query("sessions"), []string{"campaign", "host"}},
		"identities":   {query("identities"), []string{}},
		"measures":     {query("measures"), measures},
		"sql":          {store.Widget{SourceType: "sql", Source: "SELECT count(*) AS value FROM events WHERE len(:deployment) = 0 AND day >= :from"}, []string{"deployment"}},
		"sql neither":  {store.Widget{SourceType: "sql", Source: "SELECT 1 AS value"}, []string{}},
		"md":           {store.Widget{SourceType: "md", Source: "uses :deployment"}, []string{}},
		"broken query": {store.Widget{SourceType: "query", Source: "{"}, []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			got := followedSelectors(c.w, sels)
			if got == nil || !reflect.DeepEqual(got, c.want) {
				t.Errorf("followed = %#v, want %#v", got, c.want)
			}
		})
	}
	if got := followedSelectors(query("events"), nil); got == nil || len(got) != 0 {
		t.Errorf("no selectors: %#v, want an empty list", got)
	}
}
