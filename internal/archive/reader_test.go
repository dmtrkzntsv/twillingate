package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func (f fixture) reader(t *testing.T) *Reader {
	t.Helper()
	r, err := NewReader(f.log.Dir(), f.eng)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// ask runs sql through r and returns its rows as fmt prints them.
func ask(t *testing.T, r *Reader, rules Rules, sql string) string {
	t.Helper()
	res, err := r.Query(context.Background(), sql, 0, rules)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return fmt.Sprint(res.Rows)
}

func TestReaderReadsFilesAndLogsAsOneTable(t *testing.T) {
	f := newFixture(t)
	at := "2026-10-11T10:00:00Z"
	view := store.Event{ID: "v1", ProjectID: 1, Family: store.FamilyViews, EventName: "$page_view", TS: ts(at), ReceivedAt: ts(at), Path: "/"}
	p1 := evOf(1, "p1", at)
	p1.Attributes = map[string]string{"plan": "pro"}
	v := 2.5
	m := store.Event{ID: "m1", ProjectID: 1, Family: store.FamilyMeasures, EventName: "load", Measure: store.MeasureTime, Value: &v,
		TS: ts("2026-10-12T09:00:00Z"), ReceivedAt: ts("2026-10-12T09:00:00Z")}
	f.write(t, view, p1, evOf(1, "p2", "2026-10-11T11:00:00Z"), m)
	f.maintain(t, "2026-10-12")
	if got := f.files(t); !eq(got, []string{"2026-10-11.parquet", "2026-10-12.log"}) {
		t.Fatalf("files = %v, want a day file and an open log", got)
	}
	r := f.reader(t)
	if got := ask(t, r, Rules{}, "SELECT family, count(*) FROM events GROUP BY family ORDER BY family"); got != "[[measures 1] [product 2] [views 1]]" {
		t.Fatalf("events by family = %s", got)
	}
	for table, want := range map[string]string{"views": "[[1]]", "product": "[[2]]", "measures": "[[1]]"} {
		if got := ask(t, r, Rules{}, "SELECT count(*) FROM "+table); got != want {
			t.Errorf("%s: %s, want %s", table, got, want)
		}
	}
	if got := ask(t, r, Rules{}, "SELECT id, attr_plan, value FROM events WHERE id IN ('p1', 'm1') ORDER BY id"); got != "[[m1 <nil> 2.5] [p1 pro <nil>]]" {
		t.Fatalf("columns = %s", got)
	}
}

func TestReaderFollowsNewLinesAndThePass(t *testing.T) {
	f := newFixture(t)
	r := f.reader(t)
	f.write(t, evOf(1, "a", "2026-10-11T10:00:00Z"))
	if got := ask(t, r, Rules{}, "SELECT count(*) FROM events"); got != "[[1]]" {
		t.Fatalf("count = %s, want 1", got)
	}
	f.write(t, evOf(1, "b", "2026-10-11T11:00:00Z"), evOf(2, "c", "2026-10-11T12:00:00Z"))
	if got := ask(t, r, Rules{}, "SELECT count(*) FROM events"); got != "[[3]]" {
		t.Fatalf("count after more lines and a new project = %s, want 3", got)
	}
	f.maintain(t, "2026-10-12")
	if got := ask(t, r, Rules{}, "SELECT count(*) FROM events"); got != "[[3]]" {
		t.Fatalf("count after the dump = %s, want 3, nothing twice", got)
	}
}

// vanishing fails the first user query the way a file the daily pass
// removed after the listing fails it.
type vanishing struct {
	Engine
	fails int
}

func (e *vanishing) Query(ctx context.Context, sql string, maxRows int) (engine.Result, error) {
	if strings.Contains(sql, "FROM events") && e.fails > 0 {
		e.fails--
		return engine.Result{}, &engine.QueryError{Msg: `IO Error: No files found that match the pattern "/raw/1/2026-10-11.parquet"`}
	}
	return e.Engine.Query(ctx, sql, maxRows)
}

// TestReaderRetriesOnceWhenAFileVanished is Review Focus 1.
func TestReaderRetriesOnceWhenAFileVanished(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-10-11T10:00:00Z"))
	eng := &vanishing{Engine: f.eng, fails: 1}
	r, err := NewReader(f.log.Dir(), eng)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Query(context.Background(), "SELECT count(*) FROM events", 0, Rules{})
	if err != nil || fmt.Sprint(res.Rows) != "[[1]]" {
		t.Fatalf("after one vanished file: %v, %v; want the retry to answer 1", res.Rows, err)
	}
	eng.fails = 2
	var qe *engine.QueryError
	if _, err := r.Query(context.Background(), "SELECT count(*) FROM events", 0, Rules{}); !errors.As(err, &qe) {
		t.Fatalf("after two: err = %v; want the second failure returned, not a third try", err)
	}
}

// TestReaderCountsWhatACrashedMergeLeftOnce is Review Focus 2 through the
// tables: the month file and a day it merged both on disk.
func TestReaderCountsWhatACrashedMergeLeftOnce(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-09-29T10:00:00Z"), evOf(1, "b", "2026-09-30T10:00:00Z"))
	f.maintain(t, "2026-10-01") // dumps both days; September is not final yet
	day := filepath.Join(f.log.Dir(), "1", "2026-09-30.parquet")
	saved, err := os.ReadFile(day)
	if err != nil {
		t.Fatal(err)
	}
	f.maintain(t, "2026-10-02")                             // merges September
	if err := os.WriteFile(day, saved, 0o640); err != nil { // as if the merge stopped before deleting it
		t.Fatal(err)
	}
	if got := ask(t, f.reader(t), Rules{}, "SELECT count(*), count(DISTINCT id) FROM events"); got != "[[2 2]]" {
		t.Fatalf("count = %s, want 2 rows, each once", got)
	}
}

// TestReaderSkipsATornLine is Review Focus 3.
func TestReaderSkipsATornLine(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-10-12T10:00:00Z"), evOf(1, "b", "2026-10-12T11:00:00Z"))
	appendTo(t, filepath.Join(f.log.Dir(), "1", "2026-10-12.log"), `{"v":1,"id":"torn","fam`)
	if got := ask(t, f.reader(t), Rules{}, "SELECT id FROM events ORDER BY id"); got != "[[a] [b]]" {
		t.Fatalf("ids = %s, want a and b", got)
	}
}

// TestReaderOnAnEmptyDirectory is Review Focus 4: a fresh install.
func TestReaderOnAnEmptyDirectory(t *testing.T) {
	f := newFixture(t)
	r, err := NewReader(filepath.Join(t.TempDir(), "raw"), f.eng)
	if err != nil {
		t.Fatal(err)
	}
	if got := ask(t, r, Rules{}, "SELECT count(*) FROM events"); got != "[[0]]" {
		t.Fatalf("count = %s, want 0", got)
	}
	got := ask(t, r, Rules{}, "SELECT column_name || ' ' || column_type FROM (DESCRIBE events)")
	for _, want := range []string{"[id VARCHAR]", "[project_id BIGINT]", "[ts TIMESTAMP]", "[day DATE]", "[consent BIGINT]", "[value DOUBLE]"} {
		if !strings.Contains(got, want) {
			t.Errorf("DESCRIBE events = %s; missing %s", got, want)
		}
	}
	if strings.Contains(got, "[v BIGINT]") {
		t.Errorf("DESCRIBE events = %s; the line format's v is not a column", got)
	}
}

// TestReaderAppliesRulesOnRead is Review Focus 5.
func TestReaderAppliesRulesOnRead(t *testing.T) {
	f := newFixture(t)
	at := "2026-10-12T10:00:00Z"
	e := func(project int64, id, name string, attrs map[string]string) store.Event {
		ev := evOf(project, id, at)
		ev.EventName, ev.Attributes = name, attrs
		return ev
	}
	f.write(t,
		e(1, "1", "old", map[string]string{"tier": "gold"}),
		e(1, "2", "new", map[string]string{"level": "silver", "tier": "bronze"}),
		e(1, "3", "gone", nil),
		e(1, "4", "old_gone", nil),
		e(1, "5", "x", map[string]string{"secret": "s", "alias": "a"}),
		e(2, "6", "old", map[string]string{"tier": "gold", "secret": "s"}),
		store.Event{ID: "7", ProjectID: 1, Family: store.FamilyViews, EventName: "$page_view", TS: ts(at), ReceivedAt: ts(at),
			Attributes: map[string]string{"tier": "web"}},
	)
	rules := Rules{
		Events: []store.EventRule{
			{ProjectID: 1, Family: store.FamilyProduct, Name: "old", Action: store.EventRuleRename, To: "new"},
			{ProjectID: 1, Family: store.FamilyProduct, Name: "gone", Action: store.EventRuleBlock},
			{ProjectID: 1, Family: store.FamilyProduct, Name: "old_gone", Action: store.EventRuleRename, To: "gone"},
		},
		Attributes: []store.AttributeRule{
			{ProjectID: 1, Key: "tier", Action: store.EventRuleRename, To: "level"},
			{ProjectID: 1, Key: "alias", Action: store.EventRuleRename, To: "nickname"},
			{ProjectID: 1, Key: "secret", Action: store.EventRuleBlock},
		},
	}
	r := f.reader(t)
	got := ask(t, r, rules, "SELECT id, project_id, event_name, attr_level, attr_tier, attr_secret, attr_nickname FROM events ORDER BY id")
	want := "[[1 1 new gold <nil> <nil> <nil>] [2 1 new silver <nil> <nil> <nil>] [5 1 x <nil> <nil> <nil> a] " +
		"[6 2 old <nil> gold s <nil>] [7 1 $page_view <nil> web <nil> <nil>]]"
	if got != want {
		t.Fatalf("with the rules:\n got %s\nwant %s", got, want)
	}
	if got := ask(t, r, Rules{}, "SELECT count(*), count(attr_tier), count(attr_secret) FROM events"); got != "[[7 4 2]]" {
		t.Fatalf("without the rules: %s, want every row back with its own keys", got)
	}
	if got := ask(t, r, Rules{}, "SELECT count(*) FROM (DESCRIBE events) WHERE column_name = 'attr_nickname'"); got != "[[0]]" {
		t.Fatalf("attr_nickname without its rule: %s, want no such column", got)
	}
}

// counting counts the statements a reader runs to build its tables.
type counting struct {
	Engine
	execs int
}

func (c *counting) Exec(ctx context.Context, sql string) error {
	c.execs++
	return c.Engine.Exec(ctx, sql)
}

func TestReaderBuildsOnlyWhenSomethingChanged(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-10-12T10:00:00Z"))
	eng := &counting{Engine: f.eng}
	r, err := NewReader(f.log.Dir(), eng)
	if err != nil {
		t.Fatal(err)
	}
	ask(t, r, Rules{}, "SELECT count(*) FROM events")
	built := eng.execs
	ask(t, r, Rules{}, "SELECT count(*) FROM events")
	if eng.execs != built {
		t.Fatalf("a second query with nothing changed ran %d more statements, want 0", eng.execs-built)
	}
	ask(t, r, Rules{Events: []store.EventRule{{ProjectID: 1, Family: store.FamilyProduct, Name: "x", Action: store.EventRuleBlock}}}, "SELECT 1")
	if eng.execs != 2*built {
		t.Fatalf("a new rule: %d statements, want one more build (%d)", eng.execs-built, built)
	}
	f.write(t, evOf(1, "b", "2026-10-12T11:00:00Z"))
	if got := ask(t, r, Rules{}, "SELECT count(*) FROM events"); got != "[[2]]" || eng.execs != 3*built {
		t.Fatalf("a grown log: count %s after %d statements; want 2 after one more build", got, eng.execs)
	}
}

// retired fails the first user query as a fresh child fails it, after the
// engine retired the one that held the tables: it drops them, as the new
// child never had them, and answers with the catalog's message for name.
type retired struct {
	Engine
	msg   string
	fails int
	calls int
}

func (e *retired) Query(ctx context.Context, sql string, maxRows int) (engine.Result, error) {
	if strings.Contains(sql, "FROM events") {
		e.calls++
		if e.fails > 0 {
			e.fails--
			if err := e.Engine.Exec(ctx, "DROP VIEW events; DROP VIEW views; DROP VIEW product; DROP VIEW measures; DROP VIEW _tw_all; DROP TABLE _tw_hot; DROP TABLE _tw_state"); err != nil {
				return engine.Result{}, err
			}
			return engine.Result{}, &engine.QueryError{Msg: e.msg}
		}
	}
	return e.Engine.Query(ctx, sql, maxRows)
}

func TestReaderRebuildsAndRetriesOnAFreshChild(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-10-11T10:00:00Z"))
	for _, name := range []string{"events", "views", "product", "measures", "_tw_all", "_tw_hot"} {
		eng := &retired{Engine: f.eng, msg: "Catalog Error: Table with name " + name + " does not exist!\nDid you mean \"pg_tables\"?", fails: 1}
		r, err := NewReader(f.log.Dir(), eng)
		if err != nil {
			t.Fatal(err)
		}
		if got := ask(t, r, Rules{}, "SELECT count(*) FROM events"); got != "[[1]]" {
			t.Fatalf("%s: after the child was replaced: %s, want the retry to rebuild and answer 1", name, got)
		}
	}
}

func TestReaderDoesNotRetryAUsersTypo(t *testing.T) {
	f := newFixture(t)
	eng := &retired{Engine: f.eng, msg: "Catalog Error: Table with name eventz does not exist!\nDid you mean \"events\"?", fails: 1}
	r, err := NewReader(f.log.Dir(), eng)
	if err != nil {
		t.Fatal(err)
	}
	var qe *engine.QueryError
	if _, err := r.Query(context.Background(), "SELECT count(*) FROM events", 0, Rules{}); !errors.As(err, &qe) || eng.calls != 1 {
		t.Fatalf("err = %v after %d tries; want the typo's error after one", err, eng.calls)
	}
}

// TestReaderAppliesRulesOnKeysAndNamesWithNUL: SQL text is cut at a NUL, so
// a rule naming one is spelled in the statement without it.
func TestReaderAppliesRulesOnKeysAndNamesWithNUL(t *testing.T) {
	f := newFixture(t)
	ev := evOf(1, "1", "2026-10-12T10:00:00Z")
	ev.EventName, ev.Attributes = "n\x00m", map[string]string{"a\x00b": "v"}
	f.write(t, ev)
	rules := Rules{
		Events:     []store.EventRule{{ProjectID: 1, Family: store.FamilyProduct, Name: "n\x00m", Action: store.EventRuleRename, To: "z\x00"}},
		Attributes: []store.AttributeRule{{ProjectID: 1, Key: "a\x00b", Action: store.EventRuleRename, To: "c\x00d"}},
	}
	r := f.reader(t)
	if got := ask(t, r, rules, `SELECT id, event_name = 'z' || chr(0), "attr_c\u0000d", "attr_a\u0000b" FROM events`); got != "[[1 true v <nil>]]" {
		t.Fatalf("with the rules: %s", got)
	}
	if got := ask(t, r, Rules{}, `SELECT event_name = 'n' || chr(0) || 'm', "attr_a\u0000b" FROM events`); got != "[[true v]]" {
		t.Fatalf("without them: %s", got)
	}
}

func TestReaderAppliesRulesOnKeysThatMeetOrCollide(t *testing.T) {
	f := newFixture(t)
	at := "2026-10-12T10:00:00Z"
	e := func(id string, attrs map[string]string) store.Event {
		ev := evOf(1, id, at)
		ev.Attributes = attrs
		return ev
	}
	f.write(t,
		e("1", map[string]string{"a": "1", "b": "2"}),
		e("2", map[string]string{"a": "x"}),
		e("3", map[string]string{"k1": "first", "k2": "second"}),
		e("4", map[string]string{"k2": "only"}),
	)
	rules := Rules{Attributes: []store.AttributeRule{
		{ProjectID: 1, Key: "a", Action: store.EventRuleRename, To: "b"}, // onto a blocked key
		{ProjectID: 1, Key: "b", Action: store.EventRuleBlock},
		{ProjectID: 1, Key: "k2", Action: store.EventRuleRename, To: "t"}, // two keys into a target no file has
		{ProjectID: 1, Key: "k1", Action: store.EventRuleRename, To: "t"},
	}}
	r := f.reader(t)
	if got := ask(t, r, rules, "SELECT id, attr_a, attr_b FROM events WHERE id IN ('1', '2') ORDER BY id"); got != "[[1 <nil> <nil>] [2 <nil> <nil>]]" {
		t.Fatalf("a renamed onto a blocked b: %s, want both NULL", got)
	}
	if got := ask(t, r, rules, "SELECT id, attr_k1, attr_k2, attr_t FROM events WHERE id IN ('3', '4') ORDER BY id"); got != "[[3 <nil> <nil> first] [4 <nil> <nil> only]]" {
		t.Fatalf("k1 and k2 renamed into t: %s, want the first key's value, else the second's", got)
	}
}

// TestReaderRuleBetweenKeysThatDifferInCase: DuckDB reads attr_Plan and
// attr_plan as one column, so a rule from one to the other has nothing to
// move and must not blank the values the column holds.
func TestReaderRuleBetweenKeysThatDifferInCase(t *testing.T) {
	for _, c := range []struct{ name, from, to string }{
		{"upper to lower", "Plan", "plan"},
		{"lower to upper", "plan", "Plan"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			at := "2026-10-12T10:00:00Z"
			a := evOf(1, "a", at)
			a.Attributes = map[string]string{"plan": "pro"}
			b := evOf(1, "b", at)
			b.Attributes = map[string]string{"plan": "free"}
			f.write(t, a, b)
			r := f.reader(t)
			q := "SELECT id, attr_plan FROM events ORDER BY id"
			want := ask(t, r, Rules{}, q)
			if want != "[[a pro] [b free]]" {
				t.Fatalf("no rule: %s", want)
			}
			rules := Rules{Attributes: []store.AttributeRule{{ProjectID: 1, Key: c.from, Action: store.EventRuleRename, To: c.to}}}
			if got := ask(t, r, rules, q); got != want {
				t.Fatalf("rename %s to %s: %s, want %s", c.from, c.to, got, want)
			}
		})
	}
}

