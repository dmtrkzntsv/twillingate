package archive

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// pageView is a web page view of project 1 by actor (a connection hash) at at.
func pageView(id, actor, at, path string) store.Event {
	return store.Event{ID: id, ProjectID: 1, Family: store.FamilyViews, EventName: "$page_view", Kind: "web",
		ActorID: actor, ActorKind: store.ActorConnection, TS: ts(at), ReceivedAt: ts(at), Host: "a.io", Path: path}
}

// logRel is project 1's log of day as a relation with the events' columns.
func (f fixture) logRel(t *testing.T, day string) string {
	t.Helper()
	p := f.project1(day + ".log")
	keys, err := logKeys(p)
	if err != nil {
		t.Fatal(err)
	}
	return "(" + logSelect(p, columnsOf(keys), false) + ")"
}

// TestSessionsSplitAsAggViewsDailyDoes: a gap over 30 minutes starts a
// session, a declared session id holds one together whatever the gap, a
// lone view is a session of one; product events are not views. The entry
// view gives the path, referrer and UTMs, the exit view its path.
func TestSessionsSplitAsAggViewsDailyDoes(t *testing.T) {
	f := newFixture(t)
	a1 := pageView("a1", "a", "2026-10-11T10:00:00Z", "/")
	a1.ReferrerSource, a1.UTMSource = "google", "nl"
	s1 := pageView("s1", "s", "2026-10-11T11:00:00Z", "/app")
	s2 := pageView("s2", "s", "2026-10-11T13:00:00Z", "/app/2")
	s1.SessionID, s2.SessionID = "x", "x"
	f.write(t, a1,
		pageView("a2", "a", "2026-10-11T10:10:00Z", "/pricing"),
		pageView("a3", "a", "2026-10-11T10:50:00Z", "/"), // 40 minutes on: a new session
		s1, s2, // one declared session, two hours long
		pageView("b1", "b", "2026-10-11T12:00:00Z", "/"),
		evOf(1, "p1", "2026-10-11T10:05:00Z"))
	got := f.values(t, "SELECT actor_id, views, duration_sec, entry_path, exit_path, referrer_source, utm_source, session_id FROM ("+
		sessionsSelect(f.logRel(t, "2026-10-11"))+") ORDER BY actor_id, started_at")
	want := []string{
		"[a 2 600 / /pricing google nl <nil>]",
		"[a 1 0 / / <nil> <nil> <nil>]",
		"[b 1 0 / / <nil> <nil> <nil>]",
		"[s 2 7200 /app /app/2 <nil> <nil> x]",
	}
	if !eq(got, want) {
		t.Fatalf("sessions = %v\nwant       %v", got, want)
	}
}

// TestSessionsCutAtMidnight: an actor's views either side of midnight UTC
// are two sessions, one per day, as the daily rollup has them.
func TestSessionsCutAtMidnight(t *testing.T) {
	f := newFixture(t)
	f.write(t, pageView("a1", "a", "2026-10-11T23:55:00Z", "/"), pageView("a2", "a", "2026-10-12T00:05:00Z", "/"))
	rel := "(SELECT * FROM " + f.logRel(t, "2026-10-11") + " UNION ALL BY NAME SELECT * FROM " + f.logRel(t, "2026-10-12") + ")"
	got := f.values(t, "SELECT CAST(day AS VARCHAR), views FROM ("+sessionsSelect(rel)+") ORDER BY day")
	if !eq(got, []string{"[2026-10-11 1]", "[2026-10-12 1]"}) {
		t.Fatalf("sessions = %v, want one per day", got)
	}
}

func named(e store.Event, user, userName, group, groupName string) store.Event {
	e.UserID, e.UserName, e.GroupID, e.GroupName = user, userName, group, groupName
	return e
}

// TestIdentitiesKeepTheLatestNameAndCount: one row per user and per group;
// the name of the latest event that carried one; first and last seen;
// views and product events counted, measures not.
func TestIdentitiesKeepTheLatestNameAndCount(t *testing.T) {
	f := newFixture(t)
	m := evOf(1, "m1", "2026-10-11T13:00:00Z")
	m.Family = store.FamilyMeasures
	f.write(t,
		named(pageView("v1", "u1", "2026-10-11T08:00:00Z", "/"), "u1", "", "", ""),
		named(evOf(1, "p1", "2026-10-11T09:00:00Z"), "u1", "Ada", "g1", "Acme"),
		named(evOf(1, "p2", "2026-10-11T10:00:00Z"), "u1", "Ada L.", "g1", ""),
		named(evOf(1, "p3", "2026-10-11T11:00:00Z"), "u1", "", "", ""), // no name: the last one stays
		named(evOf(1, "p4", "2026-10-11T12:00:00Z"), "u2", "", "", ""), // never named
		named(m, "u2", "Bo", "", ""),                                   // a measure: neither counted nor naming
	)
	got := f.values(t, "SELECT kind, id, name, CAST(first_seen AS VARCHAR), CAST(last_seen AS VARCHAR), views, events FROM ("+
		identitiesSelect(f.logRel(t, "2026-10-11"))+")")
	want := []string{
		"[group g1 Acme 2026-10-11 09:00:00 2026-10-11 10:00:00 0 2]",
		"[user u1 Ada L. 2026-10-11 08:00:00 2026-10-11 11:00:00 1 3]",
		"[user u2 <nil> 2026-10-11 12:00:00 2026-10-11 12:00:00 0 1]",
	}
	if !eq(got, want) {
		t.Fatalf("identities = %v\nwant         %v", got, want)
	}
}

