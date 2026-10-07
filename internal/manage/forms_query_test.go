package manage

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestSubmissionsQueryColumns(t *testing.T) {
	fixed := []string{"Page", "Referrer", "UTM source", "UTM medium", "UTM campaign"}
	draft := store.Form{Status: store.FormDraft, Fields: []string{"email", "note"}}
	_, cols := SubmissionsQuery(draft)
	if want := append([]string{"Received", "email", "note"}, fixed...); !reflect.DeepEqual(cols, want) {
		t.Errorf("draft columns = %q, want %q", cols, want)
	}
	// An approved form shows its expected fields, in their order, not every field seen.
	approved := store.Form{Status: store.FormApproved, Fields: []string{"email", "note", "spam"},
		ExpectedFields: []string{"note", "email"}}
	_, cols = SubmissionsQuery(approved)
	if want := append([]string{"Received", "note", "email"}, fixed...); !reflect.DeepEqual(cols, want) {
		t.Errorf("approved columns = %q, want %q", cols, want)
	}
	// A field named like a fixed column, or id, is shown as "<name> (field)";
	// so is one that differs only in case, since SQLite's column names are
	// case-insensitive and the two would read one column.
	clash := store.Form{Status: store.FormDraft, Fields: []string{"Page", "id", "utm source", "received"}}
	q, cols := SubmissionsQuery(clash)
	want := append([]string{"Received", "Page (field)", "id (field)", "utm source (field)", "received (field)"}, fixed...)
	if !reflect.DeepEqual(cols, want) {
		t.Errorf("clash columns = %q, want %q", cols, want)
	}
	// Renamed names stay unique too.
	for _, c := range []struct{ fields, want []string }{
		{[]string{"Page", "Page (field)"}, []string{"Page (field)", "Page (field) (field)"}},
		{[]string{"Email", "email"}, []string{"Email", "email (field)"}},
	} {
		_, cols := SubmissionsQuery(store.Form{Status: store.FormDraft, Fields: c.fields})
		if got := cols[1 : 1+len(c.fields)]; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: columns %q, want %q", c.fields, got, c.want)
		}
	}
	if !strings.HasPrefix(q, "SELECT id AS c0,") || !strings.Contains(q, "WHERE project_id = ? AND form = ?") {
		t.Errorf("query shape: %s", q)
	}
}

func TestSubmissionsQueryQuotesNames(t *testing.T) {
	f := store.Form{Status: store.FormDraft, Fields: []string{`a"b`, "it's", "x.y"}}
	q, cols := SubmissionsQuery(f)
	for _, want := range []string{`AS c2`, `AS c3`, `'$."x.y"'`, `key = 'a"b'`, `'$."it''s"'`} {
		if !strings.Contains(q, want) {
			t.Errorf("query lacks %s:\n%s", want, q)
		}
	}
	if cols[1] != `a"b` || cols[2] != "it's" {
		t.Errorf("columns = %q", cols)
	}
}

func TestCSVSafe(t *testing.T) {
	for in, want := range map[string]string{
		"=1+1": "'=1+1", "+x": "'+x", "-y": "'-y", "@z": "'@z", "\tt": "'\tt", "\rr": "'\rr",
		"plain": "plain", "": "", "a=b": "a=b", " =x": " =x",
	} {
		if got := CSVSafe(in); got != want {
			t.Errorf("CSVSafe(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCSVSafeTable(t *testing.T) {
	h, rows := CSVSafeTable([]string{"=a", "b"}, [][]string{{"x", "@y"}, {"-z", ""}})
	if !reflect.DeepEqual(h, []string{"'=a", "b"}) ||
		!reflect.DeepEqual(rows, [][]string{{"x", "'@y"}, {"'-z", ""}}) {
		t.Fatalf("got %q %q", h, rows)
	}
}

// TestSubmissionsQueryNamesColumnsByPosition: no display name, and so no
// field name, is ever an SQL identifier; a field reaches the SQL only in
// a quoted JSON path. readsql refuses meta, dbstat, sqlite_* and pragma_*
// as names, so a field called that would otherwise break its table.
func TestSubmissionsQueryNamesColumnsByPosition(t *testing.T) {
	f := store.Form{Status: store.FormDraft, Fields: []string{"Meta", "dbstat", "meta", "pragma_y", "sqlite_x"}}
	q, cols := SubmissionsQuery(f)
	if want := []string{"Received", "Meta", "dbstat", "meta (field)", "pragma_y", "sqlite_x"}; !reflect.DeepEqual(cols[:6], want) {
		t.Fatalf("columns = %q", cols)
	}
	for i := range len(cols) + 1 {
		if want := fmt.Sprintf(" AS c%d", i); !strings.Contains(q, want) {
			t.Errorf("query lacks %q:\n%s", want, q)
		}
	}
	for _, c := range cols {
		if strings.Contains(q, `"`+c+`"`) && !strings.Contains(q, `'$."`+c+`"'`) {
			t.Errorf("query names %q outside a JSON path:\n%s", c, q)
		}
	}
	if strings.Contains(q, "AS \"") {
		t.Errorf("query quotes an identifier:\n%s", q)
	}
}
