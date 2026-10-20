package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// fakeCall is one Files.Query a fakeFiles saw.
type fakeCall struct {
	sql     string
	args    []engine.Arg
	maxRows int
	scope   bool
	project int64
}

// fakeFiles is Files that runs nothing: it records each query and
// answers with answer's result (one row of value "1" by default).
type fakeFiles struct {
	calls   []fakeCall
	sample  int64
	answer  func(sql string, maxRows int) (engine.Result, error)
	maxRows int
}

func (f *fakeFiles) Query(_ context.Context, q archive.Scoped) (engine.Result, error) {
	f.calls = append(f.calls, fakeCall{q.SQL, q.Args, q.MaxRows, q.Scope, q.Project})
	if f.answer != nil {
		return f.answer(q.SQL, q.MaxRows)
	}
	return engine.Result{Columns: []string{"value"}, Rows: [][]any{{"1"}}}, nil
}

func (f *fakeFiles) SampleProject(context.Context) (int64, error) { return f.sample, nil }
func (f *fakeFiles) Timeout() time.Duration                       { return 5 * time.Second }
func (f *fakeFiles) MaxRows() int                                 { return f.maxRows }

// failing is a fakeFiles whose every query fails with err.
func failing(err error) *fakeFiles {
	return &fakeFiles{maxRows: 10, answer: func(string, int) (engine.Result, error) { return engine.Result{}, err }}
}

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
		{"select day::DATE, ':project' as p -- :to", false, false}, // a cast, a string and a comment
	} {
		project, rng := s.Follows(tt.content)
		if project != tt.wantProject || rng != tt.wantRng {
			t.Errorf("Follows(%q) = (%v,%v), want (%v,%v)", tt.content, project, rng, tt.wantProject, tt.wantRng)
		}
	}
}

func TestSQLValidateRefusesUnsupportedParams(t *testing.T) {
	s := &sqlSource{} // refused by the scanner before any query would run
	line := testComponents(t)["line"]

	for content, msg := range map[string]string{
		"select :path as x, 1 as y":     "sql uses :path; widgets get only :project, :from and :to",
		"select ? as x, 1 as y":         "? is not a parameter here; use :name",
		"select $1 as x, 1 as y":        "$1 is not a parameter here; use :name",
		"select $tw_from as x, 1 as y":  "$tw_from is not a parameter here; use :name",
		"select 1 as x, 2 as y; drop x": "sql must be a single statement; remove what follows the ';'",
		" ;\n":                          "sql must not be empty",
	} {
		err := s.Validate(context.Background(), content, line)
		wantRefusal(t, err, store.ErrInvalid, msg)
	}
}

// TestSQLValidateBindsTheSampleProjectAndWeek: the shape check and the
// sample both bind Files' sample project and the last seven days, scope
// the query to that project, and pass only the arguments the SQL uses.
func TestSQLValidateBindsTheSampleProjectAndWeek(t *testing.T) {
	f := &fakeFiles{sample: 7, maxRows: 10}
	now := time.Date(2026, 10, 14, 23, 0, 0, 0, time.FixedZone("UTC-3", -3*3600)) // 2026-10-15 in UTC
	s := &sqlSource{files: f, sampleRows: true, now: func() time.Time { return now }}
	content := "SELECT count(*) AS value FROM views WHERE project_id = :project AND day BETWEEN :from AND :to"
	if err := s.Validate(context.Background(), content, testComponents(t)["stat"]); err != nil {
		t.Fatal(err)
	}
	bound := "SELECT count(*) AS value FROM views WHERE project_id = CAST($tw_project AS BIGINT) AND day BETWEEN CAST($tw_from AS DATE) AND CAST($tw_to AS DATE)"
	args := []engine.Arg{{Name: "tw_project", Value: "7"}, {Name: "tw_from", Value: "2026-10-09"}, {Name: "tw_to", Value: "2026-10-15"}}
	want := []fakeCall{
		{enginesql.Shape(bound), args, 0, true, 7},
		{enginesql.Text(bound), args, 5, true, 7},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls = %+v\nwant  %+v", f.calls, want)
	}

	// Without :project, nothing is scoped and no project is bound.
	f.calls = nil
	if err := s.Validate(context.Background(), "SELECT 1 AS value", testComponents(t)["stat"]); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || f.calls[0].scope || f.calls[0].args != nil {
		t.Errorf("calls = %+v, want two unscoped, without arguments", f.calls)
	}
}

