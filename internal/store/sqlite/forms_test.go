package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

var formsNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// newSub is a submission to "contact" on project 1, received at formsNow.
func newSub(id string, fields map[string]string) store.NewSubmission {
	return store.NewSubmission{
		Submission: store.Submission{
			ProjectID: 1, ID: id, Form: "contact", ReceivedAt: formsNow,
			Fields: fields,
		},
		DraftUntil: formsNow.Add(7 * 24 * time.Hour),
		Event: store.Event{
			ID: id, ProjectID: 1, Family: store.FamilyProduct, EventName: "$form_submit",
			TS: formsNow, ReceivedAt: formsNow, Kind: "web",
			ActorKind: "user", ActorID: "a1", Host: "example.com", Path: "/contact",
			Attributes: map[string]string{"form": "contact"},
		},
	}
}

func countRows(t *testing.T, db *DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func storedFields(t *testing.T, db *DB, id string) map[string]string {
	t.Helper()
	var blob string
	if err := db.db.QueryRow(`SELECT fields FROM submissions WHERE project_id=1 AND id=?`, id).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(blob), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func formFieldNames(t *testing.T, db *DB) []string {
	t.Helper()
	var blob string
	if err := db.db.QueryRow(`SELECT fields FROM forms WHERE project_id=1 AND name='contact'`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	var names []string
	if err := json.Unmarshal([]byte(blob), &names); err != nil {
		t.Fatal(err)
	}
	return names
}

func approve(t *testing.T, db *DB, expected string) {
	t.Helper()
	execAll(t, db, `UPDATE forms SET status='approved', expected_fields='`+expected+`', draft_until=NULL,
		approved_at='2026-10-06T11:00:00Z' WHERE project_id=1 AND name='contact'`)
}

func TestWriteSubmissionFirstCreatesDraft(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	form, inserted, err := db.WriteSubmission(ctx, newSub("s1", map[string]string{"name": "Ann", "email": "a@x.io"}))
	if err != nil || !inserted {
		t.Fatalf("inserted=%v err=%v", inserted, err)
	}
	if form.Status != store.FormDraft || form.Name != "contact" || form.ProjectID != 1 {
		t.Fatalf("form = %+v", form)
	}
	if form.DraftUntil == nil || !form.DraftUntil.Equal(formsNow.Add(7*24*time.Hour)) {
		t.Fatalf("draft_until = %v", form.DraftUntil)
	}
	if !form.CreatedAt.Equal(formsNow) {
		t.Fatalf("created_at = %v", form.CreatedAt)
	}
	if form.LastSubmittedAt == nil || !form.LastSubmittedAt.Equal(formsNow) {
		t.Fatalf("last_submitted_at = %v", form.LastSubmittedAt)
	}
	if want := []string{"email", "name"}; !reflect.DeepEqual(form.Fields, want) {
		t.Fatalf("fields = %v, want %v", form.Fields, want)
	}
	if got := storedFields(t, db, "s1"); !reflect.DeepEqual(got, map[string]string{"name": "Ann", "email": "a@x.io"}) {
		t.Fatalf("stored = %v", got)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM events`); n != 0 {
		t.Fatalf("a draft wrote %d events", n)
	}
	var recv string
	var attribution *string
	if err := db.db.QueryRow(`SELECT received_at, attribution FROM submissions
		WHERE project_id=1 AND id='s1'`).Scan(&recv, &attribution); err != nil {
		t.Fatal(err)
	}
	if recv != "2026-10-06T12:00:00Z" || attribution != nil {
		t.Fatalf("row = %q %v, want no attribution stored as NULL", recv, attribution)
	}
}

// TestWriteSubmissionStoresAttribution: the attribution is a JSON object
// that leaves empty values out.
func TestWriteSubmissionStoresAttribution(t *testing.T) {
	db := newTestDB(t)
	n := newSub("s1", map[string]string{"a": "1"})
	n.Submission.Attribution = store.Attribution{Referrer: "google", UTMSource: "x", UTMCampaign: "oct"}
	if _, _, err := db.WriteSubmission(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	var blob string
	if err := db.db.QueryRow(`SELECT attribution FROM submissions WHERE id='s1'`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if want := `{"referrer":"google","utm_source":"x","utm_campaign":"oct"}`; blob != want {
		t.Fatalf("attribution = %s, want %s", blob, want)
	}
}

func TestWriteSubmissionMergesFieldNames(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, _, err := db.WriteSubmission(ctx, newSub("s1", map[string]string{"name": "Ann"})); err != nil {
		t.Fatal(err)
	}
	later := newSub("s2", map[string]string{"company": "X", "name": "Bob"})
	later.Submission.ReceivedAt = formsNow.Add(time.Minute)
	later.Event.TS = later.Submission.ReceivedAt
	form, inserted, err := db.WriteSubmission(ctx, later)
	if err != nil || !inserted {
		t.Fatalf("inserted=%v err=%v", inserted, err)
	}
	if want := []string{"company", "name"}; !reflect.DeepEqual(form.Fields, want) {
		t.Fatalf("fields = %v, want %v", form.Fields, want)
	}
	if !form.LastSubmittedAt.Equal(formsNow.Add(time.Minute)) {
		t.Fatalf("last_submitted_at = %v", form.LastSubmittedAt)
	}
	// The draft_until of the first stands: later submissions do not extend it.
	if !form.DraftUntil.Equal(formsNow.Add(7 * 24 * time.Hour)) {
		t.Fatalf("draft_until = %v", form.DraftUntil)
	}
}

func TestWriteSubmissionDuplicateIDIsNoOp(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, inserted, err := db.WriteSubmission(ctx, newSub("s1", map[string]string{"a": "1"})); err != nil || !inserted {
		t.Fatalf("first: inserted=%v err=%v", inserted, err)
	}
	// The retry carries another field: it must not leak into the form's names.
	form, inserted, err := db.WriteSubmission(ctx, newSub("s1", map[string]string{"a": "2", "b": "3"}))
	if err != nil || inserted {
		t.Fatalf("retry: inserted=%v err=%v", inserted, err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM submissions`); n != 1 {
		t.Fatalf("%d submissions", n)
	}
	if got := storedFields(t, db, "s1"); got["a"] != "1" {
		t.Fatalf("stored = %v", got)
	}
	if want := []string{"a"}; !reflect.DeepEqual(form.Fields, want) {
		t.Fatalf("fields = %v, want %v", form.Fields, want)
	}
}

func TestWriteSubmissionApprovedKeepsExpectedAndWritesEvent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, _, err := db.WriteSubmission(ctx, newSub("s0", map[string]string{"name": "Ann"})); err != nil {
		t.Fatal(err)
	}
	approve(t, db, `["name"]`)
	execAll(t, db, `DELETE FROM submissions`)

	n := newSub("s1", map[string]string{"name": "Bob", "tracking": "zzz"})
	form, inserted, err := db.WriteSubmission(ctx, n)
	if err != nil || !inserted {
		t.Fatalf("inserted=%v err=%v", inserted, err)
	}
	if form.Status != store.FormApproved {
		t.Fatalf("form = %+v", form)
	}
	if got := storedFields(t, db, "s1"); !reflect.DeepEqual(got, map[string]string{"name": "Bob"}) {
		t.Fatalf("stored = %v, want only the expected field", got)
	}
	if want := []string{"name", "tracking"}; !reflect.DeepEqual(formFieldNames(t, db), want) {
		t.Fatalf("fields = %v, want %v (dropped names stay recorded)", formFieldNames(t, db), want)
	}
	attributeAll(t, db, nil)
	var id, family, name, ts, day, kind, actorKind, actorID, host, path, attrs string
	if err := db.db.QueryRow(`SELECT id, family, event_name, ts, day, kind, actor_kind, actor_id, host, path, attributes
		FROM events`).Scan(&id, &family, &name, &ts, &day, &kind, &actorKind, &actorID, &host, &path, &attrs); err != nil {
		t.Fatal(err)
	}
	if id != "s1" || family != "product" || name != "$form_submit" || ts != "2026-10-06T12:00:00Z" ||
		day != "2026-10-06" || kind != "web" || actorKind != "user" || actorID != "a1" ||
		host != "example.com" || path != "/contact" || attrs != `{"form":"contact"}` {
		t.Fatalf("event = %q %q %q %q %q %q %q %q %q %q %q", id, family, name, ts, day, kind, actorKind, actorID, host, path, attrs)
	}
	// The event is found by the key a later delete uses, and its ts is the
	// submission's received_at.
	var recv string
	if err := db.db.QueryRow(`SELECT received_at FROM submissions WHERE project_id=1 AND id='s1'`).Scan(&recv); err != nil {
		t.Fatal(err)
	}
	if recv != ts {
		t.Fatalf("received_at %q != event ts %q", recv, ts)
	}
	// ingest recorded that the form key arrived.
	if c := countRows(t, db, `SELECT COUNT(*) FROM usage_attributes WHERE project_id=1 AND attr_key='form'`); c != 1 {
		t.Fatalf("usage_attributes rows for form = %d", c)
	}

	// A retry after the first succeeded writes no second event.
	if _, inserted, err := db.WriteSubmission(ctx, n); err != nil || inserted {
		t.Fatalf("retry: inserted=%v err=%v", inserted, err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM events`); c != 1 {
		t.Fatalf("%d events after retry", c)
	}
}

// TestWriteSubmissionRetryAfterApprovalWritesNoEvent covers a JSON retry
// whose first attempt was stored as a draft: one row, still no event.
func TestWriteSubmissionRetryAfterApprovalWritesNoEvent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	n := newSub("s1", map[string]string{"name": "Ann"})
	if _, _, err := db.WriteSubmission(ctx, n); err != nil {
		t.Fatal(err)
	}
	approve(t, db, `["name"]`)
	if _, inserted, err := db.WriteSubmission(ctx, n); err != nil || inserted {
		t.Fatalf("retry: inserted=%v err=%v", inserted, err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM events`); c != 0 {
		t.Fatalf("%d events", c)
	}
}

func TestWriteSubmissionRefusedWhenClosed(t *testing.T) {
	cases := []struct {
		name  string
		setup string // SQL run after the draft exists
	}{
		{"archived", `UPDATE forms SET archived_at='2026-10-06T11:00:00Z'`},
		{"draft expired", `UPDATE forms SET draft_until='2026-10-06T12:00:00Z'`},
		{"closed", `UPDATE forms SET closes_at='2026-10-06T11:59:59Z'`},
		{"approved and closed", `UPDATE forms SET status='approved', expected_fields='["a"]', draft_until=NULL, closes_at='2026-10-06T12:00:00Z'`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := newTestDB(t)
			ctx := context.Background()
			if _, _, err := db.WriteSubmission(ctx, newSub("s0", map[string]string{"a": "1"})); err != nil {
				t.Fatal(err)
			}
			execAll(t, db, c.setup, `UPDATE forms SET return_url='https://site.com/back'`)
			subs := countRows(t, db, `SELECT COUNT(*) FROM submissions`)
			evs := countRows(t, db, `SELECT COUNT(*) FROM events`)
			var fieldsBefore, lastBefore string
			if err := db.db.QueryRow(`SELECT fields, COALESCE(last_submitted_at,'') FROM forms`).Scan(&fieldsBefore, &lastBefore); err != nil {
				t.Fatal(err)
			}

			n := newSub("s1", map[string]string{"a": "1", "new": "x"})
			form, inserted, err := db.WriteSubmission(ctx, n)
			if !errors.Is(err, store.ErrFormClosed) || inserted {
				t.Fatalf("inserted=%v err=%v, want ErrFormClosed", inserted, err)
			}
			// The refusal hands back the form, so ingest can send the
			// visitor to its return_url with the error fragment.
			if form.Name != "contact" || form.ReturnURL != "https://site.com/back" {
				t.Fatalf("refused with form %+v, want the form as it stands", form)
			}
			if countRows(t, db, `SELECT COUNT(*) FROM submissions`) != subs ||
				countRows(t, db, `SELECT COUNT(*) FROM events`) != evs {
				t.Fatal("a refused submission wrote rows")
			}
			var fieldsAfter, lastAfter string
			if err := db.db.QueryRow(`SELECT fields, COALESCE(last_submitted_at,'') FROM forms`).Scan(&fieldsAfter, &lastAfter); err != nil {
				t.Fatal(err)
			}
			if fieldsAfter != fieldsBefore || lastAfter != lastBefore {
				t.Fatal("a refused submission changed the form")
			}
		})
	}
}

// TestWriteSubmissionDraftUntilInPastRefusesAndRollsBack: a brand-new form
// is never refused (its draft_until is in the future), but a refusal must
// not leave a half-created row either way: the transaction rolls back.
func TestWriteSubmissionDraftUntilInPastRefusesAndRollsBack(t *testing.T) {
	db := newTestDB(t)
	n := newSub("s1", map[string]string{"a": "1"})
	n.DraftUntil = formsNow.Add(-time.Hour)
	_, _, err := db.WriteSubmission(context.Background(), n)
	if !errors.Is(err, store.ErrFormClosed) {
		t.Fatalf("err = %v", err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM forms`); c != 0 {
		t.Fatalf("%d forms left behind", c)
	}
}

func TestWriteSubmissionIsPerProject(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	other := newSub("s1", map[string]string{"a": "1"})
	other.Submission.ProjectID = 2
	other.Event.ProjectID = 2
	if _, _, err := db.WriteSubmission(ctx, newSub("s1", map[string]string{"a": "1"})); err != nil {
		t.Fatal(err)
	}
	if _, inserted, err := db.WriteSubmission(ctx, other); err != nil || !inserted {
		t.Fatalf("other project: inserted=%v err=%v", inserted, err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM forms`); c != 2 {
		t.Fatalf("%d forms", c)
	}
}

// attributeAll runs what the jobs runner does for every pending
// submission, with the event as found returns it (nil: as it waits).
func attributeAll(t *testing.T, db *DB, found func(store.Event) store.Event) {
	t.Helper()
	ps, err := db.PendingSubmissions(context.Background(), formsNow.Add(100*24*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		ev := p.Event
		if found != nil {
			ev = found(ev)
		}
		if _, err := db.AttributeSubmission(context.Background(), p, ev); err != nil {
			t.Fatal(err)
		}
	}
}

// A submission waits with the event the request built; an approved
// form's gets written by AttributeSubmission, with the attribution, and
// only once.
func TestSubmissionWaitsForItsAttribution(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, _, err := db.WriteSubmission(ctx, newSub("s0", map[string]string{"name": "Ann"})); err != nil {
		t.Fatal(err)
	}
	approve(t, db, `["name"]`)
	if _, _, err := db.WriteSubmission(ctx, newSub("s1", map[string]string{"name": "Bob"})); err != nil {
		t.Fatal(err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM raw_product`); c != 0 {
		t.Fatalf("%d events written before attribution", c)
	}
	ps, err := db.PendingSubmissions(ctx, formsNow, 10)
	if err != nil || len(ps) != 2 {
		t.Fatalf("pending = %+v, %v", ps, err)
	}
	if ps[0].ID != "s0" || ps[0].WriteEvent || ps[1].ID != "s1" || !ps[1].WriteEvent ||
		ps[1].Event.ActorID != "a1" || ps[1].Event.EventName != store.FormSubmitEvent || !ps[1].ReceivedAt.Equal(formsNow) {
		t.Fatalf("pending = %+v", ps)
	}
	if early, _ := db.PendingSubmissions(ctx, formsNow.Add(-time.Second), 10); len(early) != 0 {
		t.Fatalf("pending before its arrival = %+v", early)
	}
	attributeAll(t, db, func(ev store.Event) store.Event { ev.ReferrerSource, ev.UTMSource = "google", "cpc"; return ev })
	var attr string
	if err := db.db.QueryRow(`SELECT attribution FROM submissions WHERE id='s1'`).Scan(&attr); err != nil ||
		attr != `{"referrer":"google","utm_source":"cpc"}` {
		t.Fatalf("attribution = %q, %v", attr, err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM raw_product WHERE id='s1' AND referrer_source='google'`); c != 1 {
		t.Fatalf("%d attributed events for s1", c)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM raw_product WHERE id='s0'`); c != 0 {
		t.Fatal("a draft's submission wrote an event")
	}
	if left, _ := db.PendingSubmissions(ctx, formsNow.Add(time.Hour), 10); len(left) != 0 {
		t.Fatalf("still pending: %+v", left)
	}
	if wrote, err := db.AttributeSubmission(ctx, ps[1], ps[1].Event); err != nil || wrote {
		t.Fatalf("second AttributeSubmission wrote=%v err=%v", wrote, err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM raw_product WHERE id='s1'`); c != 1 {
		t.Fatalf("%d events for s1 after a second attribution", c)
	}
}

// The connection hash ingest computed waits with the submission; a
// submission without one reads back "".
func TestPendingSubmissionKeepsItsConnection(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	n := newSub("s0", nil)
	n.ConnectionID = "h1"
	if _, _, err := db.WriteSubmission(ctx, n); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.WriteSubmission(ctx, newSub("s1", nil)); err != nil {
		t.Fatal(err)
	}
	ps, err := db.PendingSubmissions(ctx, formsNow, 10)
	if err != nil || len(ps) != 2 || ps[0].ConnectionID != "h1" || ps[1].ConnectionID != "" {
		t.Fatalf("pending = %+v, %v", ps, err)
	}
	attributeAll(t, db, nil)
	if c := countRows(t, db, `SELECT COUNT(*) FROM submissions WHERE pending_connection IS NOT NULL`); c != 0 {
		t.Fatalf("%d attributed submissions keep their connection hash", c)
	}
}

// A row whose pending event does not read comes back with its error, and
// with what else it read, so the job can clear it; it counts toward the
// limit like any other row, and is no error of the call.
func TestPendingSubmissionsReturnsAnUnreadableEventWithItsError(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, id := range []string{"s0", "s1", "s2"} {
		n := newSub(id, nil)
		n.ConnectionID = "h-" + id
		if _, _, err := db.WriteSubmission(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	approve(t, db, `[]`)
	execAll(t, db, `UPDATE submissions SET pending_event='{', pending_write=1 WHERE id='s1'`)
	ps, err := db.PendingSubmissions(ctx, formsNow, 10)
	if err != nil {
		t.Fatalf("err = %v, want none: an unreadable row is not a failed call", err)
	}
	if len(ps) != 3 || ps[0].ID != "s0" || ps[1].ID != "s1" || ps[2].ID != "s2" {
		t.Fatalf("pending = %+v, want s0, s1 and s2", ps)
	}
	bad := ps[1]
	if bad.Err == nil || bad.ProjectID != 1 || !bad.ReceivedAt.Equal(formsNow) || bad.ConnectionID != "h-s1" {
		t.Fatalf("bad = %+v, want its error beside its project, arrival and connection", bad)
	}
	if !reflect.DeepEqual(bad.Event, store.Event{}) || bad.WriteEvent {
		t.Fatalf("bad = %+v, want a zero event that is not to be written", bad)
	}
	if ps[0].Err != nil || ps[2].Err != nil {
		t.Fatalf("readable rows carry an error: %v, %v", ps[0].Err, ps[2].Err)
	}
	if ps, err := db.PendingSubmissions(ctx, formsNow, 2); err != nil || len(ps) != 2 || ps[1].ID != "s1" {
		t.Fatalf("limit 2 = %+v, %v; want s0 and s1", ps, err)
	}
	// The job clears it as attributed with nothing: no attribution, no
	// event, its fields kept, out of the queue.
	if wrote, err := db.AttributeSubmission(ctx, bad, store.Event{}); err != nil || wrote {
		t.Fatalf("clearing wrote=%v err=%v", wrote, err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM submissions WHERE id='s1' AND pending_event IS NULL
		AND attribution IS NULL AND fields='{}'`); c != 1 {
		t.Fatal("the cleared submission is not kept unattributed")
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM raw_product WHERE id='s1'`); c != 0 {
		t.Fatal("clearing an unreadable submission wrote an event")
	}
	if ps, _ := db.PendingSubmissions(ctx, formsNow, 10); len(ps) != 2 {
		t.Fatalf("pending after clearing = %+v, want s0 and s2", ps)
	}
}

// Erasure before the job: the submission is gone, and its event is never
// written.
func TestAttributeSubmissionAfterDeleteWritesNothing(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, _, err := db.WriteSubmission(ctx, newSub("s0", nil)); err != nil {
		t.Fatal(err)
	}
	approve(t, db, `[]`)
	if _, _, err := db.WriteSubmission(ctx, newSub("s1", nil)); err != nil {
		t.Fatal(err)
	}
	ps, _ := db.PendingSubmissions(ctx, formsNow, 10)
	execAll(t, db, `DELETE FROM submissions WHERE id='s1'`)
	wrote, err := db.AttributeSubmission(ctx, ps[1], ps[1].Event)
	if err != nil || wrote {
		t.Fatalf("wrote=%v err=%v", wrote, err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM raw_product`); c != 0 {
		t.Fatalf("%d events for a deleted submission", c)
	}
}

// An approved form's attributed event also waits in the outbox
// (unlogged_event) until the job has written it to the event files;
// MarkLogged takes it out. A draft's submission never reaches it.
func TestAttributedEventWaitsUnloggedUntilMarked(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, _, err := db.WriteSubmission(ctx, newSub("s0", nil)); err != nil {
		t.Fatal(err)
	}
	approve(t, db, `[]`)
	for _, id := range []string{"s1", "s2"} {
		if _, _, err := db.WriteSubmission(ctx, newSub(id, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if us, err := db.UnloggedEvents(ctx, 10); err != nil || len(us) != 0 {
		t.Fatalf("unlogged before attribution = %+v, %v", us, err)
	}
	if n, err := db.UnloggedCount(ctx); err != nil || n != 0 {
		t.Fatalf("unlogged count before attribution = %d, %v", n, err)
	}
	attributeAll(t, db, func(ev store.Event) store.Event { ev.ReferrerSource = "google"; return ev })
	us, err := db.UnloggedEvents(ctx, 10)
	if err != nil || len(us) != 2 || us[0].ID != "s1" || us[1].ID != "s2" {
		t.Fatalf("unlogged = %+v, %v; want s1 and s2, not the draft's s0", us, err)
	}
	if u := us[0]; u.Err != nil || u.ProjectID != 1 || u.Event.ID != "s1" || u.Event.ProjectID != 1 ||
		u.Event.ReferrerSource != "google" || u.Event.EventName != store.FormSubmitEvent || !u.Event.TS.Equal(formsNow) {
		t.Fatalf("unlogged s1 = %+v, want the event as written", u)
	}
	if one, _ := db.UnloggedEvents(ctx, 1); len(one) != 1 || one[0].ID != "s1" {
		t.Fatalf("limit 1 = %+v, want s1", one)
	}
	if n, err := db.UnloggedCount(ctx); err != nil || n != 2 {
		t.Fatalf("unlogged count = %d, %v; want 2", n, err)
	}
	if err := db.MarkLogged(ctx, us[:1]); err != nil {
		t.Fatal(err)
	}
	if n, err := db.UnloggedCount(ctx); err != nil || n != 1 {
		t.Fatalf("unlogged count after marking s1 = %d, %v; want 1", n, err)
	}
	if left, err := db.UnloggedEvents(ctx, 10); err != nil || len(left) != 1 || left[0].ID != "s2" {
		t.Fatalf("unlogged after marking s1 = %+v, %v; want s2", left, err)
	}
	if err := db.MarkLogged(ctx, nil); err != nil {
		t.Fatalf("marking nothing: %v", err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM raw_product WHERE id IN ('s1','s2')`); c != 2 {
		t.Fatalf("%d events in SQLite, want 2: marking touches only the outbox", c)
	}
}

// Deleting a submission takes its unlogged event with it, as it takes
// the event SQLite holds.
func TestDeletedSubmissionLeavesTheOutbox(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, _, err := db.WriteSubmission(ctx, newSub("s0", nil)); err != nil {
		t.Fatal(err)
	}
	approve(t, db, `[]`)
	if _, _, err := db.WriteSubmission(ctx, newSub("s1", nil)); err != nil {
		t.Fatal(err)
	}
	attributeAll(t, db, nil)
	if us, _ := db.UnloggedEvents(ctx, 10); len(us) != 1 {
		t.Fatalf("unlogged = %+v, want s1", us)
	}
	if _, err := db.DeleteSubmissions(ctx, 1, []string{"s1"}, store.AuditEntry{Actor: "t", Action: "submission.delete", Subject: "form/1/contact"}); err != nil {
		t.Fatal(err)
	}
	if us, err := db.UnloggedEvents(ctx, 10); err != nil || len(us) != 0 {
		t.Fatalf("unlogged after delete = %+v, %v", us, err)
	}
}

// An outbox row that does not read comes back with its error and a zero
// event, so the job can mark it rather than meet it every minute.
func TestUnloggedEventsReturnsAnUnreadableRowWithItsError(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, _, err := db.WriteSubmission(ctx, newSub("s0", nil)); err != nil {
		t.Fatal(err)
	}
	approve(t, db, `[]`)
	for _, id := range []string{"s1", "s2"} {
		if _, _, err := db.WriteSubmission(ctx, newSub(id, nil)); err != nil {
			t.Fatal(err)
		}
	}
	attributeAll(t, db, nil)
	execAll(t, db, `UPDATE submissions SET unlogged_event='{' WHERE id='s1'`)
	us, err := db.UnloggedEvents(ctx, 10)
	if err != nil || len(us) != 2 {
		t.Fatalf("unlogged = %+v, %v; want s1 and s2", us, err)
	}
	if u := us[0]; u.ID != "s1" || u.ProjectID != 1 || u.Err == nil || !reflect.DeepEqual(u.Event, store.Event{}) {
		t.Fatalf("unreadable = %+v, want its key and error beside a zero event", u)
	}
	if us[1].Err != nil || us[1].Event.ID != "s2" {
		t.Fatalf("readable = %+v", us[1])
	}
	if err := db.MarkLogged(ctx, us); err != nil {
		t.Fatal(err)
	}
	if left, _ := db.UnloggedEvents(ctx, 10); len(left) != 0 {
		t.Fatalf("unlogged after marking = %+v", left)
	}
}

// A row as migration 041 finds it (pending_event NULL) is attributed.
func TestSubmissionsFromBeforeTheMigrationAreNotPending(t *testing.T) {
	db := newTestDB(t)
	if _, _, err := db.WriteSubmission(context.Background(), newSub("old", nil)); err != nil {
		t.Fatal(err)
	}
	execAll(t, db, `UPDATE submissions SET pending_event=NULL, pending_write=NULL`)
	if ps, err := db.PendingSubmissions(context.Background(), formsNow.Add(time.Hour), 10); err != nil || len(ps) != 0 {
		t.Fatalf("pending = %+v, %v", ps, err)
	}
}

func TestWriteSubmissionNilFieldsStoreAnObject(t *testing.T) {
	db := newTestDB(t)
	if _, _, err := db.WriteSubmission(context.Background(), newSub("s1", nil)); err != nil {
		t.Fatal(err)
	}
	var blob string
	if err := db.db.QueryRow(`SELECT fields FROM submissions WHERE id='s1'`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if blob != "{}" {
		t.Fatalf("fields = %q, want {}", blob)
	}
	if names := formFieldNames(t, db); len(names) != 0 {
		t.Fatalf("form fields = %v", names)
	}
}

// TestWriteSubmissionEventShapeIsTheStores: whatever family, name,
// attributes, id, project and ts the caller put on the event, the one
// written is the submission's $form_submit.
func TestWriteSubmissionEventShapeIsTheStores(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, _, err := db.WriteSubmission(ctx, newSub("s0", map[string]string{"name": "Ann"})); err != nil {
		t.Fatal(err)
	}
	approve(t, db, `["name"]`)
	n := newSub("s1", map[string]string{"name": "Bob"})
	n.Event.Family = store.FamilyViews
	n.Event.EventName = "other"
	n.Event.Attributes = map[string]string{"form": "wrong", "extra": "x"}
	n.Event.ID, n.Event.ProjectID, n.Event.TS = "zzz", 9, formsNow.Add(48*time.Hour)
	if _, _, err := db.WriteSubmission(ctx, n); err != nil {
		t.Fatal(err)
	}
	attributeAll(t, db, nil)
	var id, name, ts, attrs string
	var project int64
	if err := db.db.QueryRow(`SELECT id, project_id, event_name, ts, attributes FROM raw_product`).
		Scan(&id, &project, &name, &ts, &attrs); err != nil {
		t.Fatal(err)
	}
	if id != "s1" || project != 1 || name != "$form_submit" || ts != "2026-10-06T12:00:00Z" || attrs != `{"form":"contact"}` {
		t.Fatalf("event = %q %d %q %q %q", id, project, name, ts, attrs)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM raw_views`); c != 0 {
		t.Fatalf("%d view rows", c)
	}
}

// TestWriteSubmissionRetryOfStoredIDSucceedsAfterClose: the retry of an
// id already stored is a success, not a refusal, once the form has closed.
func TestWriteSubmissionRetryOfStoredIDSucceedsAfterClose(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	n := newSub("s1", map[string]string{"a": "1"})
	if _, _, err := db.WriteSubmission(ctx, n); err != nil {
		t.Fatal(err)
	}
	for _, closer := range []string{
		`UPDATE forms SET archived_at='2026-10-06T11:00:00Z'`,
		`UPDATE forms SET archived_at=NULL, closes_at='2026-10-06T11:00:00Z'`,
		`UPDATE forms SET closes_at=NULL, draft_until='2026-10-06T11:00:00Z'`,
	} {
		execAll(t, db, closer)
		form, inserted, err := db.WriteSubmission(ctx, n)
		if err != nil || inserted || form.Name != "contact" {
			t.Fatalf("%s: form=%+v inserted=%v err=%v", closer, form, inserted, err)
		}
		// A new id is still refused.
		if _, _, err := db.WriteSubmission(ctx, newSub("fresh", nil)); !errors.Is(err, store.ErrFormClosed) {
			t.Fatalf("%s: new id err = %v", closer, err)
		}
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM submissions`); c != 1 {
		t.Fatalf("%d submissions", c)
	}
}

var formAudit = store.AuditEntry{Actor: "test", Action: "form.test", Subject: "form/1/contact"}

func mustWrite(t *testing.T, db *DB, n store.NewSubmission) {
	t.Helper()
	if _, _, err := db.WriteSubmission(context.Background(), n); err != nil {
		t.Fatal(err)
	}
}

func TestApproveForm(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	mustWrite(t, db, newSub("s1", map[string]string{"name": "Ann", "email": "a@x.io"}))

	later := formsNow.Add(time.Hour)
	if err := db.ApproveForm(ctx, 1, "contact", []string{"email"}, later,
		store.AuditEntry{Actor: "agent", Action: "form.approve", Subject: "form/1/contact"}); err != nil {
		t.Fatal(err)
	}
	f, err := db.GetForm(ctx, 1, "contact")
	if err != nil {
		t.Fatal(err)
	}
	if f.Status != store.FormApproved || f.DraftUntil != nil ||
		f.ApprovedAt == nil || !f.ApprovedAt.Equal(later) ||
		!reflect.DeepEqual(f.ExpectedFields, []string{"email"}) {
		t.Fatalf("form = %+v", f)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='form.approve' AND actor='agent'`); n != 1 {
		t.Fatalf("%d approve audit rows", n)
	}
	if err := db.ApproveForm(ctx, 1, "contact", []string{"email"}, later, formAudit); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second approve = %v, want ErrConflict", err)
	}
	if err := db.ApproveForm(ctx, 1, "nope", []string{"email"}, later, formAudit); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown approve = %v, want ErrNotFound", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='form.test'`); n != 0 {
		t.Fatalf("a refused approve wrote %d audit rows", n)
	}
}

func TestGetFormUnknown(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.GetForm(context.Background(), 1, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestListFormsOrderCountsAndArchivedSplit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, name := range []string{"zeta", "alpha", "mid", "gone"} {
		n := newSub("s-"+name, map[string]string{"k": "v"})
		n.Submission.Form = name
		mustWrite(t, db, n)
	}
	n := newSub("s-alpha-2", map[string]string{"k": "v"})
	n.Submission.Form = "alpha"
	mustWrite(t, db, n)
	// Another project's form never shows.
	other := newSub("o1", map[string]string{"k": "v"})
	other.Submission.ProjectID, other.Event.ProjectID = 2, 2
	mustWrite(t, db, other)

	for _, name := range []string{"zeta", "mid"} {
		if err := db.ApproveForm(ctx, 1, name, []string{"k"}, formsNow, formAudit); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetFormArchived(ctx, 1, "gone", true, time.Time{}, formAudit); err != nil {
		t.Fatal(err)
	}

	active, err := db.ListForms(ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	counts := map[string]int{}
	for _, f := range active {
		got = append(got, f.Name)
		counts[f.Name] = f.Submissions
	}
	// Drafts first (alpha), then approved by name (mid, zeta).
	if !reflect.DeepEqual(got, []string{"alpha", "mid", "zeta"}) {
		t.Fatalf("active order = %v", got)
	}
	if counts["alpha"] != 2 || counts["mid"] != 1 {
		t.Fatalf("counts = %v", counts)
	}
	archived, err := db.ListForms(ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 1 || archived[0].Name != "gone" || archived[0].ArchivedAt == nil {
		t.Fatalf("archived = %+v", archived)
	}
	none, err := db.ListForms(ctx, 3, false)
	if err != nil || len(none) != 0 {
		t.Fatalf("empty project: %v %v", none, err)
	}
}

func TestUpdateFormWritesAsGiven(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	mustWrite(t, db, newSub("s1", map[string]string{"email": "a@x.io"}))
	if err := db.ApproveForm(ctx, 1, "contact", []string{"email"}, formsNow, formAudit); err != nil {
		t.Fatal(err)
	}
	f, err := db.GetForm(ctx, 1, "contact")
	if err != nil {
		t.Fatal(err)
	}
	closes := formsNow.Add(48 * time.Hour)
	f.Purpose, f.ReturnURL, f.ClosesAt, f.ExpectedFields = "leads", "https://example.com/thanks", &closes, []string{"email", "name"}
	if err := db.UpdateForm(ctx, f, store.AuditEntry{Actor: "agent", Action: "form.update", Subject: "form/1/contact"}); err != nil {
		t.Fatal(err)
	}
	g, err := db.GetForm(ctx, 1, "contact")
	if err != nil {
		t.Fatal(err)
	}
	if g.Purpose != "leads" || g.ReturnURL != "https://example.com/thanks" ||
		g.ClosesAt == nil || !g.ClosesAt.Equal(closes) || !reflect.DeepEqual(g.ExpectedFields, []string{"email", "name"}) {
		t.Fatalf("form = %+v", g)
	}
	if g.Status != store.FormApproved || g.ApprovedAt == nil {
		t.Fatalf("update changed the state: %+v", g)
	}
	// nil clears closes_at.
	g.ClosesAt = nil
	if err := db.UpdateForm(ctx, g, formAudit); err != nil {
		t.Fatal(err)
	}
	if h, _ := db.GetForm(ctx, 1, "contact"); h.ClosesAt != nil {
		t.Fatalf("closes_at = %v", h.ClosesAt)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='form.update'`); n != 1 {
		t.Fatalf("%d update audit rows", n)
	}
	g.Name = "nope"
	if err := db.UpdateForm(ctx, g, formAudit); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown update = %v", err)
	}
}

func TestUpdateFormNilExpectedKeepsList(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	mustWrite(t, db, newSub("s1", map[string]string{"email": "a@x.io"}))
	if err := db.ApproveForm(ctx, 1, "contact", []string{"email"}, formsNow, formAudit); err != nil {
		t.Fatal(err)
	}
	// A caller holding a stale draft read: no expected list.
	stale := store.Form{ProjectID: 1, Name: "contact", Purpose: "leads"}
	if err := db.UpdateForm(ctx, stale, formAudit); err != nil {
		t.Fatal(err)
	}
	g, err := db.GetForm(ctx, 1, "contact")
	if err != nil {
		t.Fatal(err)
	}
	if g.Purpose != "leads" || !reflect.DeepEqual(g.ExpectedFields, []string{"email"}) {
		t.Fatalf("form = %+v, want purpose set and expected_fields kept", g)
	}
}

func TestSetFormArchived(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	mustWrite(t, db, newSub("s1", map[string]string{"email": "a@x.io"}))
	fresh := formsNow.Add(30 * 24 * time.Hour)

	// Restoring an active form and archiving twice are no-ops.
	if err := db.SetFormArchived(ctx, 1, "contact", false, fresh, formAudit); err != nil {
		t.Fatal(err)
	}
	if f, _ := db.GetForm(ctx, 1, "contact"); f.ArchivedAt != nil || !f.DraftUntil.Equal(formsNow.Add(7*24*time.Hour)) {
		t.Fatalf("restore of an active draft changed it: %+v", f)
	}
	if err := db.SetFormArchived(ctx, 1, "contact", true, time.Time{}, formAudit); err != nil {
		t.Fatal(err)
	}
	f, _ := db.GetForm(ctx, 1, "contact")
	if f.ArchivedAt == nil {
		t.Fatal("not archived")
	}
	first := *f.ArchivedAt
	if err := db.SetFormArchived(ctx, 1, "contact", true, time.Time{}, formAudit); err != nil {
		t.Fatal(err)
	}
	if f, _ := db.GetForm(ctx, 1, "contact"); !f.ArchivedAt.Equal(first) {
		t.Fatalf("second archive moved archived_at: %v -> %v", first, f.ArchivedAt)
	}
	// Restore of a draft: draft_until is the passed value.
	if err := db.SetFormArchived(ctx, 1, "contact", false, fresh, formAudit); err != nil {
		t.Fatal(err)
	}
	f, _ = db.GetForm(ctx, 1, "contact")
	if f.ArchivedAt != nil || f.DraftUntil == nil || !f.DraftUntil.Equal(fresh) {
		t.Fatalf("restored draft = %+v", f)
	}
	if err := db.SetFormArchived(ctx, 1, "nope", true, time.Time{}, formAudit); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown = %v", err)
	}

	// An approved form keeps no draft_until whatever is passed.
	if err := db.ApproveForm(ctx, 1, "contact", []string{"email"}, formsNow, formAudit); err != nil {
		t.Fatal(err)
	}
	if err := db.SetFormArchived(ctx, 1, "contact", true, time.Time{}, formAudit); err != nil {
		t.Fatal(err)
	}
	if err := db.SetFormArchived(ctx, 1, "contact", false, fresh, formAudit); err != nil {
		t.Fatal(err)
	}
	f, _ = db.GetForm(ctx, 1, "contact")
	if f.ArchivedAt != nil || f.DraftUntil != nil || f.Status != store.FormApproved {
		t.Fatalf("restored approved form = %+v", f)
	}
}

func TestExpireDrafts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	mk := func(name string, created time.Time, draftDays int) {
		n := newSub("s-"+name, map[string]string{"k": "v"})
		n.Submission.Form = name
		n.Submission.ReceivedAt = created
		n.DraftUntil = created.Add(time.Duration(draftDays) * 24 * time.Hour)
		n.Event.TS, n.Event.ReceivedAt = created, created
		mustWrite(t, db, n)
	}
	old := formsNow.Add(-10 * 24 * time.Hour)
	mk("stale", old, 7)
	mk("fresh", formsNow.Add(-24*time.Hour), 7)
	mk("approved-old", old, 7)
	mk("already", old, 7)
	if err := db.ApproveForm(ctx, 1, "approved-old", []string{"k"}, old, formAudit); err != nil {
		t.Fatal(err)
	}
	if err := db.SetFormArchived(ctx, 1, "already", true, time.Time{}, formAudit); err != nil {
		t.Fatal(err)
	}

	n, err := db.ExpireDrafts(ctx, formsNow)
	if err != nil || n != 1 {
		t.Fatalf("expired %d, err %v", n, err)
	}
	for name, want := range map[string]bool{"stale": true, "fresh": false, "approved-old": false, "already": true} {
		f, err := db.GetForm(ctx, 1, name)
		if err != nil {
			t.Fatal(err)
		}
		if (f.ArchivedAt != nil) != want {
			t.Errorf("%s archived = %v, want %v", name, f.ArchivedAt, want)
		}
	}
	if f, _ := db.GetForm(ctx, 1, "stale"); !f.ArchivedAt.Equal(formsNow) {
		t.Errorf("archived_at = %v, want %v", f.ArchivedAt, formsNow)
	}
	rows := auditRows(t, db, "form.expire")
	if len(rows) != 1 || rows[0].Actor != "retention" || rows[0].Subject != "form/1/stale" {
		t.Fatalf("audit = %+v", rows)
	}
	if n, err := db.ExpireDrafts(ctx, formsNow); err != nil || n != 0 {
		t.Fatalf("second pass expired %d, err %v", n, err)
	}
	// draft_until == now is past (Open uses After).
	mk("edge", formsNow.Add(-7*24*time.Hour), 7)
	if n, err := db.ExpireDrafts(ctx, formsNow); err != nil || n != 1 {
		t.Fatalf("edge: expired %d, err %v", n, err)
	}
}

func rawProductIDs(t *testing.T, db *DB) map[string]bool {
	t.Helper()
	rows, err := db.db.Query(`SELECT id FROM raw_product`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[id] = true
	}
	return ids
}

func TestDeleteSubmissionsRemovesRowAndRawEvent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	mustWrite(t, db, newSub("s0", map[string]string{"email": "draft@x.io"})) // draft: no event
	if err := db.ApproveForm(ctx, 1, "contact", []string{"email"}, formsNow, formAudit); err != nil {
		t.Fatal(err)
	}
	// Different days, so the event key's day part matters.
	for i, id := range []string{"s1", "s2", "s3"} {
		n := newSub(id, map[string]string{"email": id + "@x.io"})
		at := formsNow.Add(time.Duration(i) * 24 * time.Hour)
		n.Submission.ReceivedAt, n.Event.TS, n.Event.ReceivedAt = at, at, at
		mustWrite(t, db, n)
	}
	// An unrelated product event with the same id on another project, and
	// one on this project.
	other := newSub("x0", map[string]string{"email": "o@x.io"})
	other.Submission.ProjectID, other.Event.ProjectID = 2, 2
	mustWrite(t, db, other)
	if err := db.ApproveForm(ctx, 2, "contact", []string{"email"}, formsNow, formAudit); err != nil {
		t.Fatal(err)
	}
	other = newSub("s1", map[string]string{"email": "o@x.io"})
	other.Submission.ProjectID, other.Event.ProjectID = 2, 2
	mustWrite(t, db, other)
	attributeAll(t, db, nil)
	if c := countRows(t, db, `SELECT COUNT(*) FROM events`); c != 4 {
		t.Fatalf("setup: %d events, want 4", c)
	}
	if ids := rawProductIDs(t, db); len(ids) != 3 {
		t.Fatalf("setup: raw product ids = %v", ids)
	}

	n, err := db.DeleteSubmissions(ctx, 1, []string{"s1", "s3", "s0", "missing"},
		store.AuditEntry{Actor: "agent", Action: "submission.delete", Subject: "project/1", Detail: "ids"})
	if err != nil || n != 3 {
		t.Fatalf("deleted %d, err %v", n, err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM submissions WHERE project_id=1`); c != 1 {
		t.Fatalf("%d submissions left on project 1", c)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM submissions WHERE project_id=1 AND id='s2'`); c != 1 {
		t.Fatal("s2 was deleted")
	}
	// s2 (project 1) and s1 (project 2) keep their events.
	if c := countRows(t, db, `SELECT COUNT(*) FROM events`); c != 2 {
		t.Fatalf("%d events left, want 2", c)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM events WHERE project_id=1 AND id='s2'`); c != 1 {
		t.Fatal("s2's event was deleted")
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM events WHERE project_id=2 AND id='s1'`); c != 1 {
		t.Fatal("another project's event was deleted")
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM submissions WHERE project_id=2`); c != 2 {
		t.Fatal("another project's submission was deleted")
	}
	// The audit row counts what was deleted, not what was asked for.
	details := func() []string {
		t.Helper()
		rows, err := db.db.Query(`SELECT detail FROM audit_log WHERE action='submission.delete' ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				t.Fatal(err)
			}
			out = append(out, d)
		}
		return out
	}
	if got := details(); !reflect.DeepEqual(got, []string{"ids (3 deleted)"}) {
		t.Fatalf("audit details = %q", got)
	}
	// Deleting nothing is still audited, with 0.
	if n, err := db.DeleteSubmissions(ctx, 1, []string{"s1"}, store.AuditEntry{Actor: "agent", Action: "submission.delete",
		Subject: "project/1", Detail: "search"}); err != nil || n != 0 {
		t.Fatalf("already deleted: %d, %v", n, err)
	}
	if n, err := db.DeleteSubmissions(ctx, 1, nil, store.AuditEntry{Actor: "agent", Action: "submission.delete",
		Subject: "project/1", Detail: "filters: contact"}); err != nil || n != 0 {
		t.Fatalf("empty ids: %d, %v", n, err)
	}
	if got := details(); !reflect.DeepEqual(got, []string{"ids (3 deleted)", "search (0 deleted)", "filters: contact (0 deleted)"}) {
		t.Fatalf("audit details = %q", got)
	}
}

func TestFindSubmissions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	add := func(form, id string, at time.Time, fields map[string]string) {
		n := newSub(id, fields)
		n.Submission.Form, n.Submission.ReceivedAt, n.Event.ReceivedAt, n.Event.TS = form, at, at, at
		mustWrite(t, db, n)
	}
	add("contact", "a", formsNow.Add(-3*time.Hour), map[string]string{"name": "Ann", "email": "Alice@Example.com"})
	add("contact", "b", formsNow.Add(-2*time.Hour), map[string]string{"note": "ask ALICE again"})
	add("contact", "c", formsNow.Add(-1*time.Hour), map[string]string{"note": "100% sure", "x": "no match"})
	add("contact", "d", formsNow, map[string]string{"note": "1000 sure"})
	add("hidden", "e", formsNow, map[string]string{"email": "alice@example.com"})
	add("contact", "f", formsNow.Add(-4*time.Hour), map[string]string{"under": "a_b", "plain": "axb"})
	if err := db.SetFormArchived(ctx, 1, "hidden", true, time.Time{}, formAudit); err != nil {
		t.Fatal(err)
	}
	other := newSub("o", map[string]string{"email": "alice@other.com"})
	other.Submission.ProjectID, other.Event.ProjectID = 2, 2
	mustWrite(t, db, other)

	ids := func(subs []store.Submission) []string {
		var out []string
		for _, s := range subs {
			out = append(out, s.ID)
		}
		return out
	}
	subs, next, err := db.FindSubmissions(ctx, 1, "alice", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	// An archived form's submissions are found too, and marked: an
	// erasure request reaches them (D8, revised).
	if !reflect.DeepEqual(ids(subs), []string{"e", "b", "a"}) || next != "" {
		t.Fatalf("alice = %v next %q", ids(subs), next)
	}
	if subs[2].Form != "contact" || subs[2].Fields["email"] != "Alice@Example.com" || subs[2].ProjectID != 1 ||
		!subs[2].ReceivedAt.Equal(formsNow.Add(-3*time.Hour)) || subs[2].Archived {
		t.Fatalf("submission = %+v", subs[2])
	}
	if subs[0].Form != "hidden" || !subs[0].Archived || subs[1].Archived {
		t.Fatalf("archived marks = %+v", subs)
	}
	// LIKE wildcards in the search are literal.
	if subs, _, _ = db.FindSubmissions(ctx, 1, "100%", 10, ""); !reflect.DeepEqual(ids(subs), []string{"c"}) {
		t.Fatalf("100%% = %v", ids(subs))
	}
	if subs, _, _ = db.FindSubmissions(ctx, 1, "a_b", 10, ""); !reflect.DeepEqual(ids(subs), []string{"f"}) {
		t.Fatalf("a_b = %v", ids(subs))
	}
	if subs, _, _ = db.FindSubmissions(ctx, 1, `\`, 10, ""); len(subs) != 0 {
		t.Fatalf(`\ = %v`, ids(subs))
	}
	// Keys are not searched, only values.
	if subs, _, _ = db.FindSubmissions(ctx, 1, "note", 10, ""); len(subs) != 0 {
		t.Fatalf("a key matched: %v", ids(subs))
	}
	// Paging: newest first, cursor = last id of a page that has more.
	subs, next, err = db.FindSubmissions(ctx, 1, "sure", 1, "")
	if err != nil || !reflect.DeepEqual(ids(subs), []string{"d"}) || next != "d" {
		t.Fatalf("page 1 = %v next %q err %v", ids(subs), next, err)
	}
	subs, next, err = db.FindSubmissions(ctx, 1, "sure", 1, next)
	if err != nil || !reflect.DeepEqual(ids(subs), []string{"c"}) || next != "" {
		t.Fatalf("page 2 = %v next %q err %v", ids(subs), next, err)
	}
	if _, _, err = db.FindSubmissions(ctx, 1, "sure", 1, "gone"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown cursor = %v", err)
	}
	if _, _, err = db.FindSubmissions(ctx, 1, "", 10, ""); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty search = %v", err)
	}

	all, err := db.SubmissionIDsMatching(ctx, 1, "alice")
	if err != nil || !reflect.DeepEqual(all, []string{"e", "b", "a"}) {
		t.Fatalf("SubmissionIDsMatching = %v, %v", all, err)
	}
	if _, err := db.SubmissionIDsMatching(ctx, 1, ""); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty search = %v", err)
	}
}

func TestGetSubmission(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	n := newSub("s1", map[string]string{"email": "a@x.io", "note": "hi"})
	n.Submission.Attribution = store.Attribution{Referrer: "news.example", UTMSource: "nl"}
	mustWrite(t, db, n)

	s, err := db.GetSubmission(ctx, 1, "contact", "s1")
	if err != nil {
		t.Fatal(err)
	}
	want := store.Submission{ProjectID: 1, ID: "s1", Form: "contact", ReceivedAt: formsNow,
		Fields:      map[string]string{"email": "a@x.io", "note": "hi"},
		Attribution: store.Attribution{Referrer: "news.example", UTMSource: "nl"}}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("submission = %+v\nwant %+v", s, want)
	}
	// Another form's name, another project, an unknown id: not found.
	for _, c := range []struct {
		project  int64
		form, id string
	}{{1, "other", "s1"}, {2, "contact", "s1"}, {1, "contact", "nope"}} {
		if _, err := db.GetSubmission(ctx, c.project, c.form, c.id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%+v: err = %v, want ErrNotFound", c, err)
		}
	}
	// An archived form hides its submissions.
	if err := db.SetFormArchived(ctx, 1, "contact", true, time.Time{}, formAudit); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetSubmission(ctx, 1, "contact", "s1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("archived: err = %v, want ErrNotFound", err)
	}
}

// submitAt writes a submission to "contact" on project 1 received at at.
func submitAt(t *testing.T, db *DB, id string, at time.Time) {
	t.Helper()
	n := newSub(id, map[string]string{"k": "v"})
	n.Submission.ReceivedAt, n.Event.TS, n.Event.ReceivedAt = at, at, at
	n.DraftUntil = at.Add(7 * 24 * time.Hour)
	mustWrite(t, db, n)
}

func TestListFormsNewSubmissions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	submitAt(t, db, "s1", t0)
	submitAt(t, db, "s2", t0.Add(time.Minute))
	fs, err := db.ListForms(ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if fs[0].SeenAt != nil || fs[0].NewSubmissions != 2 {
		t.Fatalf("unseen form: seen %v new %d, want nil 2", fs[0].SeenAt, fs[0].NewSubmissions)
	}
	if err := db.MarkFormSeen(ctx, 1, "contact", t0); err != nil {
		t.Fatal(err)
	}
	fs, _ = db.ListForms(ctx, 1, false)
	if fs[0].NewSubmissions != 1 || fs[0].SeenAt == nil || !fs[0].SeenAt.Equal(t0) {
		t.Fatalf("after seen at t0: seen %v new %d, want t0 1", fs[0].SeenAt, fs[0].NewSubmissions)
	}
}

func TestMarkFormSeenNeverMovesBack(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	submitAt(t, db, "s1", t0)
	if err := db.MarkFormSeen(ctx, 1, "contact", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkFormSeen(ctx, 1, "contact", t0); err != nil {
		t.Fatal(err)
	}
	f, err := db.GetForm(ctx, 1, "contact")
	if err != nil {
		t.Fatal(err)
	}
	if f.SeenAt == nil || !f.SeenAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("seen_at = %v, want t0+1h", f.SeenAt)
	}
}

func TestMarkFormSeenOnClosedDB(t *testing.T) {
	db := newTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkFormSeen(context.Background(), 1, "contact", time.Now()); err == nil {
		t.Fatal("MarkFormSeen on a closed database: want an error")
	}
}

func TestMarkFormSeenUnknownForm(t *testing.T) {
	db := newTestDB(t)
	err := db.MarkFormSeen(context.Background(), 1, "nope", time.Now())
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
