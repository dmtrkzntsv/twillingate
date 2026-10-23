package archive

// The DuckDB COPYs of sessionsSelect, identitiesSelect and aliasesSelect
// these tests compare with leave with DuckDB in step 6.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// eventsOf is files read as one relation with every fixed column at its
// type: a column a file lacks (written before it existed) reads NULL. It
// is the relation the pass derived its files from through DuckDB before
// the derivers moved to Go (derived.go), kept as their oracle.
func eventsOf(files []string) string {
	return "(" + emptyRow() + " UNION ALL BY NAME SELECT * FROM read_parquet(" + sqlList(files) + ", union_by_name=true))"
}

// derivedCase is the events files one parity case derives from. untimed
// says some view has no ts, which the Go sessions read at the epoch
// (deriveSessions): the case's sessions are not compared.
type derivedCase struct {
	name    string
	untimed bool
	build   func(t *testing.T, dir string) []string
}

// jsonLines is each map as one log line.
func jsonLines(t *testing.T, lines ...map[string]any) string {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		j, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	return b.String()
}

// manyEvents is n events of project 1 on day, every family, ts
// increasing and often within one second (a log keeps whole seconds): actors under two kinds, declared and
// undeclared sessions, gaps either side of 30 minutes, users and groups
// with and without names, pairs of user and install.
func manyEvents(t *testing.T, day string, n int, seed uint64) string {
	t.Helper()
	base, err := time.Parse(time.RFC3339, day+"T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(seed, 7))
	at := base
	fresh := false
	var b strings.Builder
events:
	for i := range n {
		gap := time.Duration(r.IntN(900)) * time.Millisecond
		if r.IntN(800) == 0 {
			gap = time.Duration(1795+r.IntN(10)) * time.Second
		}
		at = at.Add(gap + time.Microsecond)
		if fresh {
			// A view with a session id or an event with a name, and the
			// event after it, each have a second of their own, where the
			// SQL's result is not defined. In a second that mixes declared
			// and undeclared views of one actor after a gap, which of them
			// sessionsSelect's window takes first (it orders by the second
			// alone) is not, nor so whether the gap starts a session;
			// Sessionize takes the first by ts, then id. Of two names of
			// one identity in one second, arg_max may take either;
			// IdentityMap keeps the first read.
			at = at.Truncate(time.Second).Add(time.Second + time.Duration(r.IntN(500))*time.Millisecond)
		}
		fresh = false
		e := evOf(1, fmt.Sprintf("%s-%06d", day, i), day+"T00:00:00Z")
		e.TS, e.ReceivedAt = at, at
		switch r.IntN(10) {
		case 0, 1, 2, 3, 4, 5:
			e.Family, e.EventName = store.FamilyViews, "$page_view"
			e.Kind = []string{"web", "app"}[r.IntN(2)]
			e.ActorID, e.ActorKind = fmt.Sprintf("a%d", r.IntN(60)), []string{"connection", "install"}[r.IntN(2)]
			e.Host, e.Path = "a.io", fmt.Sprintf("/p/%d", r.IntN(9))
			if r.IntN(5) == 0 {
				e.SessionID = fmt.Sprintf("s%d", r.IntN(20))
			}
			if r.IntN(4) == 0 {
				e.ReferrerSource, e.UTMSource, e.UTMMedium, e.UTMCampaign = "google", "nl", "email", fmt.Sprintf("c%d", r.IntN(3))
			}
		case 6:
			e.Family, e.EventName, e.Measure = store.FamilyMeasures, "$web_vitals", "lcp"
		}
		if r.IntN(3) == 0 {
			e.UserID = fmt.Sprintf("u%d", r.IntN(80))
			if r.IntN(3) == 0 {
				e.UserName = fmt.Sprintf("User %d", r.IntN(1000))
			}
		}
		if r.IntN(4) == 0 {
			e.GroupID = fmt.Sprintf("g%d", r.IntN(15))
			if r.IntN(2) == 0 {
				e.GroupName = fmt.Sprintf("Group %d", r.IntN(1000))
			}
		}
		if r.IntN(3) == 0 {
			e.InstallID = fmt.Sprintf("i%d", r.IntN(40))
		}
		if e.SessionID != "" || e.UserName != "" || e.GroupName != "" {
			at = at.Truncate(time.Second).Add(time.Second + time.Duration(r.IntN(500))*time.Millisecond)
			e.TS, e.ReceivedAt = at, at
			fresh = true
		}
		if at.Format("2006-01-02") != day {
			break events
		}
		b.WriteString(logOf(t, e))
	}
	return b.String()
}

// handWritten is lines ingest never writes that the derivers still read
// as the SQL does: views without a kind, an actor, an id or a day, another
// project's, declared ids "2" and "", a 1800.2 s gap, two views in one
// second ordered by id; identities named by an empty name, a measure that
// names none, a group without a name.
func handWritten(t *testing.T) string {
	n := 0
	view := func(at string, more map[string]any) map[string]any {
		n++
		l := map[string]any{"v": 1, "id": fmt.Sprintf("h%02d", n), "project_id": 1, "family": "views", "ts": "2026-10-11T" + at + "Z",
			"day": "2026-10-11", "kind": "web", "actor_id": "a", "actor_kind": "connection", "host": "a.io", "path": "/" + at}
		for k, v := range more {
			if v == nil {
				delete(l, k)
			} else {
				l[k] = v
			}
		}
		return l
	}
	return jsonLines(t,
		view("10:00:00", nil),
		view("10:30:00.2", nil), // 1800.2 s on: 1800 whole seconds, the same session
		view("11:00:01", nil),   // more than 30 minutes on: a new one
		view("11:00:01.5", map[string]any{"id": "h-b"}),
		view("11:00:01.4", map[string]any{"id": "h-a"}), // the same second, ordered by id
		view("11:10:00", map[string]any{"session_id": "2"}),
		view("12:00:00", map[string]any{"session_id": "2"}),
		view("12:00:01", map[string]any{"session_id": ""}), // not in "2"'s second: see manyEvents
		view("09:00:00", map[string]any{"kind": nil}),
		view("09:00:00", map[string]any{"actor_id": nil}),
		view("09:00:00", map[string]any{"kind": nil, "actor_id": nil, "id": nil}),
		view("09:00:00", map[string]any{"id": nil, "actor_id": "nullid", "utm_source": "x"}),
		view("09:30:00", map[string]any{"day": nil, "actor_id": "noday"}),
		view("09:30:00", map[string]any{"project_id": 2, "actor_id": "other"}),
		view("08:00:00", map[string]any{"actor_id": "b", "kind": "app", "user_id": "u1", "user_name": ""}),
		view("08:00:00", map[string]any{"actor_id": "b", "kind": "web", "path": "/web"}),
		map[string]any{"v": 1, "id": "p1", "project_id": 1, "family": "product", "ts": "2026-10-11T08:30:00Z", "day": "2026-10-11",
			"user_id": "u1", "group_id": "g1", "install_id": "i1"},
		map[string]any{"v": 1, "id": "p2", "project_id": 2, "family": "product", "ts": "2026-10-11T08:31:00Z", "day": "2026-10-11",
			"user_id": "u1", "user_name": "Other", "install_id": "i1"},
		map[string]any{"v": 1, "id": "m1", "project_id": 1, "family": "measures", "ts": "2026-10-11T08:32:00Z", "day": "2026-10-11",
			"user_id": "u9", "user_name": "Nobody", "install_id": "i9"},
		map[string]any{"v": 1, "id": "e1", "project_id": 1, "family": "", "ts": "2026-10-11T08:33:00Z", "day": "2026-10-11",
			"user_id": "u8", "install_id": "i8"},
	)
}

func derivedCases(t *testing.T, f fixture) []derivedCase {
	derive := []store.Event{ // the events of derive_test.go's tests, on two days
		pageView("a1", "a", "2026-10-11T10:00:00Z", "/"), pageView("a2", "a", "2026-10-11T10:10:00Z", "/pricing"),
		pageView("a3", "a", "2026-10-11T10:50:00Z", "/"), pageView("b1", "b", "2026-10-11T12:00:00Z", "/"),
		pageView("m1", "m", "2026-10-11T23:55:00Z", "/"),
		named(pageView("v1", "u1", "2026-10-11T08:00:00Z", "/"), "u1", "", "", ""),
		named(evOf(1, "p1", "2026-10-11T09:00:00Z"), "u1", "Ada", "g1", "Acme"),
		named(evOf(1, "p2", "2026-10-11T10:00:00Z"), "u1", "Ada L.", "g1", ""),
		named(evOf(1, "p3", "2026-10-11T11:00:00Z"), "u1", "", "", ""),
		named(evOf(1, "p4", "2026-10-11T12:00:00Z"), "u2", "", "", ""),
	}
	s1, s2 := pageView("s1", "s", "2026-10-11T11:00:00Z", "/app"), pageView("s2", "s", "2026-10-11T13:00:00Z", "/app/2")
	s1.SessionID, s2.SessionID = "x", "x"
	derive[0].ReferrerSource, derive[0].UTMSource = "google", "nl"
	pair := named(evOf(1, "q1", "2026-10-11T09:30:00Z"), "u1", "", "", "")
	pair.InstallID = "i1"
	derive = append(derive, s1, s2, pair)
	next := []store.Event{pageView("m2", "m", "2026-10-12T00:05:00Z", "/"), named(evOf(1, "p5", "2026-10-12T09:00:00Z"), "u1", "Ada M.", "", "")}
	next[1].InstallID = "i1"
	return []derivedCase{
		{name: "derive tests", build: func(t *testing.T, dir string) []string {
			return []string{f.goDay(t, dir, "2026-10-11.parquet", logOf(t, derive...)), f.goDay(t, dir, "2026-10-12.parquet", logOf(t, next...))}
		}},
		{name: "a day and its segments", build: func(t *testing.T, dir string) []string {
			late := pageView("a9", "a", "2026-10-11T10:05:00Z", "/late")
			return []string{
				f.goDay(t, dir, "2026-10-11.parquet", logOf(t, derive...)),
				f.goDay(t, dir, "2026-10-11.1.parquet", logOf(t, late, derive[1], derive[6])), // a repeated id counts twice
				f.goDay(t, dir, "2026-10-11.2.parquet", manyEvents(t, "2026-10-11", 500, 3)),
			}
		}},
		{name: "a Go-merged month", build: func(t *testing.T, dir string) []string {
			days := []string{
				f.goDay(t, dir, "2026-09-01.parquet", manyEvents(t, "2026-09-01", 4000, 1)),
				f.goDay(t, dir, "2026-09-02.parquet", manyEvents(t, "2026-09-02", 4000, 2)),
				f.goDay(t, dir, "2026-09-02.1.parquet", manyEvents(t, "2026-09-02", 300, 9)),
			}
			month := filepath.Join(dir, "2026-09.parquet")
			if err := mergeFiles(context.Background(), days, month); err != nil {
				t.Fatal(err)
			}
			return []string{month}
		}},
		{name: "a DuckDB-written month", build: func(t *testing.T, dir string) []string {
			days := []string{
				f.duckDay(t, dir, "2026-09-01.parquet", manyEvents(t, "2026-09-01", 3000, 4)),
				f.duckDay(t, dir, "2026-09-02.parquet", manyEvents(t, "2026-09-02", 3000, 5)),
				f.duckDay(t, dir, "2026-09-03.parquet", handWritten(t)),
			}
			// One row group spanning the days, every family.
			return []string{f.duckCopy(t, filepath.Join(dir, "2026-09.parquet"), mergeQuery(days))}
		}},
		{name: "many rows over every file", build: func(t *testing.T, dir string) []string {
			var in []string
			for d := 1; d <= 3; d++ {
				day := fmt.Sprintf("2026-10-%02d", d)
				in = append(in, f.goDay(t, dir, day+".parquet", manyEvents(t, day, 25000, uint64(d))))
			}
			return in
		}},
		{name: "files written before the new columns", build: func(t *testing.T, dir string) []string {
			old := f.goDay(t, dir, "2026-10-10.parquet", logOf(t, derive...))
			older := f.duckCopy(t, filepath.Join(dir, "2026-10-10.old.parquet"),
				"SELECT * EXCLUDE (install_id, user_name, group_name) FROM read_parquet("+quote(old)+")")
			return []string{older, f.goDay(t, dir, "2026-10-12.parquet", logOf(t, next...))}
		}},
		{name: "hand-written", build: func(t *testing.T, dir string) []string {
			return []string{f.goDay(t, dir, "2026-10-11.parquet", handWritten(t))}
		}},
		{name: "no views and no identities", build: func(t *testing.T, dir string) []string {
			return []string{f.goDay(t, dir, "2026-10-11.parquet", logOf(t, evOf(1, "p", "2026-10-11T10:00:00Z")))}
		}},
		{name: "events without a ts", untimed: true, build: func(t *testing.T, dir string) []string {
			return []string{f.goDay(t, dir, "2026-10-11.parquet", jsonLines(t,
				map[string]any{"v": 1, "id": "t1", "project_id": 1, "family": "product", "day": "2026-10-11", "user_id": "u1", "install_id": "i1"},
				map[string]any{"v": 1, "id": "t2", "project_id": 1, "family": "views", "day": "2026-10-11", "user_id": "u1", "group_id": "g1", "group_name": "G"},
				map[string]any{"v": 1, "id": "t3", "project_id": 1, "family": "product", "ts": "2026-10-11T10:00:00Z", "day": "2026-10-11",
					"user_id": "u1", "user_name": "Ada", "install_id": "i1"},
				map[string]any{"v": 1, "id": "t4", "project_id": 1, "family": "product", "day": "2026-10-11", "user_id": "u2", "user_name": "Untimed",
					"install_id": "i2"},
			))}
		}},
	}
}

// TestGoDerivedFilesMatchDuckDB: over every case's events files, the Go
// sessions, identities and aliases files equal DuckDB's COPY of
// sessionsSelect, identitiesSelect and aliasesSelect over eventsOf, read
// through DuckDB: the same columns of the same types, the same rows, in
// the SELECTs' order. Identities and aliases have one row per key, so
// their rows are compared in order; sessions tie on their ORDER BY (an
// actor under two kinds), so they are compared as a multiset and the Go
// file's order is checked.
func TestGoDerivedFilesMatchDuckDB(t *testing.T) {
	f := newFixtureWith(t, 4, "512MB")
	for i, c := range derivedCases(t, f) {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(f.log.Dir(), fmt.Sprintf("case-%d", i))
			inputs := c.build(t, dir)
			out := func(name string) string { return filepath.Join(dir, "out", name) }
			if err := os.MkdirAll(out(""), 0o750); err != nil {
				t.Fatal(err)
			}
			src := eventsOf(inputs)
			if !c.untimed {
				f.duckCopy(t, out("duck-sessions.parquet"), sessionsSelect(src))
				if err := deriveSessions(context.Background(), inputs, out("go-sessions.parquet")); err != nil {
					t.Fatal(err)
				}
				f.sameDerived(t, out("go-sessions.parquet"), out("duck-sessions.parquet"), "project_id, day, started_at, actor_id")
			}
			fold, err := foldIdentities(context.Background(), inputs)
			if err != nil {
				t.Fatal(err)
			}
			f.duckCopy(t, out("duck-identities.parquet"), identitiesSelect(src))
			if err := fold.writeIdentities(out("go-identities.parquet")); err != nil {
				t.Fatal(err)
			}
			f.sameDerived(t, out("go-identities.parquet"), out("duck-identities.parquet"), "")
			f.duckCopy(t, out("duck-aliases.parquet"), aliasesSelect(src))
			if err := fold.writeAliases(out("go-aliases.parquet")); err != nil {
				t.Fatal(err)
			}
			f.sameDerived(t, out("go-aliases.parquet"), out("duck-aliases.parquet"), "")
			if n := f.values(t, "SELECT count(*) FROM read_parquet("+quote(out("duck-identities.parquet"))+")"); c.name != "no views and no identities" && eq(n, []string{"[0]"}) {
				t.Errorf("case %s has no identities to compare", c.name)
			}
		})
	}
}

