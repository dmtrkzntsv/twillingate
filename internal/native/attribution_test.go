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

// attributeBoth answers ls natively and through the old SQL (the oracle),
// fails unless both agree, and returns the native answer by lookup id.
func attributeBoth(t *testing.T, f *archivetest.Files, days int, rules archive.Rules, ls ...archive.Lookup) map[string]archive.Found {
	t.Helper()
	ctx := context.Background()
	for _, workers := range []int{1, 3} {
		e := New(Options{Dir: f.Dir, Workers: workers})
		got, err := e.Attribute(ctx, ls, days, rules)
		if err != nil {
			t.Fatal(err)
		}
		want, err := oracleAttribute(ctx, f.Reader, f.Dir, ls, days, rules)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(ls) {
			t.Fatalf("%d found for %d lookups", len(got), len(ls))
		}
		sortFound(got)
		sortFound(want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("workers %d: native\n%+v\nwant (SQL)\n%+v", workers, got, want)
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