// failingEngine answers every query with a missing _tw_state (a new
// child) and fails the load of the logs with err.
type failingEngine struct{ err error }

func (e failingEngine) Exec(context.Context, string) error { return e.err }
func (failingEngine) Query(context.Context, string, int) (engine.Result, error) {
	return engine.Result{}, &engine.QueryError{Msg: "Catalog Error: Table with name _tw_state does not exist"}
}

// TestReaderNamesTheLoadOfTheLogs: a build that fails while loading the
// open logs (out of memory, usually) is recognisable as that, and still
// carries the engine's error.
func TestReaderNamesTheLoadOfTheLogs(t *testing.T) {
	cause := &engine.QueryError{Msg: "Out of Memory Error: could not allocate block"}
	r, err := NewReader(t.TempDir(), failingEngine{err: cause})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Query(context.Background(), "SELECT 1", 0, Rules{})
	if !errors.Is(err, ErrLoadingLogs) {
		t.Fatalf("err = %v, want ErrLoadingLogs", err)
	}
	var qe *engine.QueryError
	if !errors.As(err, &qe) || qe != cause {
		t.Fatalf("err = %v, want the engine's *QueryError inside", err)
	}
}

// TestReaderSessionsJoinStoredAndLive: a dumped and derived day reads from
// its session file, today from the open log.
func TestReaderSessionsJoinStoredAndLive(t *testing.T) {
	f := newFixture(t)
	f.write(t, pageView("a1", "a", "2026-10-11T10:00:00Z", "/"), pageView("a2", "a", "2026-10-11T11:00:00Z", "/"),
		pageView("c1", "c", "2026-10-12T09:00:00Z", "/"))
	f.maintain(t, "2026-10-12")
	r := f.reader(t)
	if got := ask(t, r, Rules{}, "SELECT CAST(day AS VARCHAR), count(*) FROM sessions GROUP BY day ORDER BY day"); got != "[[2026-10-11 2] [2026-10-12 1]]" {
		t.Fatalf("sessions per day = %s", got)
	}
}

