package archive

import (
	"context"
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

func named(e store.Event, user, userName, group, groupName string) store.Event {
	e.UserID, e.UserName, e.GroupID, e.GroupName = user, userName, group, groupName
	return e
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
	if got := texts(readTable(t, f.project1("sessions/2026-10-10.parquet"), "actor_id", "views").pick(t, "actor_id", "views")); !eq(got, []string{"[a 2]"}) {
		t.Fatalf("sessions of 2026-10-10 = %v", got)
	}
	if got := texts(readTable(t, f.project1(identitiesFile), "id", "name", "views").pick(t, "id", "name", "views")); !eq(got, []string{"[u1 Ada 1]"}) {
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
	if got := sortedTexts(readTable(t, f.project1("sessions/2026-10-11.parquet"), "actor_id").pick(t, "actor_id")); !eq(got, []string{"[a]", "[b]"}) {
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
	if got := len(readTable(t, f.project1("sessions/2026-09.parquet")).rows); got != 2 {
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
	if got := sortedTexts(readTable(t, f.project1(identitiesFile), "id").pick(t, "id")); !eq(got, []string{"[u1]", "[u2]"}) {
		t.Fatalf("identities before = %v", got)
	}
	d, _ := civil.Parse("2026-10-12")
	if err := f.a.Maintain(context.Background(), d, 1, 60); err != nil {
		t.Fatal(err)
	}
	if got := f.derived(t); !eq(got, []string{aliasesFile, identitiesFile, "sessions/2026-10-10.parquet"}) {
		t.Fatalf("derived = %v, want July's sessions gone", got)
	}
	if got := texts(readTable(t, f.project1(identitiesFile), "id").pick(t, "id")); !eq(got, []string{"[u2]"}) {
		t.Fatalf("identities after = %v, want u1 gone with July", got)
	}
}

// TestAFailedRewriteDropsTheStaleIdentities: when retention deleted a month
// and the identities rewrite then fails, the old file (which still names the
// expired users and is newer than every events file left) is removed, so it
// reads as missing instead of current.
func TestAFailedRewriteDropsTheStaleIdentities(t *testing.T) {
	f := newFixture(t)
	f.write(t, named(pageView("a1", "u1", "2026-07-10T10:00:00Z", "/"), "u1", "Ada", "", ""),
		named(pageView("b1", "u2", "2026-10-10T10:00:00Z", "/"), "u2", "", "", ""))
	f.maintain(t, "2026-10-12")
	// A non-empty directory where the temporary file goes fails the write.
	block := f.project1(identitiesFile + ".tmp")
	if err := os.MkdirAll(block+"/x", 0o750); err != nil {
		t.Fatal(err)
	}
	d, _ := civil.Parse("2026-10-12")
	// A project's failure is logged, not returned.
	if err := f.a.Maintain(context.Background(), d, 1, 60); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.project1(identitiesFile)); !os.IsNotExist(err) {
		t.Fatalf("stat identities = %v, want the stale file removed", err)
	}
	if _, err := os.Stat(f.project1(aliasesFile)); err != nil {
		t.Fatalf("aliases = %v, want the rewritten file", err)
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
	writeTable(t, old+".x", readTable(t, old).without("install_id", "user_name", "group_name"))
	if err := os.Rename(old+".x", old); err != nil {
		t.Fatal(err)
	}
	f.maintain(t, "2026-10-12")
	if got := texts(readTable(t, f.project1(identitiesFile), "id", "name", "events").pick(t, "id", "name", "events")); !eq(got, []string{"[u1 Ada 2]"}) {
		t.Fatalf("identities = %v", got)
	}
}