// TestIdentitiesCombineKeepsTheLatestName is Review Focus 4: folding two
// sets of rows (a stored file and today's), the newer name wins, a newer
// set with no name keeps the older one, counts add, first_seen is the
// earliest.
func TestIdentitiesCombineKeepsTheLatestName(t *testing.T) {
	f := newFixture(t)
	f.write(t,
		named(evOf(1, "p1", "2026-10-11T09:00:00Z"), "u1", "Ada", "", ""),
		named(evOf(1, "p2", "2026-10-11T09:00:00Z"), "u2", "Bo", "", ""),
		named(evOf(1, "p3", "2026-10-12T09:00:00Z"), "u1", "Ada L.", "", ""),
		named(evOf(1, "p4", "2026-10-12T09:00:00Z"), "u2", "", "", ""))
	parts := "SELECT * FROM (" + identitiesSelect(f.logRel(t, "2026-10-11")) + ") UNION ALL BY NAME SELECT * FROM (" +
		identitiesSelect(f.logRel(t, "2026-10-12")) + ")"
	got := f.values(t, "SELECT id, name, CAST(first_seen AS VARCHAR), events FROM ("+identitiesCombine(parts)+") ORDER BY id")
	want := []string{"[u1 Ada L. 2026-10-11 09:00:00 2]", "[u2 Bo 2026-10-11 09:00:00 2]"}
	if !eq(got, want) {
		t.Fatalf("combined = %v, want %v", got, want)
	}
}

// TestAliasesPairUserAndInstall: every (user_id, install_id) sent on one
// event, with first seen and the events; an install never signed in pairs
// with nothing. Combining two sets adds them.
func TestAliasesPairUserAndInstall(t *testing.T) {
	f := newFixture(t)
	pair := func(e store.Event, user, install string) store.Event {
		e.UserID, e.InstallID = user, install
		return e
	}
	f.write(t,
		pair(evOf(1, "p1", "2026-10-11T09:00:00Z"), "u1", "i1"),
		pair(evOf(1, "p2", "2026-10-11T10:00:00Z"), "u1", "i1"),
		pair(evOf(1, "p3", "2026-10-11T11:00:00Z"), "u1", "i2"), // a second device
		pair(evOf(1, "p4", "2026-10-11T12:00:00Z"), "", "i3"),
		pair(evOf(1, "p5", "2026-10-12T12:00:00Z"), "u1", "i1"))
	got := f.values(t, "SELECT user_id, install_id, CAST(first_seen AS VARCHAR), events FROM ("+aliasesSelect(f.logRel(t, "2026-10-11"))+")")
	if want := []string{"[u1 i1 2026-10-11 09:00:00 2]", "[u1 i2 2026-10-11 11:00:00 1]"}; !eq(got, want) {
		t.Fatalf("aliases = %v, want %v", got, want)
	}
	parts := "SELECT * FROM (" + aliasesSelect(f.logRel(t, "2026-10-11")) + ") UNION ALL BY NAME SELECT * FROM (" +
		aliasesSelect(f.logRel(t, "2026-10-12")) + ")"
	got = f.values(t, "SELECT user_id, install_id, CAST(last_seen AS VARCHAR), events FROM ("+aliasesCombine(parts)+") ORDER BY install_id")
	if want := []string{"[u1 i1 2026-10-12 12:00:00 3]", "[u1 i2 2026-10-11 11:00:00 1]"}; !eq(got, want) {
		t.Fatalf("combined aliases = %v, want %v", got, want)
	}
}