// sameDerived fails unless the Go file goFile equals DuckDB's duck: the
// same DESCRIBE and rows. With orderBy empty the rows are compared in file
// order; otherwise as a multiset, and goFile's rows must be in orderBy's
// order (NULLs last).
func (f fixture) sameDerived(t *testing.T, goFile, duck, orderBy string) {
	t.Helper()
	if got, want := f.describe(t, goFile), f.describe(t, duck); !eq(got, want) {
		t.Fatalf("%s columns\n%v\nwant DuckDB's\n%v", filepath.Base(goFile), got, want)
	}
	rel := func(p string) string { return "read_parquet(" + quote(p) + ", file_row_number=true)" }
	if orderBy == "" {
		q := func(p string) []string {
			return f.values(t, "SELECT * EXCLUDE (file_row_number) FROM "+rel(p)+" ORDER BY file_row_number")
		}
		if got, want := q(goFile), q(duck); !eq(got, want) {
			for i := range max(len(got), len(want)) {
				if i >= len(got) || i >= len(want) || got[i] != want[i] {
					t.Fatalf("%s: %d rows, DuckDB's %d; first difference at row %d:\n%v\nwant\n%v",
						filepath.Base(goFile), len(got), len(want), i, got[min(i, len(got)-1)], want[min(i, len(want)-1)])
				}
			}
		}
		return
	}
	f.sameRows(t, "read_parquet("+quote(goFile)+")", "read_parquet("+quote(duck)+")")
	keys := func(order string) []string {
		return f.values(t, "SELECT "+orderBy+" FROM "+rel(goFile)+" ORDER BY "+order)
	}
	if got, want := keys("file_row_number"), keys(orderBy); !eq(got, want) {
		t.Fatalf("%s is not in %s order", filepath.Base(goFile), orderBy)
	}
}

