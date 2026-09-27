package reporting

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestSQLFollows(t *testing.T) {
	s := &sqlSource{}
	for _, tt := range []struct {
		content     string
		wantProject bool
		wantRng     bool
	}{
		{"select :project as p", true, false},
		{"select :from as f", false, true},
		{"select 1", false, false},
	} {
		project, rng := s.Follows(tt.content)
		if project != tt.wantProject || rng != tt.wantRng {
			t.Errorf("Follows(%q) = (%v,%v), want (%v,%v)", tt.content, project, rng, tt.wantProject, tt.wantRng)
		}
	}
}

func TestSQLValidateRefusesUnsupportedParams(t *testing.T) {
	s := &sqlSource{} // no db: refused before it would ever be reached
	comps := testComponents(t)
	line := comps["line"]

	for _, content := range []string{
		"select :path as x, 1 as y",
		"select ? as x, 1 as y",
	} {
		err := s.Validate(context.Background(), content, line)
		if !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("Validate(%q) = %v, want ErrInvalid", content, err)
		}
		if !strings.Contains(err.Error(), "widgets get only :project, :from and :to") {
			t.Errorf("Validate(%q) = %q, want the :path/? refusal message", content, err.Error())
		}
	}
}

func TestSQLValidateRefusesReadOfMeta(t *testing.T) {
	s := &sqlSource{}
	comps := testComponents(t)
	err := s.Validate(context.Background(), "select * from meta", comps["table"])
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestSQLValidateRefusesSyntaxError(t *testing.T) {
	db := newTestReadDB(t)
	s := &sqlSource{db: db}
	comps := testComponents(t)
	err := s.Validate(context.Background(), "select from", comps["table"])
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	// SQLite's own text must come through, not be swallowed by a generic
	// "invalid SQL" message: it's what tells a person which token it
	// stumbled on.
	if !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("err = %q, want SQLite's own syntax error text", err.Error())
	}
}

// TestSQLValidateBindsSampleValuesOnBothRuns pins the fix for the
// finding that the LIMIT 0 shape check ran with no bound arguments at
// all: modernc's driver errors "missing named argument \"project\"" for
// a statement that mentions :project but was given nothing to bind it
// to, so every widget using :project/:from/:to was refused outright —
// on the very first run, regardless of sampleRows. The sample project
// and date range must be computed and bound before that first run, not
// only before the sampleRows-gated second one.
func TestSQLValidateBindsSampleValuesOnBothRuns(t *testing.T) {
	st, db := newTestStoreAndReadDB(t)
	pid := mustCreateProject(t, st, "blog")
	mustWriteEvent(t, st, "e1", pid, store.FamilyViews, "2026-08-20")
	mustWriteEvent(t, st, "e2", pid, store.FamilyViews, "2026-08-21")

	comps := testComponents(t)
	line := comps["line"]
	content := `select day as x, count(*) as y from raw_views
		where project_id = :project and day >= :from and day <= :to group by day`

	for _, sampleRows := range []bool{false, true} {
		s := &sqlSource{db: db, sampleRows: sampleRows}
		if err := s.Validate(context.Background(), content, line); err != nil {
			t.Errorf("sampleRows=%v: Validate = %v, want nil", sampleRows, err)
		}
	}
}

// TestSQLValidateTimesOut pins the API_QUERY_TIMEOUT refusal. The
// content is a single-row aggregate (COUNT(*)) over an effectively
// unbounded recursive CTE: SQLite's planner answers a wrapping "LIMIT 0"
// without evaluating the aggregate at all (there's nothing a caller
// asking for zero rows needs it for), so the LIMIT 0 shape check itself
// returns instantly regardless of how expensive the query is — it's the
// sampleRows-gated LIMIT 5 run that actually has to compute the one row
// COUNT(*) produces, and that must time out and be reported with the
// environment variable a person can act on, not SQLite's own "context
// deadline exceeded" or similar.
func TestSQLValidateTimesOut(t *testing.T) {
	_, path := newTestStore(t)
	db, err := readsql.Open(path, 50*time.Millisecond, 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	s := &sqlSource{db: db, sampleRows: true}
	comps := testComponents(t)
	content := `WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r WHERE i < 1000000000)
		SELECT COUNT(*) AS value FROM r`
	err = s.Validate(context.Background(), content, comps["stat"])
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "API_QUERY_TIMEOUT") {
		t.Errorf("err = %q, want it to name API_QUERY_TIMEOUT", err.Error())
	}
}