// TestDerivedSelectsOverNothingAreTyped: over a relation with no rows each
// SELECT gives its columns at their types, which a reader's empty tables
// rely on.
func TestDerivedSelectsOverNothingAreTyped(t *testing.T) {
	f := newFixture(t)
	empty := "(" + emptyRow() + ")"
	for name, tc := range map[string]struct{ sql, want string }{
		"sessions": {sessionsSelect(empty), "[[project_id BIGINT] [day DATE] [kind VARCHAR] [actor_id VARCHAR] [actor_kind VARCHAR] [session_id VARCHAR] " +
			"[started_at TIMESTAMP] [ended_at TIMESTAMP] [duration_sec BIGINT] [views BIGINT] [entry_host VARCHAR] [entry_path VARCHAR] " +
			"[exit_path VARCHAR] [referrer_source VARCHAR] [utm_source VARCHAR] [utm_medium VARCHAR] [utm_campaign VARCHAR]]"},
		"identities": {identitiesCombine(identitiesSelect(empty)), "[[project_id BIGINT] [kind VARCHAR] [id VARCHAR] [name VARCHAR] " +
			"[first_seen TIMESTAMP] [last_seen TIMESTAMP] [views BIGINT] [events BIGINT]]"},
		"aliases": {aliasesCombine(aliasesSelect(empty)), "[[project_id BIGINT] [user_id VARCHAR] [install_id VARCHAR] " +
			"[first_seen TIMESTAMP] [last_seen TIMESTAMP] [events BIGINT]]"},
	} {
		got := f.values(t, "SELECT column_name, column_type FROM (DESCRIBE SELECT * FROM ("+tc.sql+"))")
		if fmt.Sprint(got) != tc.want {
			t.Errorf("%s columns = %v\nwant %s", name, got, tc.want)
		}
	}
}

// TestMaintainDerivesTheClosedDays: the pass writes a session file per
// dumped day, not for today's open log, and identities and aliases.
func TestMaintainDerivesTheClosedDays(t *testing.T) {
	f := newFixture(t)
	f.write(t,
		pageView("a1", "a", "2026-10-10T10:00:00Z", "/"), pageView("a2", "a", "2026-10-10T10:10:00Z", "/x"),
		named(pageView("b1", "u1", "2026-10-11T10:00:00Z", "/"), "u1", "Ada", "", ""),
		pageView("c1", "c", "2026-10-12T09:00:00Z", "/"))
	f.maintain(t, "2026-10-12")
	want := []string{aliasesFile, identitiesFile, "sessions/2026-10-10.parquet", "sessions/2026-10-11.parquet"}
	if got := f.derived(t); !eq(got, want) {
		t.Fatalf("derived = %v, want %v", got, want)
	}
	if got := f.values(t, "SELECT actor_id, views FROM read_parquet('"+f.project1("sessions/2026-10-10.parquet")+"')"); !eq(got, []string{"[a 2]"}) {
		t.Fatalf("sessions of 2026-10-10 = %v", got)
	}
	if got := f.values(t, "SELECT id, name, views FROM read_parquet('"+f.project1(identitiesFile)+"')"); !eq(got, []string{"[u1 Ada 1]"}) {
		t.Fatalf("identities = %v", got)
	}
}

// TestALateEventRederivesItsDay is Review Focus 1 (the pass's half): a
// segment newer than its day's session file makes the next pass rewrite
// it from the day file and the segment together.
func TestALateEventRederivesItsDay(t *testing.T) {
	f := newFixture(t)
	f.write(t, pageView("a1", "a", "2026-10-11T10:00:00Z", "/"))
	f.maintain(t, "2026-10-12")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(f.project1("sessions/2026-10-11.parquet"), old, old); err != nil {
		t.Fatal(err)
	}
	f.write(t, pageView("b1", "b", "2026-10-11T23:00:00Z", "/"))
	f.maintain(t, "2026-10-12")
	if got := f.files(t); !eq(got, []string{"2026-10-11.1.parquet", "2026-10-11.parquet"}) {
		t.Fatalf("files = %v, want the day file and a segment", got)
	}
	if got := f.values(t, "SELECT actor_id FROM read_parquet('"+f.project1("sessions/2026-10-11.parquet")+"') ORDER BY actor_id"); !eq(got, []string{"[a]", "[b]"}) {
		t.Fatalf("sessions of 2026-10-11 = %v, want a's and the late b's", got)
	}
}

// TestAMergedMonthHasOneSessionsFile: when a month merges, its session
// days give way to one session file of the month.
func TestAMergedMonthHasOneSessionsFile(t *testing.T) {
	f := newFixture(t)
	f.write(t, pageView("a1", "a", "2026-09-29T10:00:00Z", "/"), pageView("b1", "b", "2026-09-30T10:00:00Z", "/"))
	f.maintain(t, "2026-10-01") // dumps both days; September is not past the late window yet
	if got := f.derived(t); !eq(got, []string{aliasesFile, identitiesFile, "sessions/2026-09-29.parquet", "sessions/2026-09-30.parquet"}) {
		t.Fatalf("derived after the dump = %v", got)
	}
	f.maintain(t, "2026-10-12")
	if got := f.derived(t); !eq(got, []string{aliasesFile, identitiesFile, "sessions/2026-09.parquet"}) {
		t.Fatalf("derived after the merge = %v", got)
	}
	if got := f.values(t, "SELECT count(*) FROM read_parquet('"+f.project1("sessions/2026-09.parquet")+"')"); !eq(got, []string{"[2]"}) {
		t.Fatalf("sessions of September = %v, want 2", got)
	}
}

