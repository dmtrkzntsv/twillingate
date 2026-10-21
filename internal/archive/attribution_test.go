package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func viewOf(id string, actorKind, actor string, at time.Time, f func(*store.Event)) store.Event {
	e := store.Event{ID: id, ProjectID: 1, Family: store.FamilyViews, EventName: "$page_view",
		TS: at, ReceivedAt: at, Kind: "web", ActorKind: actorKind, ActorID: actor, Path: "/"}
	if f != nil {
		f(&e)
	}
	return e
}

func attribute(t *testing.T, r *Reader, days int, ls ...Lookup) map[string]Found {
	t.Helper()
	found, err := r.Attribute(context.Background(), ls, days, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != len(ls) {
		t.Fatalf("%d found for %d lookups", len(found), len(ls))
	}
	out := map[string]Found{}
	for _, f := range found {
		out[f.ID] = f
	}
	return out
}

// The source is the newest session with one in the window, across the
// actor, its own install id and the install ids aliased to its user; the
// rest is the newest view's. A session past the window or after the
// submission counts for nothing.
func TestAttributeFollowsAliasesAndTheWindow(t *testing.T) {
	f := newFixture(t)
	at := ts("2026-10-14T12:00:00Z")
	f.write(t,
		viewOf("v1", store.ActorInstall, "i1", at.Add(-8*24*time.Hour), func(e *store.Event) { e.ReferrerSource = "bing" }),
		viewOf("v2", store.ActorInstall, "i1", at.Add(-6*24*time.Hour), func(e *store.Event) {
			e.ReferrerSource, e.UTMSource, e.UTMCampaign = "google", "cpc", "oct"
		}),
		viewOf("v3", store.ActorInstall, "i1", at.Add(-time.Hour), func(e *store.Event) { e.OS = "macos" }),
		viewOf("v4", store.ActorUser, "u1", at.Add(-10*time.Minute), func(e *store.Event) {
			e.OS, e.GroupID, e.SessionID, e.DisplayWidth, e.Consent = "ios", "g1", "s1", 390, store.ConsentGiven
		}),
		viewOf("v5", store.ActorInstall, "i1", at.Add(5*time.Minute), func(e *store.Event) { e.ReferrerSource = "later" }),
		// The sign-in: user u1 and install i1 on one event make the alias.
		store.Event{ID: "p1", ProjectID: 1, Family: store.FamilyProduct, EventName: "login",
			TS: at.Add(-30 * time.Minute), ReceivedAt: at.Add(-30 * time.Minute),
			ActorKind: store.ActorUser, ActorID: "u1", UserID: "u1", InstallID: "i1"},
	)
	r := f.reader(t)
	got := attribute(t, r, 7,
		Lookup{ProjectID: 1, ID: "user", ActorKind: store.ActorUser, ActorID: "u1", At: at},
		Lookup{ProjectID: 1, ID: "install", ActorKind: store.ActorInstall, ActorID: "i1", At: at},
		Lookup{ProjectID: 1, ID: "sent-install", ActorKind: store.ActorConnection, ActorID: "h", InstallID: "i1", At: at},
		Lookup{ProjectID: 1, ID: "nobody", ActorKind: store.ActorConnection, ActorID: "h2", At: at},
		Lookup{ProjectID: 2, ID: "other-project", ActorKind: store.ActorInstall, ActorID: "i1", At: at},
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
	if s := attribute(t, r, 1, Lookup{ProjectID: 1, ID: "install", ActorKind: store.ActorInstall, ActorID: "i1", At: at})["install"].Source; !reflect.DeepEqual(s, store.Event{}) {
		t.Errorf("1-day window: source = %+v, want direct", s)
	}
}

// Consent after landing: the landing's views went out as the connection
// hash, the later ones with an install id. The lookup follows the
// submission's connection hash too, so the landing's source is found.
func TestAttributeFollowsTheConnectionHash(t *testing.T) {
	f := newFixture(t)
	at := ts("2026-10-14T12:00:00Z")
	f.write(t,
		viewOf("v1", store.ActorConnection, "h", at.Add(-20*time.Minute), func(e *store.Event) { e.UTMSource = "newsletter" }),
		viewOf("v2", store.ActorInstall, "i1", at.Add(-10*time.Minute), func(e *store.Event) { e.OS = "macos" }),
	)
	got := attribute(t, f.reader(t), 7,
		Lookup{ProjectID: 1, ID: "consented", ActorKind: store.ActorInstall, ActorID: "i1", InstallID: "i1", ConnectionID: "h", At: at},
		Lookup{ProjectID: 1, ID: "no-hash", ActorKind: store.ActorInstall, ActorID: "i1", InstallID: "i1", At: at},
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
	f := newFixture(t)
	at := ts("2026-10-14T12:00:00Z")
	f.write(t,
		viewOf("v1", store.ActorInstall, "i1", at.Add(-time.Hour), func(e *store.Event) { e.ReferrerSource = "google" }),
		store.Event{ID: "p1", ProjectID: 2, Family: store.FamilyProduct, EventName: "login",
			TS: at.Add(-30 * time.Minute), ReceivedAt: at.Add(-30 * time.Minute),
			ActorKind: store.ActorUser, ActorID: "u1", UserID: "u1", InstallID: "i1"},
	)
	got := attribute(t, f.reader(t), 7,
		Lookup{ProjectID: 1, ID: "user", ActorKind: store.ActorUser, ActorID: "u1", At: at},
		Lookup{ProjectID: 1, ID: "install", ActorKind: store.ActorInstall, ActorID: "i1", At: at},
	)
	if s := got["user"].Source; !reflect.DeepEqual(s, store.Event{}) {
		t.Errorf("project 1's u1: source = %+v, want direct (the alias is project 2's)", s)
	}
	if s := got["install"].Source; s.ReferrerSource != "google" {
		t.Errorf("project 1's i1: source = %+v, want google", s)
	}
}

// Ids are the client's: a quote or a NUL in one is data, never SQL.
func TestAttributeQuotesIDs(t *testing.T) {
	f := newFixture(t)
	at := ts("2026-10-14T12:00:00Z")
	odd := "o'b\x00x"
	f.write(t, viewOf("v1", store.ActorInstall, odd, at.Add(-time.Minute), func(e *store.Event) { e.ReferrerSource = "google" }))
	got := attribute(t, f.reader(t), 7,
		Lookup{ProjectID: 1, ID: "x'y", ActorKind: store.ActorInstall, ActorID: odd, InstallID: "'; DROP", At: at})
	if got["x'y"].Source.ReferrerSource != "google" {
		t.Fatalf("found = %+v", got)
	}
}

// TestAttributeWithoutFilesRunsNoQuery: a lookup whose project has no
// events file, or none as new as its window's first day, is answered from
// the file names alone, found with nothing, so a form on a site that
// sends no views spawns no engine child. Only the rest are looked up.
func TestAttributeWithoutFilesRunsNoQuery(t *testing.T) {
	dir := t.TempDir()
	// Project 2's only file is a month whose last day is far outside the
	// window of a lookup in October.
	if err := os.MkdirAll(filepath.Join(dir, "2"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2", "2026-01.parquet"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(dir, failingEngine{err: errors.New("no query expected")})
	if err != nil {
		t.Fatal(err)
	}
	at := ts("2026-10-14T12:00:00Z")
	ls := []Lookup{{ProjectID: 1, ID: "a", ActorKind: store.ActorInstall, ActorID: "i1", At: at},
		{ProjectID: 2, ID: "b", ActorKind: store.ActorInstall, ActorID: "i2", At: at}}
	got, err := r.Attribute(context.Background(), ls, 7, Rules{})
	if err != nil {
		t.Fatalf("Attribute: %v, want no query", err)
	}
	want := []Found{{ProjectID: 1, ID: "a"}, {ProjectID: 2, ID: "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("found = %+v, want %+v", got, want)
	}

	// A project with a file in the window is looked up (and here fails).
	if err := os.WriteFile(filepath.Join(dir, "2", "2026-10-10.parquet"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Attribute(context.Background(), ls, 7, Rules{}); err == nil {
		t.Fatal("Attribute with a file in the window ran no query")
	}
}

func TestAttributeNothingAsked(t *testing.T) {
	f := newFixture(t)
	if got, err := f.reader(t).Attribute(context.Background(), nil, 7, Rules{}); err != nil || got != nil {
		t.Fatalf("Attribute(nil) = %v, %v", got, err)
	}
}