// TestReaderDerivesAStaleDayOnRead is Review Focus 1 (the reader's half): a
// day whose session file is older than its events, and a day with a late
// log beside its file, read from their events, each view once.
func TestReaderDerivesAStaleDayOnRead(t *testing.T) {
	f := newFixture(t)
	f.write(t, pageView("a1", "a", "2026-10-10T10:00:00Z", "/"), pageView("b1", "b", "2026-10-11T10:00:00Z", "/"))
	f.maintain(t, "2026-10-12")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(f.project1("sessions/2026-10-10.parquet"), old, old); err != nil {
		t.Fatal(err)
	}
	f.write(t, pageView("b2", "b2", "2026-10-11T12:00:00Z", "/")) // late: a log beside the day file
	r := f.reader(t)
	if got := ask(t, r, Rules{}, "SELECT CAST(day AS VARCHAR), count(*) FROM sessions GROUP BY day ORDER BY day"); got != "[[2026-10-10 1] [2026-10-11 2]]" {
		t.Fatalf("sessions per day = %s, want each view once", got)
	}
}

// TestReaderWithoutDerivedFiles is Review Focus 2 (the reader's half): with
// day and month files and no derived file yet, the tables read the same as
// after the pass derived them.
func TestReaderWithoutDerivedFiles(t *testing.T) {
	f := newFixture(t)
	f.write(t, named(pageView("a1", "u1", "2026-09-29T10:00:00Z", "/"), "u1", "Ada", "", ""),
		named(pageView("b1", "u2", "2026-10-10T10:00:00Z", "/"), "u2", "", "", ""))
	f.maintain(t, "2026-10-12")
	q := []string{
		"SELECT CAST(day AS VARCHAR), actor_id FROM sessions ORDER BY day",
		"SELECT id, name, views FROM identities ORDER BY id",
	}
	r := f.reader(t)
	var after []string
	for _, s := range q {
		after = append(after, ask(t, r, Rules{}, s))
	}
	for _, n := range []string{sessionsDir, identitiesFile, aliasesFile} {
		if err := os.RemoveAll(f.project1(n)); err != nil {
			t.Fatal(err)
		}
	}
	for i, s := range q {
		if got := ask(t, r, Rules{}, s); got != after[i] {
			t.Errorf("%s: %s without derived files, %s with them", s, got, after[i])
		}
	}
	if after[0] != "[[2026-09-29 u1] [2026-10-10 u2]]" || after[1] != "[[u1 Ada 1] [u2 <nil> 1]]" {
		t.Errorf("with derived files: %v", after)
	}
}

