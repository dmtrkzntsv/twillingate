package manage

import (
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
	if !strings.HasPrefix(q, "SELECT id,") || !strings.Contains(q, "WHERE project_id = ? AND form = ?") {
		t.Errorf("query shape: %s", q)
	}
}

func TestSubmissionsQueryQuotesNames(t *testing.T) {
	f := store.Form{Status: store.FormDraft, Fields: []string{`a"b`, "it's", "x.y"}}
	q, cols := SubmissionsQuery(f)
	for _, want := range []string{`AS "a""b"`, `AS "it's"`, `'$."x.y"'`, `key = 'a"b'`, `'$."it''s"'`} {
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