// TestSQLValidateShapeOnly: a migration checks the shape and samples no
// rows, so the release's own SQL passes on an install with no events.
func TestSQLValidateShapeOnly(t *testing.T) {
	f := &fakeFiles{maxRows: 10}
	s := &sqlSource{files: f}
	if err := s.Validate(context.Background(), "SELECT 'x' AS value", testComponents(t)["stat"]); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0].maxRows != 0 {
		t.Errorf("calls = %+v, want the shape query alone", f.calls)
	}
}

func TestSQLLoadBindsOnlyUsedParams(t *testing.T) {
	f := &fakeFiles{maxRows: 10}
	s := &sqlSource{files: f}
	if _, err := s.Load(context.Background(), "select :project as p", Params{ProjectID: 3, From: "2026-01-01", To: "2026-01-02"}); err != nil {
		t.Fatal(err)
	}
	want := []fakeCall{{enginesql.Text("select CAST($tw_project AS BIGINT) as p"), []engine.Arg{{Name: "tw_project", Value: "3"}}, 11, true, 3}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls = %+v\nwant  %+v", f.calls, want)
	}
}

// TestSQLProjectZeroScopesToNoRows: a widget that follows :project is
// scoped even when the project is 0 (a request without one, or an install
// whose files hold no live project's events), which reads no rows rather
// than every project's.
func TestSQLProjectZeroScopesToNoRows(t *testing.T) {
	f := &fakeFiles{maxRows: 10} // SampleProject: 0
	s := &sqlSource{files: f}
	content := "SELECT count(*) AS value FROM views WHERE project_id = :project"
	if _, err := s.Load(context.Background(), content, Params{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(context.Background(), content, testComponents(t)["stat"]); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if !c.scope || c.project != 0 {
			t.Errorf("call %+v: want scoped to project 0", c)
		}
	}
}

// TestSQLLoadTruncates: Load asks for one row past MaxRows, keeps
// MaxRows of them and says the rest were cut; cells are text, NULL "".
func TestSQLLoadTruncates(t *testing.T) {
	rows := func(n int) func(string, int) (engine.Result, error) {
		return func(_ string, maxRows int) (engine.Result, error) {
			res := engine.Result{Columns: []string{"n", "x"}}
			for i := range min(n, maxRows) {
				res.Rows = append(res.Rows, []any{fmt.Sprint(i), nil})
			}
			return res, nil
		}
	}
	for n, wantTrunc := range map[int]bool{2: false, 3: false, 4: true, 10: true} {
		s := &sqlSource{files: &fakeFiles{maxRows: 3, answer: rows(n)}}
		v, err := s.Load(context.Background(), "SELECT 1", Params{})
		if err != nil {
			t.Fatal(err)
		}
		res := v.(readsql.Result)
		if len(res.Rows) != min(n, 3) || res.Truncated != wantTrunc {
			t.Errorf("%d rows: got %d rows, truncated %v; want %d, %v", n, len(res.Rows), res.Truncated, min(n, 3), wantTrunc)
		}
		if res.Rows[0][1] != "" {
			t.Errorf("NULL cell = %q, want \"\"", res.Rows[0][1])
		}
	}
	s := &sqlSource{files: &fakeFiles{maxRows: 3, answer: rows(0)}}
	v, _ := s.Load(context.Background(), "SELECT 1", Params{})
	if b, _ := json.Marshal(v); !strings.Contains(string(b), `"rows":[]`) {
		t.Errorf("no rows = %s, want rows [] rather than null", b)
	}
}

// TestFilesErrors: what reaches a person when a widget's query fails.
func TestFilesErrors(t *testing.T) {
	notes := "if this started after an update"
	for name, c := range map[string]struct {
		err  error
		want string // the whole message, or "" for a pass-through
	}{
		"timeout": {context.DeadlineExceeded,
			enginesql.Timeout(5 * time.Second)},
		"out of memory": {&engine.QueryError{Msg: "Out of Memory Error: failed to allocate"}, enginesql.OutOfMemory},
		"logs out of memory": {fmt.Errorf("%w: %w", archive.ErrLoadingLogs, &engine.QueryError{Msg: "Out of Memory Error: x"}),
			enginesql.LogsOutOfMemory},
		"child exited": {engine.ErrChildExited, enginesql.ChildExited},
		"duckdb": {&engine.QueryError{Msg: "Binder Error: Referenced column \"nope\" not found in FROM clause!\nCandidate bindings: \"day\"\n\nLINE 8: SELECT * FROM (select nope\n                     ^"},
			"Binder Error: Referenced column \"nope\" not found in FROM clause! Candidate bindings: \"day\""},
		"sqlite view": {&engine.QueryError{Msg: "Catalog Error: Table with name v_views_daily does not exist!"},
			"Catalog Error: Table with name v_views_daily does not exist!; " + sqliteHint},
		"sqlite function": {&engine.QueryError{Msg: "Catalog Error: Scalar Function with name julianday does not exist!"},
			"Catalog Error: Scalar Function with name julianday does not exist!; " + sqliteHint},
		"spawn": {engine.ErrSpawn, ""},
	} {
		s := &sqlSource{files: failing(c.err)}
		_, err := s.Load(context.Background(), "SELECT 1", Params{})
		if c.want == "" {
			if !errors.Is(err, c.err) || errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s: err = %v, want %v passed through, not a refusal", name, err, c.err)
			}
			continue
		}
		wantRefusal(t, err, store.ErrInvalid, c.want)
		if strings.Contains(err.Error(), notes) {
			t.Errorf("%s: the source adds the release notes pointer; WidgetData does", name)
		}
		// Time, memory and the child are no verdict on the SQL: a check of
		// it tells them from DuckDB's refusals by their cause.
		resource := name == "timeout" || name == "out of memory" || name == "logs out of memory" || name == "child exited"
		if cause := resourceCause(err); (cause != nil) != resource || resource && !errors.Is(cause, c.err) {
			t.Errorf("%s: resourceCause = %v, want the cause only for time, memory and the child", name, cause)
		}
	}
}

