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
			Fields: fields, ActorKind: "user", ActorID: "a1",
			Host: "example.com", Path: "/contact", Via: "form",
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
	var recv, actorKind, actorID, host, path, via string
	var visit *string
	if err := db.db.QueryRow(`SELECT received_at, actor_kind, actor_id, host, path, via, visit FROM submissions
		WHERE project_id=1 AND id='s1'`).Scan(&recv, &actorKind, &actorID, &host, &path, &via, &visit); err != nil {
		t.Fatal(err)
	}
	if recv != "2026-10-06T12:00:00Z" || actorKind != "user" || actorID != "a1" ||
		host != "example.com" || path != "/contact" || via != "form" || visit != nil {
		t.Fatalf("row = %q %q %q %q %q %q %v", recv, actorKind, actorID, host, path, via, visit)
	}
}

func TestWriteSubmissionStoresVisit(t *testing.T) {
	db := newTestDB(t)
	n := newSub("s1", map[string]string{"a": "1"})
	n.Submission.Visit = &store.Visit{LandingPath: "/pricing", Referrer: "google", UTMSource: "x", Views: 3}
	if _, _, err := db.WriteSubmission(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	var blob string
	if err := db.db.QueryRow(`SELECT visit FROM submissions WHERE id='s1'`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	var v store.Visit
	if err := json.Unmarshal([]byte(blob), &v); err != nil || v != *n.Submission.Visit {
		t.Fatalf("visit = %+v, %v", v, err)
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
	if c := countRows(t, db, `SELECT COUNT(*) FROM received_attributes WHERE project_id=1 AND attr_key='form'`); c != 1 {
		t.Fatalf("received_attributes rows for form = %d", c)
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

func addView(t *testing.T, db *DB, id string, project int64, actor string, at time.Time, path, ref, src, med, camp string) {
	t.Helper()
	err := db.WriteEvents(context.Background(), []store.Event{{
		ID: id, ProjectID: project, Family: store.FamilyViews, TS: at, ReceivedAt: at,
		ActorKind: "user", ActorID: actor, Path: path, ReferrerSource: ref,
		UTMSource: src, UTMMedium: med, UTMCampaign: camp,
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionVisit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	at := formsNow
	// t-60m is a session of its own (40 minutes before the next view);
	// t-20m and t-5m are one session.
	addView(t, db, "v1", 1, "a1", at.Add(-60*time.Minute), "/old", "", "", "", "")
	addView(t, db, "v2", 1, "a1", at.Add(-20*time.Minute), "/landing", "google", "news", "email", "launch")
	addView(t, db, "v3", 1, "a1", at.Add(-5*time.Minute), "/contact", "", "", "", "")
	addView(t, db, "v4", 1, "other", at.Add(-10*time.Minute), "/elsewhere", "", "", "", "")
	addView(t, db, "v5", 2, "a1", at.Add(-10*time.Minute), "/project2", "", "", "", "")

	got, err := db.SessionVisit(ctx, 1, "user", "a1", at)
	if err != nil {
		t.Fatal(err)
	}
	want := &store.Visit{LandingPath: "/landing", Referrer: "google", UTMSource: "news",
		UTMMedium: "email", UTMCampaign: "launch", Views: 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visit = %+v, want %+v", got, want)
	}

	// A view after `at` is not part of the session so far.
	addView(t, db, "v6", 1, "a1", at.Add(time.Minute), "/later", "", "", "", "")
	if got, _ = db.SessionVisit(ctx, 1, "user", "a1", at); got == nil || got.Views != 2 {
		t.Fatalf("visit = %+v", got)
	}

	got, err = db.SessionVisit(ctx, 1, "user", "nobody", at)
	if err != nil || got != nil {
		t.Fatalf("no views: %+v, %v", got, err)
	}
}

func TestSessionVisitReachesBackAcrossMidnight(t *testing.T) {
	db := newTestDB(t)
	at := time.Date(2026, 10, 6, 0, 10, 0, 0, time.UTC)
	addView(t, db, "v1", 1, "a1", at.Add(-20*time.Minute), "/late", "", "", "", "")
	addView(t, db, "v2", 1, "a1", at.Add(-5*time.Minute), "/later", "", "", "", "")
	got, err := db.SessionVisit(context.Background(), 1, "user", "a1", at)
	if err != nil || got == nil || got.LandingPath != "/late" || got.Views != 2 {
		t.Fatalf("visit = %+v, %v", got, err)
	}
}

// TestSessionVisitGapIsInclusive: a view exactly 30 minutes before the next
// is the same session; one second more starts another.
func TestSessionVisitGapIsInclusive(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	addView(t, db, "v1", 1, "a1", formsNow.Add(-40*time.Minute-time.Second), "/first", "", "", "", "")
	addView(t, db, "v2", 1, "a1", formsNow.Add(-10*time.Minute), "/second", "", "", "", "")
	got, err := db.SessionVisit(ctx, 1, "user", "a1", formsNow)
	if err != nil || got == nil || got.LandingPath != "/second" || got.Views != 1 {
		t.Fatalf("31m gap: %+v, %v", got, err)
	}
	addView(t, db, "v0", 1, "a2", formsNow.Add(-40*time.Minute), "/edge", "", "", "", "")
	addView(t, db, "v3", 1, "a2", formsNow.Add(-10*time.Minute), "/second", "", "", "", "")
	got, err = db.SessionVisit(ctx, 1, "user", "a2", formsNow)
	if err != nil || got == nil || got.LandingPath != "/edge" || got.Views != 2 {
		t.Fatalf("30m gap: %+v, %v", got, err)
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

// TestSessionVisitNeedsACurrentSession: the newest view must be within
// 30 minutes of `at`, the gap rule's own boundary.
func TestSessionVisitNeedsACurrentSession(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	addView(t, db, "v1", 1, "a1", formsNow.Add(-31*time.Minute), "/old", "", "", "", "")
	addView(t, db, "v2", 1, "a2", formsNow.Add(-30*time.Minute), "/edge", "", "", "", "")
	got, err := db.SessionVisit(ctx, 1, "user", "a1", formsNow)
	if err != nil || got != nil {
		t.Fatalf("31m: %+v, %v", got, err)
	}
	got, err = db.SessionVisit(ctx, 1, "user", "a2", formsNow)
	if err != nil || got == nil || got.LandingPath != "/edge" || got.Views != 1 {
		t.Fatalf("30m: %+v, %v", got, err)
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
	if c := countRows(t, db, `SELECT COUNT(*) FROM events`); c != 4 {
		t.Fatalf("setup: %d events, want 4", c)
	}
	if ids := rawProductIDs(t, db); len(ids) != 3 {
		t.Fatalf("setup: raw product ids = %v", ids)
	}

	n, err := db.DeleteSubmissions(ctx, 1, []string{"s1", "s3", "s0", "missing"},
		store.AuditEntry{Actor: "agent", Action: "submission.delete", Subject: "project/1", Detail: `{"count":3}`})
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
	if c := countRows(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='submission.delete'`); c != 1 {
		t.Fatalf("%d audit rows", c)
	}
	if n, err := db.DeleteSubmissions(ctx, 1, nil, formAudit); err != nil || n != 0 {
		t.Fatalf("empty ids: %d, %v", n, err)
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
	if !reflect.DeepEqual(ids(subs), []string{"b", "a"}) || next != "" {
		t.Fatalf("alice = %v next %q", ids(subs), next)
	}
	if subs[1].Form != "contact" || subs[1].Fields["email"] != "Alice@Example.com" || subs[1].ProjectID != 1 ||
		!subs[1].ReceivedAt.Equal(formsNow.Add(-3*time.Hour)) {
		t.Fatalf("submission = %+v", subs[1])
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
	if err != nil || !reflect.DeepEqual(all, []string{"b", "a"}) {
		t.Fatalf("SubmissionIDsMatching = %v, %v", all, err)
	}
	if _, err := db.SubmissionIDsMatching(ctx, 1, ""); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty search = %v", err)
	}
}
