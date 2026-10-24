package native

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func attrTS(t testing.TB, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func formView(id string, actorKind, actor string, at time.Time, f func(*store.Event)) store.Event {
	e := store.Event{ID: id, ProjectID: 1, Family: store.FamilyViews, EventName: "$page_view",
		TS: at, ReceivedAt: at, Kind: "web", ActorKind: actorKind, ActorID: actor, Path: "/"}
	if f != nil {
		f(&e)
	}
	return e
}

func sortFound(fs []archive.Found) {
	slices.SortFunc(fs, func(a, b archive.Found) int {
		if a.ProjectID != b.ProjectID {
			return int(a.ProjectID - b.ProjectID)
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// attributeBoth answers ls natively with one worker and with three, fails
// unless both are the golden (the old SQL's answer), and returns the
// native answer by lookup id.
func attributeBoth(t *testing.T, f *archivetest.Files, days int, rules archive.Rules, ls ...archive.Lookup) map[string]archive.Found {
	t.Helper()
	ctx := context.Background()
	for _, workers := range []int{1, 3} {
		e := New(Options{Dir: f.Dir, Workers: workers})
		got, err := e.Attribute(ctx, ls, days, rules)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(ls) {
			t.Fatalf("%d found for %d lookups", len(got), len(ls))
		}
		sortFound(got)
		want := frozenFound(t, days, rules, ls, got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("workers %d: native\n%+v\nwant (the golden)\n%+v", workers, got, want)
		}
	}
	got, _ := New(Options{Dir: f.Dir}).Attribute(ctx, ls, days, rules)
	out := map[string]archive.Found{}
	for _, x := range got {
		out[x.ID] = x
	}
	return out
}

// aliasEvents are the views and the sign-in of the aliases case, around at.
func aliasEvents(at time.Time) []store.Event {
	return []store.Event{
		formView("v1", store.ActorInstall, "i1", at.Add(-8*24*time.Hour), func(e *store.Event) { e.ReferrerSource = "bing" }),
		formView("v2", store.ActorInstall, "i1", at.Add(-6*24*time.Hour), func(e *store.Event) {
			e.ReferrerSource, e.UTMSource, e.UTMCampaign = "google", "cpc", "oct"
		}),
		formView("v3", store.ActorInstall, "i1", at.Add(-time.Hour), func(e *store.Event) { e.OS = "macos" }),
		formView("v4", store.ActorUser, "u1", at.Add(-10*time.Minute), func(e *store.Event) {
			e.OS, e.GroupID, e.SessionID, e.DisplayWidth, e.Consent = "ios", "g1", "s1", 390, store.ConsentGiven
		}),
		formView("v5", store.ActorInstall, "i1", at.Add(5*time.Minute), func(e *store.Event) { e.ReferrerSource = "later" }),
		// The sign-in: user u1 and install i1 on one event make the alias.
		{ID: "p1", ProjectID: 1, Family: store.FamilyProduct, EventName: "login",
			TS: at.Add(-30 * time.Minute), ReceivedAt: at.Add(-30 * time.Minute),
			ActorKind: store.ActorUser, ActorID: "u1", UserID: "u1", InstallID: "i1"},
	}
}

// checkAliases asserts the aliases case's answer.
func checkAliases(t *testing.T, f *archivetest.Files, at time.Time) {
	t.Helper()
	got := attributeBoth(t, f, 7, archive.Rules{},
		archive.Lookup{ProjectID: 1, ID: "user", ActorKind: store.ActorUser, ActorID: "u1", At: at},
		archive.Lookup{ProjectID: 1, ID: "install", ActorKind: store.ActorInstall, ActorID: "i1", At: at},
		archive.Lookup{ProjectID: 1, ID: "sent-install", ActorKind: store.ActorConnection, ActorID: "h", InstallID: "i1", At: at},
		archive.Lookup{ProjectID: 1, ID: "nobody", ActorKind: store.ActorConnection, ActorID: "h2", At: at},
		archive.Lookup{ProjectID: 2, ID: "other-project", ActorKind: store.ActorInstall, ActorID: "i1", At: at},
	)
	google := store.Event{ReferrerSource: "google", UTMSource: "cpc", UTMCampaign: "oct"}
	for _, id := range []string{"user", "install", "sent-install"} {
		if !reflect.DeepEqual(got[id].Source, google) || got[id].ProjectID != 1 {
			t.Errorf("%s: source = %+v, want google/cpc/oct", id, got[id].Source)
		}
	}
	if n := got["user"].Newest; n.OS != "ios" || n.GroupID != "g1" || n.SessionID != "s1" || n.DisplayWidth != 390 ||
		n.Consent != store.ConsentGiven || n.Kind != "web" {
		t.Errorf("user: newest = %+v, want v4's", n)
	}
	if n := got["install"].Newest; n.OS != "macos" {
		t.Errorf("install: newest = %+v, want v3's (v5 is after the submission)", n)
	}
	for _, id := range []string{"nobody", "other-project"} {
		if !reflect.DeepEqual(got[id].Source, store.Event{}) || !reflect.DeepEqual(got[id].Newest, store.Event{}) {
			t.Errorf("%s: found %+v", id, got[id])
		}
	}
	// A day's window reaches neither google (6 days back) nor bing.
	if s := attributeBoth(t, f, 1, archive.Rules{}, archive.Lookup{ProjectID: 1, ID: "install", ActorKind: store.ActorInstall,
		ActorID: "i1", At: at})["install"].Source; !reflect.DeepEqual(s, store.Event{}) {
		t.Errorf("1-day window: source = %+v, want direct", s)
	}
}

// The source is the newest session with one in the window, across the
// actor, its own install id and the install ids aliased to its user; the
// rest is the newest view's. A session past the window or after the
// submission counts for nothing. Here every event is in a log: the
// sessions and aliases are derived on read.
func TestAttributeFollowsAliasesAndTheWindow(t *testing.T) {
	f := archivetest.New(t)
	at := attrTS(t, "2026-10-14T12:00:00Z")
	f.Write(t, aliasEvents(at)...)
	checkAliases(t, f, at)
}

// The same answers once the days before the submission's are dumped and
// derived: stored sessions and aliases, the submission's day a live gap
// over its open log.
func TestAttributeOverStoredAndLiveFiles(t *testing.T) {
	f := archivetest.New(t)
	at := attrTS(t, "2026-10-14T12:00:00Z")
	f.Write(t, aliasEvents(at)...)
	f.Maintain(t, "2026-10-14")
	all, err := archive.ListFiles(f.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if p := all[1]; p.Aliases == "" || len(p.Sessions) == 0 || len(p.SessionGaps) == 0 || len(p.Logs) == 0 {
		t.Fatalf("files = %+v, want stored aliases and sessions, and a live gap over a log", p)
	}
	checkAliases(t, f, at)

	// A sign-in only in the open log, of a user whose install's views are
	// in the stored files, aliases them too.
	f.Write(t, store.Event{ID: "p2", ProjectID: 1, Family: store.FamilyProduct, EventName: "login",
		TS: at.Add(-5 * time.Minute), ReceivedAt: at.Add(-5 * time.Minute),
		ActorKind: store.ActorUser, ActorID: "u2", UserID: "u2", InstallID: "i1"})
	got := attributeBoth(t, f, 7, archive.Rules{},
		archive.Lookup{ProjectID: 1, ID: "u2", ActorKind: store.ActorUser, ActorID: "u2", At: at})
	if s := got["u2"].Source; s.ReferrerSource != "google" {
		t.Errorf("u2: source = %+v, want i1's google", s)
	}
}

// Consent after landing: the landing's views went out as the connection
// hash, the later ones with an install id. The lookup follows the
// submission's connection hash too, so the landing's source is found.
func TestAttributeFollowsTheConnectionHash(t *testing.T) {
	f := archivetest.New(t)
	at := attrTS(t, "2026-10-14T12:00:00Z")
	f.Write(t,
		formView("v1", store.ActorConnection, "h", at.Add(-20*time.Minute), func(e *store.Event) { e.UTMSource = "newsletter" }),
		formView("v2", store.ActorInstall, "i1", at.Add(-10*time.Minute), func(e *store.Event) { e.OS = "macos" }),
	)
	got := attributeBoth(t, f, 7, archive.Rules{},
		archive.Lookup{ProjectID: 1, ID: "consented", ActorKind: store.ActorInstall, ActorID: "i1", InstallID: "i1", ConnectionID: "h", At: at},
		archive.Lookup{ProjectID: 1, ID: "no-hash", ActorKind: store.ActorInstall, ActorID: "i1", InstallID: "i1", At: at},
	)
	if s := got["consented"].Source; s.UTMSource != "newsletter" {
		t.Errorf("consented: source = %+v, want the landing's newsletter", s)
	}
	if n := got["consented"].Newest; n.OS != "macos" {
		t.Errorf("consented: newest = %+v, want v2's", n)
	}
	if s := got["no-hash"].Source; !reflect.DeepEqual(s, store.Event{}) {
		t.Errorf("no-hash: source = %+v, want direct", s)
	}
}

// Aliases are per project: user u1 signing in with install i1 on project
// 2 does not make project 1's i1 visits u1's.
func TestAttributeAliasesStayInTheirProject(t *testing.T) {
	f := archivetest.New(t)
	at := attrTS(t, "2026-10-14T12:00:00Z")
	f.Write(t,
		formView("v1", store.ActorInstall, "i1", at.Add(-time.Hour), func(e *store.Event) { e.ReferrerSource = "google" }),
		store.Event{ID: "p1", ProjectID: 2, Family: store.FamilyProduct, EventName: "login",
			TS: at.Add(-30 * time.Minute), ReceivedAt: at.Add(-30 * time.Minute),
			ActorKind: store.ActorUser, ActorID: "u1", UserID: "u1", InstallID: "i1"},
	)
	got := attributeBoth(t, f, 7, archive.Rules{},
		archive.Lookup{ProjectID: 1, ID: "user", ActorKind: store.ActorUser, ActorID: "u1", At: at},
		archive.Lookup{ProjectID: 1, ID: "install", ActorKind: store.ActorInstall, ActorID: "i1", At: at},
	)
	if s := got["user"].Source; !reflect.DeepEqual(s, store.Event{}) {
		t.Errorf("project 1's u1: source = %+v, want direct (the alias is project 2's)", s)
	}
	if s := got["install"].Source; s.ReferrerSource != "google" {
		t.Errorf("project 1's i1: source = %+v, want google", s)
	}
}

// Ids are the client's: a quote or a NUL in one is data.
func TestAttributeQuotesIDs(t *testing.T) {
	f := archivetest.New(t)
	at := attrTS(t, "2026-10-14T12:00:00Z")
	odd := "o'b\x00x"
	f.Write(t, formView("v1", store.ActorInstall, odd, at.Add(-time.Minute), func(e *store.Event) { e.ReferrerSource = "google" }))
	got := attributeBoth(t, f, 7, archive.Rules{},
		archive.Lookup{ProjectID: 1, ID: "x'y", ActorKind: store.ActorInstall, ActorID: odd, InstallID: "'; DROP", At: at})
	if got["x'y"].Source.ReferrerSource != "google" {
		t.Fatalf("found = %+v", got)
	}
}

// The views are read with the rules (a blocked view is no newest view);
// the sessions without (a blocked view's session still sources). Ties
// rank as the SQL ranks them: sessions by actor_kind, then actor_id;
// views by id, descending. Lookups of one batch arrive at different
// times, each with its own window, the arrival cut to the second.
func TestAttributeRulesTiesAndWindows(t *testing.T) {
	f := archivetest.New(t)
	at := attrTS(t, "2026-10-14T12:00:00Z")
	f.Write(t,
		// Two sessions starting together, of the install and the connection.
		formView("t1", store.ActorInstall, "i1", at.Add(-2*time.Hour), func(e *store.Event) { e.ReferrerSource = "from-install" }),
		formView("t2", store.ActorConnection, "h", at.Add(-2*time.Hour), func(e *store.Event) { e.ReferrerSource = "from-connection" }),
		// Two views at one time: the greater id is the newest.
		formView("a-view", store.ActorInstall, "i2", at.Add(-time.Hour), func(e *store.Event) { e.OS = "a" }),
		formView("b-view", store.ActorInstall, "i2", at.Add(-time.Hour), func(e *store.Event) { e.OS = "b" }),
		// A blocked view, newer than both, with its own source.
		formView("blocked", store.ActorInstall, "i2", at.Add(-time.Minute), func(e *store.Event) {
			e.EventName, e.OS, e.UTMSource = "$bot_view", "blocked", "bot"
		}),
		// A second after an arrival 0.9 seconds past at: past it, the
		// arrival cut to its second.
		formView("late", store.ActorInstall, "i3", at.Add(time.Second), func(e *store.Event) { e.OS = "late" }),
		formView("early", store.ActorInstall, "i3", at.Add(-3*24*time.Hour), func(e *store.Event) { e.OS = "early" }),
	)
	rules := archive.Rules{Events: []store.EventRule{{ProjectID: 1, Family: store.FamilyViews, Name: "$bot_view",
		Action: store.EventRuleBlock}}}
	got := attributeBoth(t, f, 7, rules,
		archive.Lookup{ProjectID: 1, ID: "tie", ActorKind: store.ActorInstall, ActorID: "i1", ConnectionID: "h", At: at},
		archive.Lookup{ProjectID: 1, ID: "views", ActorKind: store.ActorInstall, ActorID: "i2", At: at},
		archive.Lookup{ProjectID: 1, ID: "second", ActorKind: store.ActorInstall, ActorID: "i3", At: at.Add(900 * time.Millisecond)},
		archive.Lookup{ProjectID: 1, ID: "short", ActorKind: store.ActorInstall, ActorID: "i3", At: at.Add(-2 * 24 * time.Hour)},
	)
	if s := got["tie"].Source.ReferrerSource; s != "from-connection" {
		t.Errorf("tie: source %q, want the connection's (its kind sorts first)", s)
	}
	if n := got["views"].Newest.OS; n != "b" {
		t.Errorf("views: newest %q, want b-view's (blocked left out, ids descending)", n)
	}
	if s := got["views"].Source.UTMSource; s != "bot" {
		t.Errorf("views: source %+v, want the blocked view's session (no rule on sessions)", got["views"].Source)
	}
	if n := got["second"].Newest.OS; n != "early" {
		t.Errorf("second: newest %q, want early (late is past the arrival's second)", n)
	}
	if n := got["short"].Newest.OS; n != "early" {
		t.Errorf("short: newest %q, want early", n)
	}

	// Without the rule, the blocked view is the newest.
	if n := attributeBoth(t, f, 7, archive.Rules{}, archive.Lookup{ProjectID: 1, ID: "views", ActorKind: store.ActorInstall,
		ActorID: "i2", At: at})["views"].Newest.OS; n != "blocked" {
		t.Errorf("no rule: newest %q, want blocked", n)
	}
}

// TestAttributeWithoutFilesReadsNone: a lookup whose project has no
// events file, or none as new as its window's first day, is answered from
// the file names alone, found with nothing. Only the rest are looked up.
func TestAttributeWithoutFilesReadsNone(t *testing.T) {
	dir := t.TempDir()
	// Project 2's only file is a month whose last day is far outside the
	// window of a lookup in October; empty, it fails any read.
	if err := os.MkdirAll(filepath.Join(dir, "2"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2", "2026-01.parquet"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	e := New(Options{Dir: dir})
	at := attrTS(t, "2026-10-14T12:00:00Z")
	ls := []archive.Lookup{{ProjectID: 1, ID: "a", ActorKind: store.ActorInstall, ActorID: "i1", At: at},
		{ProjectID: 2, ID: "b", ActorKind: store.ActorInstall, ActorID: "i2", At: at}}
	got, err := e.Attribute(context.Background(), ls, 7, archive.Rules{})
	if err != nil {
		t.Fatalf("Attribute: %v, want no read", err)
	}
	want := []archive.Found{{ProjectID: 1, ID: "a"}, {ProjectID: 2, ID: "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("found = %+v, want %+v", got, want)
	}

	// A project with a file in the window is looked up (and here fails).
	if err := os.WriteFile(filepath.Join(dir, "2", "2026-10-10.parquet"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Attribute(context.Background(), ls, 7, archive.Rules{}); err == nil {
		t.Fatal("Attribute with a file in the window read nothing")
	}
}

func TestAttributeNothingAsked(t *testing.T) {
	if got, err := New(Options{Dir: t.TempDir()}).Attribute(context.Background(), nil, 7, archive.Rules{}); err != nil || got != nil {
		t.Fatalf("Attribute(nil) = %v, %v", got, err)
	}
}

// aliasOnlyInParquet writes a sign-in of u1 with install i1 on a day the
// pass then dumps, and i1's views with a source, so the alias is in a day
// file (and aliases.parquet) and in no log.
func aliasOnlyInParquet(t *testing.T) (*archivetest.Files, time.Time) {
	t.Helper()
	f := archivetest.New(t)
	at := attrTS(t, "2026-10-14T12:00:00Z")
	day := at.Add(-2 * 24 * time.Hour)
	f.Write(t,
		formView("v1", store.ActorInstall, "i1", day, func(e *store.Event) { e.ReferrerSource = "google" }),
		store.Event{ID: "p1", ProjectID: 1, Family: store.FamilyProduct, EventName: "login", TS: day.Add(time.Minute),
			ReceivedAt: day.Add(time.Minute), ActorKind: store.ActorUser, ActorID: "u1", UserID: "u1", InstallID: "i1"},
		formView("v2", store.ActorInstall, "i9", at.Add(-time.Hour), nil), // today's log, of no alias
	)
	f.Maintain(t, "2026-10-14")
	return f, at
}

// While aliases.parquet is missing, or older than an events file, the
// aliases come from every events file: a sign-in only in a day file still
// makes the install's visits the user's.
func TestAttributeStaleAliasesReadTheEventsFiles(t *testing.T) {
	check := func(t *testing.T, f *archivetest.Files, at time.Time) {
		t.Helper()
		all, err := archive.ListFiles(f.Dir)
		if err != nil {
			t.Fatal(err)
		}
		if all[1].Aliases != "" || len(all[1].Parquet) == 0 {
			t.Fatalf("files = %+v, want no current aliases file and a day file", all[1])
		}
		got := attributeBoth(t, f, 7, archive.Rules{},
			archive.Lookup{ProjectID: 1, ID: "user", ActorKind: store.ActorUser, ActorID: "u1", At: at})
		if s := got["user"].Source; s.ReferrerSource != "google" {
			t.Errorf("user: source = %+v, want i1's google through the day file's alias", s)
		}
	}
	t.Run("missing", func(t *testing.T) {
		f, at := aliasOnlyInParquet(t)
		if err := os.Remove(filepath.Join(f.Dir, "1", "aliases.parquet")); err != nil {
			t.Fatal(err)
		}
		check(t, f, at)
	})
	t.Run("stale", func(t *testing.T) {
		f, at := aliasOnlyInParquet(t)
		day := filepath.Join(f.Dir, "1", "2026-10-12.parquet")
		later := time.Now().Add(time.Hour)
		if err := os.Chtimes(day, later, later); err != nil {
			t.Fatal(err)
		}
		check(t, f, at)
	})
}

// appendRaw appends lines to project 1's log of day as they are, past the
// log's own checks: a line without an id, as no client sends but a file
// may hold.
func appendRaw(t *testing.T, f *archivetest.Files, day string, lines ...string) {
	t.Helper()
	lf, err := os.OpenFile(filepath.Join(f.Dir, "1", day+".log"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	for _, l := range lines {
		if _, err := lf.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

// The window's first instant is in it; a NULL view id ranks after any id
// at the same time, and alone wins by its time (a log keeps one line
// without an id, the first: the two cases are in two days' logs); lookups of two projects
// in one batch each read their own project.
func TestAttributeEdgesNullIDsAndProjects(t *testing.T) {
	f := archivetest.New(t)
	at := attrTS(t, "2026-10-14T12:00:00Z")
	t1, t2 := at.Add(-24*time.Hour), at.Add(-time.Hour)
	f.Write(t,
		formView("edge", store.ActorInstall, "i4", at.Add(-7*24*time.Hour), func(e *store.Event) { e.ReferrerSource = "edge" }),
		formView("past", store.ActorInstall, "i4", at.Add(-7*24*time.Hour-time.Hour), func(e *store.Event) { e.ReferrerSource = "past" }),
		formView("named", store.ActorInstall, "i5", t1, func(e *store.Event) { e.OS = "named" }),
		formView("older", store.ActorInstall, "i6", t2.Add(-time.Minute), func(e *store.Event) { e.OS = "older" }),
		formView("p2", store.ActorInstall, "i5", t2, func(e *store.Event) { e.ProjectID, e.OS, e.UTMSource = 2, "two", "p2" }),
	)
	line := func(actor, os string, ts time.Time) string {
		return `{"v":1,"project_id":1,"family":"views","event_name":"$page_view","ts":"` + ts.UTC().Format(time.RFC3339) +
			`","day":"` + archive.Day(ts) + `","received_at":"` + ts.UTC().Format(time.RFC3339) + `","kind":"web","actor_kind":"install","actor_id":"` +
			actor + `","path":"/","os":"` + os + `"}`
	}
	appendRaw(t, f, "2026-10-13", line("i5", "nullid", t1))
	appendRaw(t, f, "2026-10-14", line("i6", "nullid", t2))
	got := attributeBoth(t, f, 7, archive.Rules{},
		archive.Lookup{ProjectID: 1, ID: "edge", ActorKind: store.ActorInstall, ActorID: "i4", At: at},
		archive.Lookup{ProjectID: 1, ID: "named", ActorKind: store.ActorInstall, ActorID: "i5", At: at},
		archive.Lookup{ProjectID: 1, ID: "nullid", ActorKind: store.ActorInstall, ActorID: "i6", At: at},
		archive.Lookup{ProjectID: 2, ID: "two", ActorKind: store.ActorInstall, ActorID: "i5", At: at},
	)
	if s := got["edge"].Source.ReferrerSource; s != "edge" {
		t.Errorf("edge: source %q, want the view at the window's first instant", s)
	}
	if n := got["named"].Newest.OS; n != "named" {
		t.Errorf("named: newest %q, want the view with an id (a NULL id sorts last)", n)
	}
	if n := got["nullid"].Newest.OS; n != "nullid" {
		t.Errorf("nullid: newest %q, want the NULL-id view (newer)", n)
	}
	if x := got["two"]; x.ProjectID != 2 || x.Newest.OS != "two" || x.Source.UTMSource != "p2" {
		t.Errorf("two: %+v, want project 2's own view", x)
	}
}