// TestSQLiteHintOnTheEngine: the names a SQLite widget fails on in
// DuckDB, as DuckDB words them, get the hint; other errors do not.
func TestSQLiteHintOnTheEngine(t *testing.T) {
	s := &sqlSource{files: newTestFiles(t), sampleRows: true}
	table := testComponents(t)["table"]
	for content, hint := range map[string]bool{
		"SELECT * FROM v_views_daily":                                true,
		"SELECT * FROM agg_views_daily":                              true,
		"SELECT * FROM raw_views":                                    true,
		"SELECT julianday(day) AS d FROM events":                     true,
		"SELECT strftime('%s', ts) AS s FROM events":                 true,
		"SELECT date(day, '+1 day') AS d FROM events":                true,
		"SELECT last_seen_day FROM identities":                       true,
		"SELECT updated_at FROM identities":                          true,
		"SELECT json_extract(attributes, '$.plan') AS p FROM events": true,
		"SELECT 1 AS n FROM events e JOIN views v WHERE e.id = v.id": true,
		"SELECT MAX(day, day) AS d FROM events":                      true,
		"SELECT * FROM meta":                                         false, // no such table, SQLite's or not
		"SELECT nope FROM events":                                    false,
		"SELECT day + 'x' AS d FROM events":                          false,
		"SELECT count(*) AS n FROM views WHERE day > :from":          false, // runs
	} {
		err := s.Validate(context.Background(), content, table)
		if strings.HasSuffix(content, ":from") {
			if err != nil {
				t.Errorf("%s: %v, want nil", content, err)
			}
			continue
		}
		if !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", content, err)
			continue
		}
		if got := strings.HasSuffix(err.Error(), "; "+sqliteHint); got != hint {
			t.Errorf("%s: hint %v, want %v: %q", content, got, hint, err)
		}
	}
}

func TestSQLValidateRefusesSyntaxError(t *testing.T) {
	s := &sqlSource{files: newTestFiles(t)}
	err := s.Validate(context.Background(), "select from where", testComponents(t)["table"])
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	// DuckDB's own text must come through, not be swallowed by a generic
	// "invalid SQL" message: it's what tells a person which token it
	// stumbled on.
	if !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("err = %q, want DuckDB's own syntax error text", err.Error())
	}
}

// TestSQLValidateOnTheFiles: a widget over :project, :from and :to
// validates against real files, with and without the sample run, and
// the sample reads the sample project's rows: stat's value is a number.
func TestSQLValidateOnTheFiles(t *testing.T) {
	st, f := newTestStoreAndFiles(t, 1000)
	pid := mustCreateProject(t, st, "blog")
	today := time.Now().UTC().Format("2006-01-02")
	mustWriteEvent(t, f, "e1", pid, store.FamilyViews, today)
	mustWriteEvent(t, f, "e2", pid, store.FamilyViews, today)

	content := `select day as x, count(*) as y from views
		where project_id = :project and day >= :from and day <= :to group by day`
	for _, sampleRows := range []bool{false, true} {
		s := &sqlSource{files: f, sampleRows: sampleRows}
		if err := s.Validate(context.Background(), content, testComponents(t)["line"]); err != nil {
			t.Errorf("sampleRows=%v: Validate = %v, want nil", sampleRows, err)
		}
	}
	s := &sqlSource{files: f, sampleRows: true}
	err := s.Validate(context.Background(), "select 'n' || count(*) as value from views where project_id = :project", testComponents(t)["stat"])
	wantRefusal(t, err, store.ErrInvalid, `stat.value: "n2" is not number`)
}

