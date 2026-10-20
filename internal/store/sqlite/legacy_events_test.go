package sqlite

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// legacyDB opens a database at LastEventsVersion, the last schema with
// the events table: where the import's reads run.
func legacyDB(t *testing.T) *DB {
	t.Helper()
	return newTestDBAt(t, LastEventsVersion)
}

// insertLegacy writes evs into events as ingest did through 041, for the
// import's reads to find.
func insertLegacy(t *testing.T, db *DB, evs ...store.Event) {
	t.Helper()
	for _, e := range evs {
		attrs := e.Attributes
		if attrs == nil {
			attrs = map[string]string{}
		}
		blob, err := json.Marshal(attrs)
		if err != nil {
			t.Fatal(err)
		}
		rate := e.SampleRate
		if rate == 0 {
			rate = 1
		}
		if _, err := db.db.Exec(`INSERT INTO events
			(id, project_id, family, event_name, ts, day, received_at, kind,
			 actor_id, actor_kind, user_id, group_id, session_id,
			 host, path, referrer_source, utm_source, utm_medium, utm_campaign,
			 platform, os, os_version, os_name, browser, browser_version, browser_locale,
			 app_version, app_locale, device, device_model, display_width, display_height,
			 country, consent, attributes, value, measure, sample_rate)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			e.ID, e.ProjectID, string(e.Family), e.EventName,
			e.TS.UTC().Format(tsFormat), e.TS.UTC().Format("2006-01-02"), e.ReceivedAt.UTC().Format(tsFormat), e.Kind,
			e.ActorID, e.ActorKind, e.UserID, e.GroupID, e.SessionID,
			e.Host, e.Path, e.ReferrerSource, e.UTMSource, e.UTMMedium, e.UTMCampaign,
			e.Platform, e.OS, e.OSVersion, e.OSName, e.Browser, e.BrowserVersion, e.BrowserLocale,
			e.AppVersion, e.AppLocale, e.Device, e.DeviceModel, e.DisplayWidth, e.DisplayHeight,
			e.Country, e.Consent, string(blob), e.Value, e.Measure, rate); err != nil {
			t.Fatalf("event %s: %v", e.ID, err)
		}
	}
}

func rawAt(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestRawEventsReadsBackWhatWasWritten: a day's rows come back as the
// events ingest stored, in (ts, id) order, with the install id where
// the install was the actor and the latest names identities holds
// (raw-first D19, PR 5c Decision 4).
func TestRawEventsReadsBackWhatWasWritten(t *testing.T) {
	d := legacyDB(t)
	ctx := context.Background()
	v := 12.5
	evs := []store.Event{
		{ID: "b", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup",
			TS: rawAt("2026-10-11T10:00:00Z"), ReceivedAt: rawAt("2026-10-11T10:00:01Z"),
			ActorID: "u1", ActorKind: store.ActorUser, UserID: "u1", GroupID: "g1",
			Platform: "web", Country: "DE", Consent: store.ConsentGiven, SampleRate: 1,
			Attributes: map[string]string{"plan": "pro"}},
		{ID: "a", ProjectID: 1, Family: store.FamilyViews, EventName: "$page_view",
			TS: rawAt("2026-10-11T09:00:00Z"), ReceivedAt: rawAt("2026-10-11T09:00:00Z"), Kind: "web",
			ActorID: "i1", ActorKind: store.ActorInstall, Host: "x.io", Path: "/", Platform: "web",
			DisplayWidth: 390, DisplayHeight: 844, Consent: store.ConsentNone, SampleRate: 1},
		{ID: "c", ProjectID: 1, Family: store.FamilyMeasures, EventName: "load",
			TS: rawAt("2026-10-11T11:00:00Z"), ReceivedAt: rawAt("2026-10-11T11:00:00Z"),
			ActorID: "h", ActorKind: store.ActorConnection, Platform: "web",
			Value: &v, Measure: store.MeasureTime, SampleRate: 0.5},
		// Another day and another project: not read.
		{ID: "d", ProjectID: 1, Family: store.FamilyProduct, EventName: "x", TS: rawAt("2026-10-12T00:00:00Z"),
			ReceivedAt: rawAt("2026-10-12T00:00:00Z"), ActorID: "h", Platform: "web", SampleRate: 1},
		{ID: "e", ProjectID: 2, Family: store.FamilyProduct, EventName: "x", TS: rawAt("2026-10-11T09:00:00Z"),
			ReceivedAt: rawAt("2026-10-11T09:00:00Z"), ActorID: "h", Platform: "web", SampleRate: 1},
	}
	insertLegacy(t, d, evs...)
	execAll(t, d, `INSERT INTO identities (project_id, kind, id, name) VALUES (1, 'user', 'u1', 'Ada'), (1, 'group', 'g1', 'Acme')`)
	day, _ := civil.Parse("2026-10-11")
	got, err := d.RawEvents(ctx, 1, day)
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := evs[1], evs[0], evs[2]
	a.InstallID = "i1"
	b.UserName, b.GroupName = "Ada", "Acme"
	if want := []store.Event{a, b, c}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RawEvents =\n%+v\nwant\n%+v", got, want)
	}
}

// TestRawEventsReadsOlderRows: rows in shapes ingest no longer writes copy
// without failing the day: a ts with fractional seconds, no received_at
// (a zero ReceivedAt), and attribute values that are JSON numbers,
// booleans or null (a value is its JSON text, null is dropped).
func TestRawEventsReadsOlderRows(t *testing.T) {
	d := legacyDB(t)
	ctx := context.Background()
	insertLegacy(t, d, store.Event{ID: "a", ProjectID: 1, Family: store.FamilyProduct, EventName: "x",
		TS: rawAt("2026-10-11T09:00:00Z"), ReceivedAt: rawAt("2026-10-11T09:00:00Z"), ActorID: "h", Platform: "web", SampleRate: 1})
	if _, err := d.db.ExecContext(ctx, `UPDATE events SET ts = '2026-10-11T09:00:00.250Z', received_at = '',
		attributes = '{"n":42,"b":true,"z":null,"s":"x"}' WHERE id = 'a'`); err != nil {
		t.Fatal(err)
	}
	day, _ := civil.Parse("2026-10-11")
	got, err := d.RawEvents(ctx, 1, day)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if want := rawAt("2026-10-11T09:00:00.25Z"); !got[0].TS.Equal(want) {
		t.Fatalf("TS = %v, want %v", got[0].TS, want)
	}
	if !got[0].ReceivedAt.IsZero() {
		t.Fatalf("ReceivedAt of a row without one = %v, want zero", got[0].ReceivedAt)
	}
	if want := map[string]string{"n": "42", "b": "true", "s": "x"}; !reflect.DeepEqual(got[0].Attributes, want) {
		t.Fatalf("Attributes = %v, want %v", got[0].Attributes, want)
	}
}
