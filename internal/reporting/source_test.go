package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/reporting/sqlbind"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// fakeCall is one query a fakeFiles saw.
type fakeCall struct {
	mode    sqlengine.Mode
	sql     string
	args    map[string]string
	maxRows int
	scope   bool
	project int64
}

// fakeFiles is an engine that runs nothing: it records each query and
// answers with answer's result (one row of value "1" by default).
type fakeFiles struct {
	calls   []fakeCall
	sample  int64 // the one project with files, 0 for none
	answer  func(sql string, maxRows int) (sqlengine.Result, error)
	maxRows int
}

func (f *fakeFiles) Query(_ context.Context, req sqlengine.Request) (sqlengine.Result, error) {
	c := fakeCall{mode: req.Mode, sql: req.SQL, args: req.Args, maxRows: req.MaxRows}
	if req.Scope != nil {
		c.scope, c.project = true, *req.Scope
	}
	f.calls = append(f.calls, c)
	if f.answer != nil {
		return f.answer(req.SQL, req.MaxRows)
	}
	return sqlengine.Result{Columns: []string{"value"}, Rows: [][]any{{"1"}}}, nil
}

// eng is the engine sqlSource runs on over f: its own events directory,
// where only f.sample has a file, a 5s timeout and f's row cap.
func (f *fakeFiles) eng(t *testing.T) *sqlEngine {
	t.Helper()
	dir := t.TempDir()
	if f.sample != 0 {
		pdir := filepath.Join(dir, strconv.FormatInt(f.sample, 10))
		if err := os.MkdirAll(pdir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pdir, "2026-10-15.log"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return newSQLEngine(f, Options{EventsDir: dir, QueryTimeout: 5 * time.Second, QueryMaxRows: f.maxRows})
}

// failing is a fakeFiles whose every query fails with err.
func failing(err error) *fakeFiles {
	return &fakeFiles{maxRows: 10, answer: func(string, int) (sqlengine.Result, error) { return sqlengine.Result{}, err }}
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
	s := &sqlSource{eng: f.eng(t), sampleRows: true, now: func() time.Time { return now }}
	content := "SELECT count(*) AS value FROM views WHERE project_id = :project AND day BETWEEN :from AND :to"
	if err := s.Validate(context.Background(), content, testComponents(t)["stat"]); err != nil {
		t.Fatal(err)
	}
	bound := "SELECT count(*) AS value FROM views WHERE project_id = CAST($tw_project AS BIGINT) AND day BETWEEN CAST($tw_from AS DATE) AND CAST($tw_to AS DATE)"
	args := map[string]string{"tw_project": "7", "tw_from": "2026-10-09", "tw_to": "2026-10-15"}
	want := []fakeCall{
		{sqlengine.ModeShape, bound, args, 0, true, 7},
		{sqlengine.ModeText, bound, args, 5, true, 7},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls = %+v\nwant  %+v", f.calls, want)
	}

	// Without :project, nothing is scoped and no project is bound.
	f.calls = nil
	if err := s.Validate(context.Background(), "SELECT 1 AS value", testComponents(t)["stat"]); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || f.calls[0].scope || len(f.calls[0].args) != 0 {
		t.Errorf("calls = %+v, want two unscoped, without arguments", f.calls)
	}
}

// TestSQLValidateShapeOnly: a migration checks the shape and samples no
// rows, so the release's own SQL passes on an install with no events.
func TestSQLValidateShapeOnly(t *testing.T) {
	f := &fakeFiles{maxRows: 10}
	s := &sqlSource{eng: f.eng(t)}
	if err := s.Validate(context.Background(), "SELECT 'x' AS value", testComponents(t)["stat"]); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0].mode != sqlengine.ModeShape || f.calls[0].maxRows != 0 {
		t.Errorf("calls = %+v, want the shape query alone", f.calls)
	}
}

func TestSQLLoadBindsOnlyUsedParams(t *testing.T) {
	f := &fakeFiles{maxRows: 10}
	s := &sqlSource{eng: f.eng(t)}
	if _, err := s.Load(context.Background(), "select :project as p", Params{ProjectID: 3, From: "2026-01-01", To: "2026-01-02"}); err != nil {
		t.Fatal(err)
	}
	want := []fakeCall{{sqlengine.ModeText, "select CAST($tw_project AS BIGINT) as p", map[string]string{"tw_project": "3"}, 11, true, 3}}
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
	s := &sqlSource{eng: f.eng(t)}
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
	rows := func(n int) func(string, int) (sqlengine.Result, error) {
		return func(_ string, maxRows int) (sqlengine.Result, error) {
			res := sqlengine.Result{Columns: []string{"n", "x"}}
			for i := range min(n, maxRows) {
				res.Rows = append(res.Rows, []any{fmt.Sprint(i), nil})
			}
			return res, nil
		}
	}
	for n, wantTrunc := range map[int]bool{2: false, 3: false, 4: true, 10: true} {
		s := &sqlSource{eng: (&fakeFiles{maxRows: 3, answer: rows(n)}).eng(t)}
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
	s := &sqlSource{eng: (&fakeFiles{maxRows: 3, answer: rows(0)}).eng(t)}
	v, _ := s.Load(context.Background(), "SELECT 1", Params{})
	if b, _ := json.Marshal(v); !strings.Contains(string(b), `"rows":[]`) {
		t.Errorf("no rows = %s, want rows [] rather than null", b)
	}
}

// TestFilesErrors: what reaches a person when a widget's query fails, by
// the engine's kind.
func TestFilesErrors(t *testing.T) {
	notes := "if this started after an update"
	kind := func(k sqlengine.Kind, msg string) error {
		return &sqlengine.Error{Status: k.Status(), Kind: k, Message: msg}
	}
	for name, c := range map[string]struct {
		err      error
		want     string // the whole message, or "" for a pass-through
		resource bool   // no verdict on the SQL
	}{
		"timeout":        {context.DeadlineExceeded, sqlbind.Timeout(5 * time.Second), true},
		"engine timeout": {kind(sqlengine.KindTimeout, "the query exceeded X-Timeout-Ms"), sqlbind.Timeout(5 * time.Second), true},
		"out of memory":  {kind(sqlengine.KindOutOfMemory, "Out of Memory Error: failed to allocate"), sqlbind.OutOfMemory, true},
		"logs out of memory": {kind(sqlengine.KindOutOfMemoryLoadingLog, "Out of Memory Error: x"),
			sqlbind.LogsOutOfMemory, true},
		"child exited": {kind(sqlengine.KindExited, "engine: child exited"), sqlbind.EngineExited, true},
		"busy":         {kind(sqlengine.KindBusy, "no free slot; retry later"), sqlbind.Busy, true},
		"internal":     {kind(sqlengine.KindInternal, "engine: spawn: no such file"), sqlbind.EngineFailed, true},
		"unavailable":  {fmt.Errorf("%w: dial unix /x.sock: connect: connection refused", sqlengine.ErrUnavailable), sqlbind.EngineFailed, true},
		"duckdb": {kind(sqlengine.KindSQL, "Binder Error: Referenced column \"nope\" not found in FROM clause!\nCandidate bindings: \"day\"\n\nLINE 8: SELECT * FROM (select nope\n                     ^"),
			"Binder Error: Referenced column \"nope\" not found in FROM clause! Candidate bindings: \"day\"", false},
		"sqlite view": {kind(sqlengine.KindSQL, "Catalog Error: Table with name v_views_daily does not exist!"),
			"Catalog Error: Table with name v_views_daily does not exist!; " + sqliteHint, false},
		"sqlite function": {kind(sqlengine.KindSQL, "Catalog Error: Scalar Function with name julianday does not exist!"),
			"Catalog Error: Scalar Function with name julianday does not exist!; " + sqliteHint, false},
		"refused": {kind(sqlengine.KindRefused, "main.events names a schema"), "main.events names a schema", false},
		"gone":    {fmt.Errorf("sqlengine: %w", context.Canceled), "", false},
		"other":   {errors.New("reporting: something else"), "", false},
	} {
		s := &sqlSource{eng: failing(c.err).eng(t)}
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
		// Time, memory, the worker and the engine itself are no verdict on
		// the SQL: a check of it tells them from the engine's refusals by
		// their cause.
		if cause := resourceCause(err); (cause != nil) != c.resource || c.resource && !errors.Is(cause, c.err) {
			t.Errorf("%s: resourceCause = %v, want the cause only for time, memory, the worker and the engine", name, cause)
		}
	}
}

// TestSQLiteHintOnTheEngine: the names a SQLite widget fails on in
// DuckDB, as DuckDB words them, get the hint; other errors do not.
func TestSQLiteHintOnTheEngine(t *testing.T) {
	s := &sqlSource{eng: newTestFiles(t).eng(), sampleRows: true}
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

// TestWidgetSQLReadsOnlyTheTables: a widget's save (Validate), preview,
// load and page each refuse SQL that reads a file or gets past the
// project's scope, with the same message, as ErrInvalid; SQL over the
// tables, a path compared as text included, loads.
func TestWidgetSQLReadsOnlyTheTables(t *testing.T) {
	svc, _ := previewService(t, 1000)
	seedViews(t, svc, 1, "2026-09-10")
	seedViews(t, svc, 2, "2026-09-10")
	s := svc.sources["sql"].(*sqlSource)
	table := testComponents(t)["table"]
	ctx := context.Background()
	p := Params{ProjectID: 1, From: "2026-09-01", To: "2026-09-14"}
	for content, want := range map[string]string{
		"SELECT * FROM read_parquet('/etc/passwd')":                     "read_parquet() is not allowed",
		"SELECT * FROM read_csv_auto('/etc/passwd')":                    "read_csv_auto() is not allowed",
		"SELECT * FROM glob('/*')":                                      "glob() is not allowed",
		"SELECT count(*) AS n FROM main.events WHERE :project > 0":      "main.events names a schema",
		`SELECT count(*) AS n FROM "main"."views" WHERE :project > 0`:   "main.views names a schema",
		"SELECT * FROM '/etc/passwd'":                                   `"/etc/passwd" is not a table here`,
		"WITH _tw_all AS (SELECT * FROM _tw_all) SELECT * FROM _tw_all": `"_tw_all" is not a table here`,
	} {
		check := func(how string, err error) {
			t.Helper()
			if !errors.Is(err, store.ErrInvalid) || !strings.HasPrefix(err.Error(), want) ||
				!strings.Contains(err.Error(), "; SQL here reads only the tables events, views, product, measures, sessions, identities and aliases") {
				t.Errorf("%s %s: err = %v; want ErrInvalid %q", how, content, err, want)
			}
		}
		check("save", s.Validate(ctx, content, table))
		_, err := svc.Preview(ctx, PreviewIn{Component: "table", Source: Source{Type: "sql", Content: content}, ProjectID: 1, From: p.From, To: p.To})
		check("preview", err)
		_, err = s.Load(ctx, content, p)
		check("load", err)
		_, err = s.LoadPage(ctx, content, p, readsql.Page{})
		check("page", err)
	}
	got, err := s.Load(ctx, "SELECT count(*) AS n FROM views WHERE project_id = :project AND path IS DISTINCT FROM '/etc/passwd'", p)
	if err != nil || fmt.Sprint(got.(readsql.Result).Rows) != "[[1]]" {
		t.Errorf("load over views = %v, %v; want project 1's one view", got, err)
	}
}

func TestSQLValidateRefusesSyntaxError(t *testing.T) {
	s := &sqlSource{eng: newTestFiles(t).eng()}
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
		s := &sqlSource{eng: f.eng(), sampleRows: sampleRows}
		if err := s.Validate(context.Background(), content, testComponents(t)["line"]); err != nil {
			t.Errorf("sampleRows=%v: Validate = %v, want nil", sampleRows, err)
		}
	}
	s := &sqlSource{eng: f.eng(), sampleRows: true}
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
	if err := (&sqlSource{eng: f.eng()}).Validate(context.Background(), "SELECT 1 AS value", stat); err != nil {
		t.Fatal(err)
	}
	f.timeout = 50 * time.Millisecond
	s := &sqlSource{eng: f.eng(), sampleRows: true}
	err := s.Validate(context.Background(), "SELECT sum(hash(i)) AS value FROM range(100000000000) t(i)", stat)
	wantRefusal(t, err, store.ErrInvalid, sqlbind.Timeout(50*time.Millisecond))
}

func TestSQLValidateChecksColumnsViaLimitZero(t *testing.T) {
	s := &sqlSource{eng: newTestFiles(t).eng()}
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
	s := &sqlSource{eng: newTestFiles(t).eng(), sampleRows: true}
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
	s := &sqlSource{eng: f.eng()}

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

// TestSQLWithoutAnEngine: a sql source with no engine fails with
// ErrSQLEngine rather than a nil dereference.
func TestSQLWithoutAnEngine(t *testing.T) {
	s := &sqlSource{}
	if _, err := s.Load(context.Background(), "SELECT 1", Params{}); !errors.Is(err, ErrSQLEngine) {
		t.Errorf("Load: err = %v, want ErrSQLEngine", err)
	}
	if err := s.Validate(context.Background(), "SELECT 1 AS value", testComponents(t)["stat"]); !errors.Is(err, ErrSQLEngine) {
		t.Errorf("Validate: err = %v, want ErrSQLEngine", err)
	}
}

// projectsEngine is an engine over a FakeSQL and an events directory
// where each project in days has one empty log of its day.
func projectsEngine(t *testing.T, days map[int64]string) (*sqlEngine, *archivetest.FakeSQL) {
	t.Helper()
	dir := t.TempDir()
	for id, day := range days {
		pdir := filepath.Join(dir, strconv.FormatInt(id, 10))
		if err := os.MkdirAll(pdir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pdir, day+".log"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fake := archivetest.NewFakeSQL(t)
	return newSQLEngine(fake.Client(t), Options{EventsDir: dir, QueryTimeout: 5 * time.Second, QueryMaxRows: 10}), fake
}

// scopeIs takes the requests scoped to project.
func scopeIs(project int64) archivetest.Match {
	return func(r sqlengine.Request) bool { return r.Scope != nil && *r.Scope == project }
}

// scopes are the projects fake's requests were scoped to, in order.
func scopes(fake *archivetest.FakeSQL) []int64 {
	var out []int64
	for _, r := range fake.Requests() {
		if r.Scope != nil {
			out = append(out, *r.Scope)
		}
	}
	return out
}

// TestSQLValidateTriesEveryProject: SQL that reads :project, with no
// project given (a save, a copy, the check at start), passes on the
// first live project with files it runs on, the sample project (the
// newest day) first and the others by id; it is refused only when every
// one refuses it, with the sample project's refusal. A refusal no other
// project would change (the engine refusing the request, a timeout) ends
// the check on the first.
func TestSQLValidateTriesEveryProject(t *testing.T) {
	const content = "SELECT count(*) AS value FROM product WHERE project_id = :project AND COALESCE(attr_position, '') <> ''"
	notFound := archivetest.Fail(sqlengine.KindSQL, `Binder Error: Referenced column "attr_position" not found`)
	stat := testComponents(t)["stat"]
	days := map[int64]string{1: "2026-10-15", 2: "2026-10-14", 3: "2026-10-15"} // 1 is the sample: newest, lowest id

	t.Run("passes on the project that has the column", func(t *testing.T) {
		eng, fake := projectsEngine(t, days)
		fake.On(archivetest.Any, notFound)
		fake.On(scopeIs(2), archivetest.Shape("value"))
		s := &sqlSource{eng: eng}
		if err := s.Validate(context.Background(), content, stat); err != nil {
			t.Fatalf("err = %v, want it to pass on project 2", err)
		}
		if got := scopes(fake); !reflect.DeepEqual(got, []int64{1, 2}) {
			t.Errorf("scopes = %v, want [1 2]: the sample, then by id up to the first that runs", got)
		}
	})
	t.Run("fails everywhere", func(t *testing.T) {
		eng, fake := projectsEngine(t, days)
		fake.On(archivetest.Any, archivetest.Fail(sqlengine.KindSQL, "Binder Error: elsewhere"))
		fake.On(scopeIs(1), notFound)
		s := &sqlSource{eng: eng}
		err := s.Validate(context.Background(), content, stat)
		wantRefusal(t, err, store.ErrInvalid, `Binder Error: Referenced column "attr_position" not found`)
		if got := scopes(fake); !reflect.DeepEqual(got, []int64{1, 2, 3}) {
			t.Errorf("scopes = %v, want every project, [1 2 3]", got)
		}
	})
	t.Run("the component's columns", func(t *testing.T) {
		eng, fake := projectsEngine(t, days)
		fake.On(archivetest.Any, archivetest.Shape("nope"))
		fake.On(scopeIs(3), archivetest.Shape("value"))
		s := &sqlSource{eng: eng}
		if err := s.Validate(context.Background(), content, stat); err != nil {
			t.Fatalf("err = %v, want it to pass on project 3", err)
		}
	})
	for name, answer := range map[string]archivetest.Answer{
		"refused": archivetest.Fail(sqlengine.KindRefused, `"read_csv" reads files`),
		"timeout": archivetest.Fail(sqlengine.KindTimeout, "the query exceeded X-Timeout-Ms"),
	} {
		t.Run(name+" stops at once", func(t *testing.T) {
			eng, fake := projectsEngine(t, days)
			fake.On(archivetest.Any, answer)
			s := &sqlSource{eng: eng}
			if err := s.Validate(context.Background(), content, stat); err == nil {
				t.Fatal("err = nil, want the first project's")
			}
			if got := scopes(fake); !reflect.DeepEqual(got, []int64{1}) {
				t.Errorf("scopes = %v, want the sample project alone", got)
			}
		})
	}
	t.Run("without :project, one unscoped check", func(t *testing.T) {
		eng, fake := projectsEngine(t, days)
		fake.On(archivetest.Any, archivetest.Fail(sqlengine.KindSQL, "Binder Error: x"))
		s := &sqlSource{eng: eng}
		if err := s.Validate(context.Background(), "SELECT attr_position AS value FROM product", stat); err == nil {
			t.Fatal("err = nil, want the refusal")
		}
		if n, got := len(fake.Requests()), scopes(fake); n != 1 || got != nil {
			t.Errorf("%d requests scoped to %v, want one unscoped", n, got)
		}
	})
}

// TestSQLExplicitProjectIsTheOnlyOne: a load with its project (a
// dashboard's selection, widget_data's project_id) runs on that project
// alone, and fails when it fails there, whatever another project has.
func TestSQLExplicitProjectIsTheOnlyOne(t *testing.T) {
	eng, fake := projectsEngine(t, map[int64]string{1: "2026-10-15", 2: "2026-10-14"})
	fake.On(archivetest.Any, archivetest.Text([]string{"value"}, []string{"1"}))
	fake.On(scopeIs(1), archivetest.Fail(sqlengine.KindSQL, `Binder Error: Referenced column "attr_position" not found`))
	s := &sqlSource{eng: eng}
	_, err := s.Load(context.Background(), "SELECT count(attr_position) AS value FROM product WHERE project_id = :project", Params{ProjectID: 1})
	wantRefusal(t, err, store.ErrInvalid, `Binder Error: Referenced column "attr_position" not found`)
	if got := scopes(fake); !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("scopes = %v, want project 1 alone", got)
	}
}