// TestSQLValidateTimesOut pins the CONSOLE_QUERY_TIMEOUT refusal. The
// shape check (LIMIT 0) does no work, so it is the sample run that has
// to compute the one row and runs out of time.
func TestSQLValidateTimesOut(t *testing.T) {
	f := newTestFiles(t)
	stat := testComponents(t)["stat"]
	// The first query starts the child and builds the tables, which the
	// short timeout below is not for.
	if err := (&sqlSource{files: f}).Validate(context.Background(), "SELECT 1 AS value", stat); err != nil {
		t.Fatal(err)
	}
	f.timeout = 50 * time.Millisecond
	s := &sqlSource{files: f, sampleRows: true}
	err := s.Validate(context.Background(), "SELECT sum(hash(i)) AS value FROM range(100000000000) t(i)", stat)
	wantRefusal(t, err, store.ErrInvalid, enginesql.Timeout(50*time.Millisecond))
}

func TestSQLValidateChecksColumnsViaLimitZero(t *testing.T) {
	s := &sqlSource{files: newTestFiles(t)}
	line := testComponents(t)["line"]

	if err := s.Validate(context.Background(), "select 1 as x, 2 as y", line); err != nil {
		t.Errorf("Validate(x,y) = %v, want nil", err)
	}

	err := s.Validate(context.Background(), "select 1 as x, 2 as visitor", line)
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("Validate(x,visitor) = %v, want ErrInvalid", err)
	}
}

func TestSQLValidateSamplesRowsAndChecksTypes(t *testing.T) {
	s := &sqlSource{files: newTestFiles(t), sampleRows: true}
	stat := testComponents(t)["stat"]

	if err := s.Validate(context.Background(), "select 3.5 as value", stat); err != nil {
		t.Errorf("Validate(value=3.5) = %v, want nil", err)
	}

	err := s.Validate(context.Background(), "select 'oops' as value", stat)
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("Validate(value='oops') = %v, want ErrInvalid", err)
	}
}

// TestSQLLoadOnTheFiles: cells read as SQLite's did, a number without a
// trailing .0 and NULL as "", and :project scopes the tables to the
// project's rows even without a filter of the widget's own.
func TestSQLLoadOnTheFiles(t *testing.T) {
	st, f := newTestStoreAndFiles(t, 1000)
	blog, docs := mustCreateProject(t, st, "blog"), mustCreateProject(t, st, "docs")
	mustWriteEvent(t, f, "b1", blog, store.FamilyViews, "2026-08-20")
	mustWriteEvent(t, f, "b2", blog, store.FamilyViews, "2026-08-21")
	mustWriteEvent(t, f, "d1", docs, store.FamilyViews, "2026-08-20")
	s := &sqlSource{files: f}

	v, err := s.Load(context.Background(),
		"SELECT :project AS p, count(*) / 1 AS n, NULL AS z, min(day) AS d FROM views", Params{ProjectID: blog})
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{fmt.Sprint(blog), "2", "", "2026-08-20"}}; !reflect.DeepEqual(v.(readsql.Result).Rows, want) {
		t.Errorf("rows = %v, want %v", v.(readsql.Result).Rows, want)
	}
	v, err = s.Load(context.Background(), "SELECT count(*) AS n FROM views", Params{ProjectID: blog})
	if err != nil {
		t.Fatal(err)
	}
	if got := v.(readsql.Result).Rows[0][0]; got != "3" {
		t.Errorf("without :project: n = %s, want 3, every project's", got)
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

// TestSQLWithoutFiles: a Service given no Files fails a sql widget with
// an error of its own rather than a nil dereference.
func TestSQLWithoutFiles(t *testing.T) {
	s := &sqlSource{}
	if _, err := s.Load(context.Background(), "SELECT 1", Params{}); !errors.Is(err, errNoFiles) {
		t.Errorf("Load: err = %v, want errNoFiles", err)
	}
	if err := s.Validate(context.Background(), "SELECT :project AS value", testComponents(t)["stat"]); !errors.Is(err, errNoFiles) {
		t.Errorf("Validate: err = %v, want errNoFiles", err)
	}
}
