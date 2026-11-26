package reporting

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/reporting/sqlbind"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Selector is one of a dashboard's selectors: a picker of Column's
// values above the page, which narrows the widgets that follow it. Name
// is the SQL parameter (:name) and the key a selection is sent under;
// Label is what the picker shows.
type Selector struct {
	Name   string `json:"name" jsonschema:"a lower-case letter, then up to 31 lower-case letters, digits or _; unique on the dashboard and not project, from or to. SQL reads it as :name, a VARCHAR[] of the picked values, empty for All"`
	Label  string `json:"label,omitempty" jsonschema:"what the picker shows; default the column without attr_"`
	Column string `json:"column" jsonschema:"a text filter column of events (utm_campaign, host, country, device_label, …) or attr_<key> for any attribute key; not a day, number or def_ column"`
}

// BoundSelector is one of a widget's dashboard's selectors as a load
// binds it: its name and column, and the picked values (nil: All).
type BoundSelector struct {
	Name, Column string
	Values       []string
}

// maxSelectors bounds a dashboard's selectors.
const maxSelectors = 5

// selectorName is what a selector's name must match.
var selectorName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// switcherParams are the names a selector may not take: the parameters
// the project and range switchers bind, with what owns each.
var switcherParams = map[string]string{"project": "the project switcher's", "from": "the date range's", "to": "the date range's"}

// sessionSelectorColumns are the selector columns a query on sessions
// follows: the ones that mean the same there as on events. A session
// carries others (kind, day, actor_id) that mean something else.
var sessionSelectorColumns = map[string]bool{"utm_source": true, "utm_medium": true, "utm_campaign": true, "referrer_source": true}

// parseSelectors reads a dashboard's stored selectors (store.Dashboard's
// Selectors): "" and "[]" read as none, an empty non-nil list.
func parseSelectors(raw string) ([]Selector, error) {
	out := []Selector{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	if out == nil { // "null"
		out = []Selector{}
	}
	return out, nil
}

// storedSelectors is parseSelectors for a read that has no error to
// return: a row the store holds was checked when it was written, so one
// that does not parse reads as none.
func storedSelectors(raw string) []Selector {
	sels, err := parseSelectors(raw)
	if err != nil {
		return []Selector{}
	}
	return sels
}

// selectorsText is sels as the store keeps them.
func selectorsText(sels []Selector) string {
	if len(sels) == 0 {
		return "[]"
	}
	b, err := json.Marshal(sels)
	if err != nil { // three strings always marshal
		panic(err)
	}
	return string(b)
}

// checkSelectors refuses selectors that break the rules (at most five; a
// name of the pattern, unique and not a switcher's; a text column of
// events) and returns them with names, labels and columns trimmed and an
// empty label filled in from the column. None is an empty, non-nil list.
func checkSelectors(in []Selector) ([]Selector, error) {
	if len(in) > maxSelectors {
		return nil, store.Refuse(store.ErrInvalid, "selectors: a dashboard takes at most %d, got %d", maxSelectors, len(in))
	}
	out := make([]Selector, len(in))
	for i, s := range in {
		s.Name, s.Label, s.Column = strings.TrimSpace(s.Name), strings.TrimSpace(s.Label), strings.TrimSpace(s.Column)
		if !selectorName.MatchString(s.Name) {
			return nil, store.Refuse(store.ErrInvalid,
				"selectors[%d].name: %q is not a lower-case letter followed by up to 31 lower-case letters, digits or _", i, s.Name)
		}
		if owner, ok := switcherParams[s.Name]; ok {
			return nil, store.Refuse(store.ErrInvalid, "selectors[%d].name: :%s is %s; pick another name", i, s.Name, owner)
		}
		if j := slices.IndexFunc(out[:i], func(o Selector) bool { return o.Name == s.Name }); j >= 0 {
			return nil, store.Refuse(store.ErrInvalid, "selectors[%d].name: %s is already selectors[%d]'s", i, s.Name, j)
		}
		if err := checkSelectorColumn(i, s.Column); err != nil {
			return nil, err
		}
		if s.Label == "" {
			s.Label = strings.TrimPrefix(s.Column, "attr_")
		}
		out[i] = s
	}
	return out, nil
}

// checkSelectorColumn refuses column, selectors[i]'s, unless the native
// engine takes it as a breakdown's dimension on events and types it text.
func checkSelectorColumn(i int, column string) error {
	if column == "" {
		return store.Refuse(store.ErrInvalid, "selectors[%d].column is required", i)
	}
	if strings.HasPrefix(column, "def_") {
		return store.Refuse(store.ErrInvalid, "selectors[%d].column: %s is a column a query defines; a selector reads a column of events", i, column)
	}
	q := native.Query{Kind: "breakdown", Table: "events", Dimensions: []string{column}, Metrics: []native.Metric{{Op: "count"}}}
	if q.Validate() != nil {
		return store.Refuse(store.ErrInvalid, "selectors[%d].column: %q is not a filter column of events", i, column)
	}
	if t := q.ColumnTypes()[0]; t != "text" {
		return store.Refuse(store.ErrInvalid, "selectors[%d].column: %s is a %s column; a selector picks text values", i, column, t)
	}
	return nil
}

// selectorNames is sels' names, in order: the parameters a sql widget on
// their dashboard may read beside :project, :from and :to.
func selectorNames(sels []Selector) []string {
	out := make([]string, len(sels))
	for i, s := range sels {
		out[i] = s.Name
	}
	return out
}

// followedSelectors is the names, in sels' order, of the selectors w
// follows: a query widget each one whose filter its query takes (on
// sessions only sessionSelectorColumns, on identities none), a sql
// widget each one whose :name it reads, anything else none. Never nil.
func followedSelectors(w store.Widget, sels []Selector) []string {
	out := []string{}
	switch w.SourceType {
	case "sql":
		params, err := sqlbind.Params(w.Source)
		if err != nil {
			return out
		}
		for _, s := range sels {
			if slices.Contains(params, ":"+s.Name) {
				out = append(out, s.Name)
			}
		}
	case "query":
		var qc queryContent
		if json.Unmarshal([]byte(w.Source), &qc) != nil {
			return out
		}
		for _, s := range sels {
			if queryFollows(qc.Query, s.Column) {
				out = append(out, s.Name)
			}
		}
	}
	return out
}

// queryFollows reports whether q takes a filter on column: on sessions
// only sessionSelectorColumns, on identities none, and otherwise
// whatever the native engine accepts ahead of q's own filters.
func queryFollows(q native.Query, column string) bool {
	if q.Table == "identities" || (q.Table == "sessions" && !sessionSelectorColumns[column]) {
		return false
	}
	q.Where = append([]native.Filter{{Column: column, Op: "in", Values: []string{"x"}}}, q.Where...)
	return q.Validate() == nil
}