func TestSampleProject(t *testing.T) {
	st, db := newTestStoreAndReadDB(t)

	// No projects at all: 0, not an error.
	id, err := sampleProject(context.Background(), db)
	if err != nil || id != 0 {
		t.Fatalf("sampleProject (empty) = (%d, %v), want (0, nil)", id, err)
	}

	older := mustCreateProject(t, st, "older")
	mustWriteEvent(t, st, "e-older", older, store.FamilyViews, "2026-08-01")

	newer := mustCreateProject(t, st, "newer")
	mustWriteEvent(t, st, "e-newer", newer, store.FamilyProduct, "2026-08-15")

	archived := mustCreateProject(t, st, "archived")
	mustWriteEvent(t, st, "e-archived", archived, store.FamilyViews, "2026-08-31")
	if err := st.SetProjectArchived(context.Background(), archived, true,
		store.AuditEntry{Actor: "test", Action: "project.archive"}); err != nil {
		t.Fatal(err)
	}

	id, err = sampleProject(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if id != newer {
		t.Errorf("sampleProject = %d, want %d (most recently active, archived skipped)", id, newer)
	}
}

func TestSQLValidateChecksColumnsViaLimitZero(t *testing.T) {
	db := newTestReadDB(t)
	s := &sqlSource{db: db}
	comps := testComponents(t)
	line := comps["line"]

	if err := s.Validate(context.Background(), "select 1 as x, 2 as y", line); err != nil {
		t.Errorf("Validate(x,y) = %v, want nil", err)
	}

	err := s.Validate(context.Background(), "select 1 as x, 2 as visitor", line)
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("Validate(x,visitor) = %v, want ErrInvalid", err)
	}
}

func TestSQLValidateSamplesRowsAndChecksTypes(t *testing.T) {
	db := newTestReadDB(t)
	s := &sqlSource{db: db, sampleRows: true}
	comps := testComponents(t)
	stat := comps["stat"]

	if err := s.Validate(context.Background(), "select 3.5 as value", stat); err != nil {
		t.Errorf("Validate(value=3.5) = %v, want nil", err)
	}

	err := s.Validate(context.Background(), "select 'oops' as value", stat)
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("Validate(value='oops') = %v, want ErrInvalid", err)
	}
}

func TestMDValidate(t *testing.T) {
	m := mdSource{}
	comps := testComponents(t)
	markdown := comps["markdown"]

	for _, content := range []string{"", "   \n\t"} {
		err := m.Validate(context.Background(), content, markdown)
		if !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("Validate(%q) = %v, want ErrInvalid", content, err)
		}
		if !strings.Contains(err.Error(), "markdown text is empty") {
			t.Errorf("err = %q, want it to say markdown text is empty", err.Error())
		}
	}
	if err := m.Validate(context.Background(), "# hi", markdown); err != nil {
		t.Errorf("Validate(non-empty) = %v, want nil", err)
	}
}

func TestMDLoadAndCacheable(t *testing.T) {
	m := mdSource{}
	v, err := m.Load(context.Background(), "# hi", Params{})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := v.(Markdown)
	if !ok || got.Markdown != "# hi" {
		t.Errorf("Load = %#v, want Markdown{Markdown: \"# hi\"}", v)
	}
	if m.Cacheable() {
		t.Error("md Cacheable() = true, want false")
	}
	s := &sqlSource{}
	if !s.Cacheable() {
		t.Error("sql Cacheable() = false, want true")
	}
}

func TestSQLLoadBindsOnlyUsedParams(t *testing.T) {
	db := newTestReadDB(t)
	s := &sqlSource{db: db}

	v, err := s.Load(context.Background(), "select :project as p", Params{ProjectID: 7})
	if err != nil {
		t.Fatal(err)
	}
	res, ok := v.(readsql.Result)
	if !ok {
		t.Fatalf("Load returned %T, want readsql.Result", v)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "7" {
		t.Errorf("rows = %v, want [[7]]", res.Rows)
	}

	// content that never mentions :from or :to must still load: bindArgs
	// binds only the params content actually uses, so an unused :from/:to
	// in Params is never an error.
	v, err = s.Load(context.Background(), "select :project as p", Params{ProjectID: 3, From: "2026-01-01", To: "2026-01-02"})
	if err != nil {
		t.Fatal(err)
	}
	res, ok = v.(readsql.Result)
	if !ok || len(res.Rows) != 1 || res.Rows[0][0] != "3" {
		t.Errorf("rows = %v, want [[3]]", res.Rows)
	}
}