// TestReaderIdentitiesFoldStoredAndToday is Review Focus 4: the stored file
// and today's log fold into one row per id; today's name wins, an id
// named only before keeps its name, counts add, first_seen stays.
func TestReaderIdentitiesFoldStoredAndToday(t *testing.T) {
	f := newFixture(t)
	f.write(t, named(evOf(1, "p1", "2026-10-11T09:00:00Z"), "u1", "Ada", "", ""),
		named(evOf(1, "p2", "2026-10-11T09:00:00Z"), "u2", "Bo", "", ""))
	f.maintain(t, "2026-10-12")
	f.write(t, named(evOf(1, "p3", "2026-10-12T09:00:00Z"), "u1", "Ada L.", "", ""),
		named(evOf(1, "p4", "2026-10-12T09:00:00Z"), "u2", "", "", ""))
	r := f.reader(t)
	got := ask(t, r, Rules{}, "SELECT id, name, CAST(first_seen AS VARCHAR), CAST(last_seen AS VARCHAR), events FROM identities ORDER BY id")
	want := "[[u1 Ada L. 2026-10-11 09:00:00 2026-10-12 09:00:00 2] [u2 Bo 2026-10-11 09:00:00 2026-10-12 09:00:00 2]]"
	if got != want {
		t.Fatalf("identities = %s\nwant         %s", got, want)
	}
	// A stale file is not read: the project's every event is.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(f.project1(identitiesFile), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f.project1("2026-10-11.parquet"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := ask(t, r, Rules{}, "SELECT id, name, CAST(first_seen AS VARCHAR), CAST(last_seen AS VARCHAR), events FROM identities ORDER BY id"); got != want {
		t.Fatalf("identities from a stale file = %s\nwant                          %s", got, want)
	}
}