// TestAMonthMergedBeforeSessionsExistedGetsThem is Review Focus 2: an
// install upgraded from v0.18.0 has month and day files and no derived
// file; the first pass derives every one.
func TestAMonthMergedBeforeSessionsExistedGetsThem(t *testing.T) {
	f := newFixture(t)
	f.write(t, pageView("a1", "a", "2026-09-29T10:00:00Z", "/"), pageView("b1", "b", "2026-10-10T10:00:00Z", "/"))
	f.maintain(t, "2026-10-12")
	for _, n := range []string{sessionsDir, identitiesFile, aliasesFile} {
		if err := os.RemoveAll(f.project1(n)); err != nil {
			t.Fatal(err)
		}
	}
	f.maintain(t, "2026-10-12")
	if got := f.derived(t); !eq(got, []string{aliasesFile, identitiesFile, "sessions/2026-09.parquet", "sessions/2026-10-10.parquet"}) {
		t.Fatalf("derived = %v", got)
	}
}

// TestRetentionTakesTheDerivedFilesAlong is Review Focus 3: a month that
// retention deletes takes its session file along, and identities lose its
// users although no events file is newer than them.
func TestRetentionTakesTheDerivedFilesAlong(t *testing.T) {
	f := newFixture(t)
	f.write(t, named(pageView("a1", "u1", "2026-07-10T10:00:00Z", "/"), "u1", "", "", ""),
		named(pageView("b1", "u2", "2026-10-10T10:00:00Z", "/"), "u2", "", "", ""))
	f.maintain(t, "2026-10-12")
	if got := f.values(t, "SELECT id FROM read_parquet('"+f.project1(identitiesFile)+"') ORDER BY id"); !eq(got, []string{"[u1]", "[u2]"}) {
		t.Fatalf("identities before = %v", got)
	}
	d, _ := civil.Parse("2026-10-12")
	if err := f.a.Maintain(context.Background(), d, 1, 60); err != nil {
		t.Fatal(err)
	}
	if got := f.derived(t); !eq(got, []string{aliasesFile, identitiesFile, "sessions/2026-10-10.parquet"}) {
		t.Fatalf("derived = %v, want July's sessions gone", got)
	}
	if got := f.values(t, "SELECT id FROM read_parquet('"+f.project1(identitiesFile)+"')"); !eq(got, []string{"[u2]"}) {
		t.Fatalf("identities after = %v, want u1 gone with July", got)
	}
}

// TestNoEventsFileNoDerivedFile: a project whose every month retention
// deleted keeps no derived file.
func TestNoEventsFileNoDerivedFile(t *testing.T) {
	f := newFixture(t)
	f.write(t, pageView("a1", "a", "2026-07-10T10:00:00Z", "/"))
	f.maintain(t, "2026-10-12")
	d, _ := civil.Parse("2026-10-12")
	if err := f.a.Maintain(context.Background(), d, 1, 60); err != nil {
		t.Fatal(err)
	}
	if got := f.derived(t); len(got) != 0 {
		t.Fatalf("derived = %v, want none", got)
	}
}

// TestDeriveReadsFilesWrittenBeforeTheNewColumns is Review Focus 5: a day
// file without install_id, user_name and group_name (written by v0.18.0)
// beside a new one derives without a binder error, its names NULL.
func TestDeriveReadsFilesWrittenBeforeTheNewColumns(t *testing.T) {
	f := newFixture(t)
	f.write(t, named(evOf(1, "p1", "2026-10-10T10:00:00Z"), "u1", "", "", ""),
		named(evOf(1, "p2", "2026-10-11T10:00:00Z"), "u1", "Ada", "", ""))
	f.maintain(t, "2026-10-12")
	old := f.project1("2026-10-10.parquet")
	if err := f.eng.Exec(context.Background(), "COPY (SELECT * EXCLUDE (install_id, user_name, group_name) FROM read_parquet('"+old+
		"')) TO '"+old+".tmp' (FORMAT parquet)"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(old+".tmp", old); err != nil {
		t.Fatal(err)
	}
	f.maintain(t, "2026-10-12")
	if got := f.values(t, "SELECT id, name, events FROM read_parquet('"+f.project1(identitiesFile)+"')"); !eq(got, []string{"[u1 Ada 2]"}) {
		t.Fatalf("identities = %v", got)
	}
}
