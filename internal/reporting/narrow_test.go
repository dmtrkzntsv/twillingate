package reporting

import (
	"errors"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestNarrowed(t *testing.T) {
	de := native.Filter{Column: "country", Op: "eq", Values: []string{"DE"}}
	q := store.Widget{Name: "bars", SourceType: "query", Source: countryBars}
	got, err := narrowed(q, Narrow{Where: []native.Filter{de}, ProjectID: 9})
	if err != nil {
		t.Fatal(err)
	}
	qc, _, _, err := parseQuery(got.Source)
	if err != nil {
		t.Fatal(err)
	}
	if qc.Project != 9 || len(qc.Query.Where) != 1 || qc.Query.Where[0].Column != "country" || qc.Rename["actors"] != "value" {
		t.Errorf("narrowed content = %s", got.Source)
	}
	if same, _ := narrowed(q, Narrow{}); same.Source != q.Source {
		t.Error("an empty Narrow changed the widget")
	}
	md := store.Widget{Name: "about", SourceType: "md", Source: "**hi**"}
	if same, err := narrowed(md, Narrow{Where: []native.Filter{de}}); err != nil || same.Source != md.Source {
		t.Errorf("Markdown narrowed = %v, %v", same, err)
	}
	for _, tc := range []struct {
		w    store.Widget
		n    Narrow
		want string
	}{
		{store.Widget{Name: "raw", SourceType: "sql", Source: "SELECT 1 AS value"}, Narrow{ProjectID: 1}, "widget raw is sql"},
		{q, Narrow{Where: []native.Filter{{Column: "nope", Op: "is_set"}}}, "widget bars: query.where[0].column"},
		{q, Narrow{ProjectID: -1}, "project_id"},
	} {
		_, err := narrowed(tc.w, tc.n)
		if !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("narrowed(%s, %+v) = %v, want %q", tc.w.Name, tc.n, err, tc.want)
		}
	}
}
