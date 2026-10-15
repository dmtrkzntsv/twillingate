package sqlite

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func rawAt(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestRawEventsReadsBackWhatWasWritten: a day's rows come back as the
// events WriteEvents stored, in (ts, id) order, with the install id where
// the install was the actor and the latest names identities holds
// (raw-first D19, PR 5c Decision 4).
func TestRawEventsReadsBackWhatWasWritten(t *testing.T) {
	d := newTestDB(t)
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
	if err := d.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertIdentities(ctx, []store.Identity{
		{ProjectID: 1, Kind: store.KindUser, ID: "u1", Name: "Ada"},
		{ProjectID: 1, Kind: store.KindGroup, ID: "g1", Name: "Acme"},
	}); err != nil {
		t.Fatal(err)
	}
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
	d := newTestDB(t)
	ctx := context.Background()
	if err := d.WriteEvents(ctx, []store.Event{
		{ID: "a", ProjectID: 1, Family: store.FamilyProduct, EventName: "x", TS: rawAt("2026-10-11T09:00:00Z"),
			ReceivedAt: rawAt("2026-10-11T09:00:00Z"), ActorID: "h", Platform: "web", SampleRate: 1},
	}); err != nil {
		t.Fatal(err)
	}
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