// TestReaderAliases: stored pairs and today's fold into one row per pair.
func TestReaderAliases(t *testing.T) {
	f := newFixture(t)
	pair := func(e store.Event) store.Event { e.UserID, e.InstallID = "u1", "i1"; return e }
	f.write(t, pair(evOf(1, "p1", "2026-10-11T09:00:00Z")))
	f.maintain(t, "2026-10-12")
	f.write(t, pair(evOf(1, "p2", "2026-10-12T09:00:00Z")))
	if got := ask(t, f.reader(t), Rules{}, "SELECT user_id, install_id, events FROM aliases"); got != "[[u1 i1 2]]" {
		t.Fatalf("aliases = %s", got)
	}
}

// TestReaderDerivedTablesOnAnEmptyDirectory: with nothing under raw/ the
// three tables exist, empty, with their columns.
func TestReaderDerivedTablesOnAnEmptyDirectory(t *testing.T) {
	f := newFixture(t)
	r := f.reader(t)
	for _, table := range []string{"sessions", "identities", "aliases"} {
		if got := ask(t, r, Rules{}, "SELECT count(*) FROM "+table); got != "[[0]]" {
			t.Errorf("%s: %s, want [[0]]", table, got)
		}
	}
	if got := ask(t, r, Rules{}, "SELECT count(*) FROM (DESCRIBE sessions)"); got != "[[17]]" {
		t.Errorf("sessions has %s columns, want 17", got)
	}
}
