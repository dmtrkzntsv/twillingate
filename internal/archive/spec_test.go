package archive

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// specTree is a raw/ directory with every kind of file ListFiles reads:
// project 1 a month, days, a segment, the three kinds of log, a dump and a
// merge left behind, current and stale session files, a current identities
// file and a stale aliases one; project 2 a log only; project 3 nothing.
func specTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	touch(t, dir,
		"1/2026-08.parquet", "1/sessions/2026-08.parquet", // current
		"1/2026-09.parquet", "1/2026-09-30.parquet", // a merge left its day: left out
		"1/sessions/2026-09.parquet",                            // stale
		"1/2026-10-01.parquet", "1/sessions/2026-10-01.parquet", // current
		"1/2026-10-02.parquet", "1/2026-10-02.1.parquet", // a segment
		"1/2026-10-03.parquet", "1/2026-10-03.parquet.dumping", // a dump left its log: left out
		"1/2026-10-04.1.parquet.dumping",
		"1/2026-10-05.1.closed",
		"1/2026-10-06.log",
		"1/identities.parquet", "1/aliases.parquet",
		"2/2026-10-06.log",
		"3/notes.txt",
	)
	base := time.Now().Add(-24 * time.Hour)
	age(t, filepath.Join(dir, "1"), base, map[string]int{
		"2026-08.parquet": 0, "sessions/2026-08.parquet": 1,
		"2026-09.parquet": 5, "sessions/2026-09.parquet": 1,
		"2026-10-01.parquet": 0, "sessions/2026-10-01.parquet": 1,
		"2026-10-02.parquet": 0, "2026-10-02.1.parquet": 0, "2026-10-03.parquet": 0,
		"identities.parquet": 6, "aliases.parquet": 2,
	})
	return dir
}

func specRules() Rules {
	return Rules{
		Events: []store.EventRule{
			{ProjectID: 2, Family: store.FamilyProduct, Name: "b", Action: store.EventRuleBlock, CreatedAt: "x"},
			{ProjectID: 1, Family: store.FamilyProduct, Name: "a", Action: store.EventRuleRename, To: "A"},
		},
		Attributes: []store.AttributeRule{
			{ProjectID: 2, Key: "k", Action: store.EventRuleRename, To: "K"},
			{ProjectID: 1, Key: "x", Action: store.EventRuleBlock},
		},
	}
}

// TestTableSpecListsWhatListFilesLists: the spec is ListFiles' listing with
// paths relative to raw/, projects and rules in order.
func TestTableSpecListsWhatListFilesLists(t *testing.T) {
	dir := specTree(t)
	got, err := TableSpec(dir, nil, specRules())
	if err != nil {
		t.Fatal(err)
	}
	want := sqlengine.Tables{Version: 1,
		Projects: []sqlengine.Project{{ID: 1,
			Parquet:     []string{"1/2026-08.parquet", "1/2026-09.parquet", "1/2026-10-01.parquet", "1/2026-10-02.1.parquet", "1/2026-10-02.parquet", "1/2026-10-03.parquet"},
			Logs:        []string{"1/2026-10-04.1.parquet.dumping", "1/2026-10-05.1.closed", "1/2026-10-06.log"},
			Sessions:    []string{"1/sessions/2026-08.parquet", "1/sessions/2026-10-01.parquet"},
			SessionGaps: []sqlengine.Gap{{From: "2026-09-01", To: "2026-09-30"}, {From: "2026-10-02", To: "2026-10-06"}},
			Identities:  "1/identities.parquet",
		}, {ID: 2, Parquet: []string{}, Logs: []string{"2/2026-10-06.log"}, Sessions: []string{},
			SessionGaps: []sqlengine.Gap{{From: "2026-10-06", To: "2026-10-06"}}}},
		Rules: sqlengine.Rules{
			Events: []sqlengine.EventRule{
				{ProjectID: 1, Family: "product", Name: "a", Action: "rename", To: "A"},
				{ProjectID: 2, Family: "product", Name: "b", Action: "block"},
			},
			Attributes: []sqlengine.AttributeRule{
				{ProjectID: 1, Key: "x", Action: "block"},
				{ProjectID: 2, Key: "k", Action: "rename", To: "K"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(want, "", " ")
		t.Fatalf("spec =\n%s\nwant\n%s", g, w)
	}
}

// TestTableSpecScopesToProjects: with projects, only theirs are listed,
// with their rules; a project with no files is left out.
func TestTableSpecScopesToProjects(t *testing.T) {
	dir := specTree(t)
	got, err := TableSpec(dir, []int64{2, 3}, specRules())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Projects) != 1 || got.Projects[0].ID != 2 {
		t.Errorf("projects = %+v, want project 2 only", got.Projects)
	}
	if len(got.Rules.Events) != 1 || got.Rules.Events[0].ProjectID != 2 || len(got.Rules.Attributes) != 1 || got.Rules.Attributes[0].ProjectID != 2 {
		t.Errorf("rules = %+v, want project 2's only", got.Rules)
	}
	none, err := TableSpec(dir, []int64{}, specRules())
	if err != nil || len(none.Projects) != 0 || len(none.Rules.Events)+len(none.Rules.Attributes) != 0 {
		t.Errorf("no projects = %+v, %v; want an empty spec", none, err)
	}
	b, _ := json.Marshal(none)
	if want := `{"version":1,"projects":[],"rules":{"events":[],"attributes":[]}}`; string(b) != want {
		t.Errorf("empty spec = %s, want %s", b, want)
	}
}

// TestTablePathsStayInside: TableSpec refuses a path outside raw/.
func TestTablePathsStayInside(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{filepath.Join(filepath.Dir(dir), "x.parquet"), dir, "/etc/passwd"} {
		if _, err := relPath(dir, p); !errors.Is(err, ErrTables) {
			t.Errorf("relPath(%q) = %v, want ErrTables", p, err)
		}
	}
	if r, err := relPath(dir, filepath.Join(dir, "1", "sessions", "2026-10.parquet")); err != nil || r != "1/sessions/2026-10.parquet" {
		t.Errorf("relPath inside = %q, %v", r, err)
	}
}