// TestGoSessionsOfAViewWithoutATS pins the one difference from
// sessionsSelect, which ingest never meets: a view without a ts is
// sessionized at the epoch, as native's deriveSessions does.
func TestGoSessionsOfAViewWithoutATS(t *testing.T) {
	f := newFixture(t)
	dir := f.project1("")
	in := f.goDay(t, dir, "2026-10-11.parquet", jsonLines(t,
		map[string]any{"v": 1, "id": "t1", "project_id": 1, "family": "views", "day": "2026-10-11", "kind": "web", "actor_id": "a", "path": "/x"},
		map[string]any{"v": 1, "id": "t2", "project_id": 1, "family": "views", "ts": "2026-10-11T10:00:00Z", "day": "2026-10-11",
			"kind": "web", "actor_id": "a", "path": "/y"}))
	out := f.project1("sessions.parquet")
	if err := deriveSessions(context.Background(), []string{in}, out); err != nil {
		t.Fatal(err)
	}
	got := f.values(t, "SELECT CAST(started_at AS VARCHAR), views, entry_path FROM read_parquet("+quote(out)+")")
	if want := []string{"[1970-01-01 00:00:00 1 /x]", "[2026-10-11 10:00:00 1 /y]"}; !eq(got, want) {
		t.Fatalf("sessions = %v, want %v", got, want)
	}
}

// TestDerivedFilesRefuseAColumnOfAnotherType: an events file whose fixed
// column has another type (only by hand) fails the deriver, naming the
// file and the column, and leaves no file.
func TestDerivedFilesRefuseAColumnOfAnotherType(t *testing.T) {
	f := newFixture(t)
	dir := f.project1("")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	bad := f.duckCopy(t, filepath.Join(dir, "2026-10-11.parquet"), "SELECT 'views' AS family, CAST(1 AS INTEGER) AS ts")
	out := f.project1("sessions.parquet")
	err := deriveSessions(context.Background(), []string{bad}, out)
	if err == nil || !strings.Contains(err.Error(), "2026-10-11.parquet") || !strings.Contains(err.Error(), "column ts") {
		t.Fatalf("deriveSessions = %v, want the file and column named", err)
	}
	if _, err := foldIdentities(context.Background(), []string{bad}); err == nil {
		t.Fatal("foldIdentities read a ts of another type")
	}
	if exists(out) || exists(out+".tmp") {
		t.Fatal("a refused derive left a file")
	}
}
