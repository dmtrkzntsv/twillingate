package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

var (
	alertNow = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	alertAt  = "2026-11-25T10:00:00Z"
)

func alertAudit(action string) store.AuditEntry {
	return store.AuditEntry{Actor: "test", Action: action}
}

func alertDef(window int) store.AlertDef {
	v := 5.0
	return store.AlertDef{
		Query:      []byte(`{"stat":"views"}`),
		Read:       store.AlertRead{Metric: "views"},
		Condition:  store.AlertCondition{Type: "threshold", Op: "gt", Value: &v},
		WindowDays: window,
		Fields:     map[string]string{"a": "b"},
	}
}

func newAlert(t *testing.T, d *DB, project int64, name string) store.Alert {
	t.Helper()
	a, err := d.CreateAlert(context.Background(), store.Alert{ProjectID: project, Name: name, Def: alertDef(7)}, 50, alertNow, alertAudit("alert.create"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func updAlert(t *testing.T, d *DB, project, id int64, e store.AlertEdit, now time.Time) store.Alert {
	t.Helper()
	a, err := d.UpdateAlert(context.Background(), project, id, e, now, alertAudit("alert.update"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func ptr[T any](v T) *T { return &v }

func TestMigration051CreatesAlertTables(t *testing.T) {
	db := newTestDBAt(t, 50)
	if err := db.migrateThrough(context.Background(), 51); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ typ, name string }{
		{"table", "alerts"}, {"table", "alert_evaluations"},
		{"index", "alerts_live_name"}, {"index", "alert_evaluations_alert"},
	} {
		if n := countRows(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?`, c.typ, c.name); n != 1 {
			t.Errorf("%s %s: %d rows in sqlite_master, want 1", c.typ, c.name, n)
		}
	}
}

func TestCreateAlertAuditsAndReadsBack(t *testing.T) {
	db := newTestDB(t)
	p := monitorProject(t, db, "p")
	a := newAlert(t, db, p, "traffic")
	if a.ID <= 0 || a.State != store.AlertPending || a.Revision != 1 || a.CheckRequestedAt != a.CreatedAt || a.CreatedAt != alertAt {
		t.Fatalf("created = %+v", a)
	}
	got, err := db.GetAlert(context.Background(), p, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, a) {
		t.Errorf("GetAlert = %+v, want %+v", got, a)
	}
	if got.Def.Condition.Value == nil || *got.Def.Condition.Value != 5 || got.Def.Fields["a"] != "b" {
		t.Errorf("def = %+v", got.Def)
	}
	rows := auditRows(t, db, "alert.create")
	if len(rows) != 1 || rows[0].Subject != alertSubject(a.ID) || rows[0].Detail != "traffic" {
		t.Errorf("audit = %+v", rows)
	}
	if _, err := db.GetAlert(context.Background(), p+1, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetAlert of another project: %v, want ErrNotFound", err)
	}
	if _, err := db.CreateAlert(context.Background(), store.Alert{ProjectID: p, Name: "bad", Def: store.AlertDef{Query: []byte(`{`)}}, 5, alertNow, alertAudit("alert.create")); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("invalid definition JSON: %v, want ErrInvalid", err)
	}
}

func TestListAlerts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := monitorProject(t, db, "p")
	b, a := newAlert(t, db, p, "b"), newAlert(t, db, p, "a")
	old := newAlert(t, db, p, "old")
	if _, err := db.SetAlertArchived(ctx, p, old.ID, true, 5, alertNow, alertAudit("alert.archive")); err != nil {
		t.Fatal(err)
	}
	live, err := db.ListAlerts(ctx, p, false)
	if err != nil || len(live) != 2 || live[0].ID != a.ID || live[1].ID != b.ID {
		t.Errorf("live = %+v, %v", live, err)
	}
	arch, err := db.ListAlerts(ctx, p, true)
	if err != nil || len(arch) != 1 || arch[0].ID != old.ID {
		t.Errorf("archived = %+v, %v", arch, err)
	}
}

func TestCreateAlertRefusesTakenNameAndCap(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := monitorProject(t, db, "p")
	a := newAlert(t, db, p, "x")
	create := func(name string, max int) error {
		_, err := db.CreateAlert(ctx, store.Alert{ProjectID: p, Name: name, Def: alertDef(7)}, max, alertNow, alertAudit("alert.create"))
		return err
	}
	if err := create("x", 5); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate name: %v, want ErrConflict", err)
	}
	if _, err := db.SetAlertArchived(ctx, p, a.ID, true, 5, alertNow, alertAudit("alert.archive")); err != nil {
		t.Fatal(err)
	}
	if err := create("x", 2); err != nil {
		t.Errorf("same name after archiving: %v", err)
	}
	if err := create("y", 2); err != nil {
		t.Errorf("second live alert under max 2: %v", err)
	}
	if err := create("z", 2); !errors.Is(err, store.ErrConflict) {
		t.Errorf("third live alert under max 2: %v, want ErrConflict", err)
	}
	if _, err := db.CreateAlert(ctx, store.Alert{ProjectID: 9999, Name: "q", Def: alertDef(7)}, 2, alertNow, alertAudit("alert.create")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown project: %v, want ErrNotFound", err)
	}
}

func TestUpdateAlertResetsOnDefinition(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := monitorProject(t, db, "p")
	a := newAlert(t, db, p, "x")
	other := newAlert(t, db, p, "other")
	execAll(t, db, `UPDATE alerts SET state='breached', reason='r', last_value=3, last_day='2026-11-24', fails=2, retry_at='2026-11-25T10:05:00Z' WHERE id=`+strconv.FormatInt(a.ID, 10))
	later := alertNow.Add(time.Hour)

	r := updAlert(t, db, p, a.ID, store.AlertEdit{Name: ptr("renamed")}, later)
	if r.Name != "renamed" || r.Revision != 1 || r.State != store.AlertBreached || r.LastValue == nil || r.Fails != 2 {
		t.Errorf("rename reset the alert: %+v", r)
	}
	if rows := auditRows(t, db, "alert.update"); len(rows) != 1 || rows[0].Detail != "name" || rows[0].Subject != alertSubject(a.ID) {
		t.Errorf("rename audit = %+v", rows)
	}
	if _, err := db.UpdateAlert(ctx, p, a.ID, store.AlertEdit{Name: ptr("other")}, later, alertAudit("alert.update")); !errors.Is(err, store.ErrConflict) {
		t.Errorf("rename onto a live name: %v, want ErrConflict", err)
	}

	r = updAlert(t, db, p, a.ID, store.AlertEdit{Def: ptr(alertDef(14))}, later)
	if r.Revision != 2 || r.State != store.AlertPending || r.LastValue != nil || r.LastDay != "" || r.Fails != 0 || r.Reason != "" || r.RetryAt != "" || r.CheckRequestedAt != alertTime(later) {
		t.Errorf("definition edit: %+v", r)
	}
	if r.Def.WindowDays != 14 {
		t.Errorf("window = %d", r.Def.WindowDays)
	}

	n := len(auditRows(t, db, "alert.update"))
	same := updAlert(t, db, p, a.ID, store.AlertEdit{Name: ptr("renamed"), Def: ptr(alertDef(14)), Paused: ptr(false), SnoozedUntil: ptr("")}, later.Add(time.Hour))
	if !reflect.DeepEqual(same, r) || len(auditRows(t, db, "alert.update")) != n {
		t.Errorf("an unchanged edit wrote: %+v, audits %d -> %d", same, n, len(auditRows(t, db, "alert.update")))
	}

	if _, err := db.SetAlertArchived(ctx, p, other.ID, true, 5, alertNow, alertAudit("alert.archive")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdateAlert(ctx, p, other.ID, store.AlertEdit{Name: ptr("z")}, later, alertAudit("alert.update")); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("archived edit: %v, want ErrInvalid", err)
	}
	if _, err := db.UpdateAlert(ctx, p+1, a.ID, store.AlertEdit{Name: ptr("z")}, later, alertAudit("alert.update")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("other project's alert: %v, want ErrNotFound", err)
	}
}

func TestPausingEndsTheCheckRequest(t *testing.T) {
	db := newTestDB(t)
	p := monitorProject(t, db, "p")
	a := newAlert(t, db, p, "x")
	t1, t2, t3, t4 := alertNow.Add(time.Hour), alertNow.Add(2*time.Hour), alertNow.Add(3*time.Hour), alertNow.Add(4*time.Hour)

	r := updAlert(t, db, p, a.ID, store.AlertEdit{Paused: ptr(true)}, t1)
	if r.Revision != 2 || r.State != store.AlertPending || r.CheckRequestedAt != "" || r.CheckPending() {
		t.Errorf("pause: %+v", r)
	}
	r = updAlert(t, db, p, a.ID, store.AlertEdit{Def: ptr(alertDef(9))}, t1)
	if r.CheckRequestedAt != "" {
		t.Errorf("definition edit while paused requests a check: %+v", r)
	}
	r = updAlert(t, db, p, a.ID, store.AlertEdit{Paused: ptr(false)}, t2)
	if r.CheckRequestedAt != alertTime(t2) {
		t.Errorf("unpause: %+v", r)
	}
	snooze := alertTime(t4.Add(24 * time.Hour))
	r = updAlert(t, db, p, a.ID, store.AlertEdit{SnoozedUntil: &snooze}, t3)
	if r.SnoozedUntil != snooze || r.CheckRequestedAt != "" || r.Revision != 5 {
		t.Errorf("snooze set: %+v", r)
	}
	r = updAlert(t, db, p, a.ID, store.AlertEdit{SnoozedUntil: ptr("")}, t4)
	if r.SnoozedUntil != "" || r.CheckRequestedAt != alertTime(t4) {
		t.Errorf("snooze cleared: %+v", r)
	}

	// Unpausing while snoozed leaves nothing requested.
	updAlert(t, db, p, a.ID, store.AlertEdit{Paused: ptr(true), SnoozedUntil: &snooze}, t4)
	r = updAlert(t, db, p, a.ID, store.AlertEdit{Paused: ptr(false)}, t4)
	if r.CheckRequestedAt != "" {
		t.Errorf("unpause while snoozed: %+v", r)
	}

	// A definition edit on an alert of an archived project too.
	b := newAlert(t, db, p, "y")
	if err := db.SetProjectArchived(context.Background(), p, true, alertAudit("project.archive")); err != nil {
		t.Fatal(err)
	}
	r = updAlert(t, db, p, b.ID, store.AlertEdit{Def: ptr(alertDef(11))}, t4)
	if r.CheckRequestedAt != "" {
		t.Errorf("definition edit in an archived project: %+v", r)
	}
}

// TestUpdateAlertExpectRevision: an edit expecting another revision than
// the row's is ErrConflict and writes nothing; 0 expects none.
func TestUpdateAlertExpectRevision(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := monitorProject(t, db, "p")
	a := newAlert(t, db, p, "x")
	moved := updAlert(t, db, p, a.ID, store.AlertEdit{Def: ptr(alertDef(14))}, alertNow)
	n := len(auditRows(t, db, "alert.update"))

	_, err := db.UpdateAlert(ctx, p, a.ID, store.AlertEdit{Def: ptr(alertDef(3)), ExpectRevision: a.Revision}, alertNow, alertAudit("alert.update"))
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a stale edit: %v, want ErrConflict", err)
	}
	if got, _ := db.GetAlert(ctx, p, a.ID); !reflect.DeepEqual(got, moved) || len(auditRows(t, db, "alert.update")) != n {
		t.Errorf("a stale edit wrote: %+v", got)
	}
	if r := updAlert(t, db, p, a.ID, store.AlertEdit{Def: ptr(alertDef(3)), ExpectRevision: moved.Revision}, alertNow); r.Def.WindowDays != 3 {
		t.Errorf("a current edit: %+v", r)
	}
	if r := updAlert(t, db, p, a.ID, store.AlertEdit{Name: ptr("y")}, alertNow); r.Name != "y" {
		t.Errorf("an edit expecting none: %+v", r)
	}
}

// TestClearingAnExpiredSnoozeChangesNothing: a snooze at or before now
// is none, so clearing it writes nothing (no reset, no new revision, no
// audit), while a new snooze over it is a change.
func TestClearingAnExpiredSnoozeChangesNothing(t *testing.T) {
	db := newTestDB(t)
	p := monitorProject(t, db, "p")
	a := newAlert(t, db, p, "x")
	until := alertNow.Add(time.Hour)
	updAlert(t, db, p, a.ID, store.AlertEdit{SnoozedUntil: ptr(alertTime(until))}, alertNow)
	execAll(t, db, `UPDATE alerts SET state='breached', last_value=3 WHERE id=`+strconv.FormatInt(a.ID, 10))
	snoozed, err := db.GetAlert(context.Background(), p, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := len(auditRows(t, db, "alert.update"))

	for _, at := range []time.Time{until, until.Add(time.Hour)} {
		r := updAlert(t, db, p, a.ID, store.AlertEdit{SnoozedUntil: ptr("")}, at)
		if !reflect.DeepEqual(r, snoozed) || len(auditRows(t, db, "alert.update")) != n {
			t.Errorf("clearing a snooze over at %s wrote: %+v, audits %d -> %d", alertTime(at), r, n, len(auditRows(t, db, "alert.update")))
		}
	}

	later := until.Add(time.Hour)
	r := updAlert(t, db, p, a.ID, store.AlertEdit{SnoozedUntil: ptr(alertTime(later.Add(time.Hour)))}, later)
	if r.Revision != snoozed.Revision+1 || r.State != store.AlertPending || len(auditRows(t, db, "alert.update")) != n+1 {
		t.Errorf("a new snooze over an expired one: %+v", r)
	}
}

func TestArchiveRestoreAlert(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := monitorProject(t, db, "p")
	a := newAlert(t, db, p, "x")
	later := alertNow.Add(time.Hour)

	r, err := db.SetAlertArchived(ctx, p, a.ID, true, 5, later, alertAudit("alert.archive"))
	if err != nil {
		t.Fatal(err)
	}
	if r.ArchivedAt != alertTime(later) || r.CheckRequestedAt != "" || r.Revision != 2 {
		t.Errorf("archived: %+v", r)
	}
	if _, err := db.SetAlertArchived(ctx, p, a.ID, true, 5, later, alertAudit("alert.archive")); err != nil {
		t.Fatal(err)
	}
	if rows := auditRows(t, db, "alert.archive"); len(rows) != 1 || rows[0].Subject != alertSubject(a.ID) {
		t.Errorf("archive audit = %+v", rows)
	}

	taken := newAlert(t, db, p, "x")
	if _, err := db.SetAlertArchived(ctx, p, a.ID, false, 5, later, alertAudit("alert.restore")); !errors.Is(err, store.ErrConflict) {
		t.Errorf("restore onto a live name: %v, want ErrConflict", err)
	}
	if _, err := db.SetAlertArchived(ctx, p, taken.ID, true, 5, later, alertAudit("alert.archive")); err != nil {
		t.Fatal(err)
	}
	newAlert(t, db, p, "y")
	if _, err := db.SetAlertArchived(ctx, p, a.ID, false, 1, later, alertAudit("alert.restore")); !errors.Is(err, store.ErrConflict) {
		t.Errorf("restore at the cap: %v, want ErrConflict", err)
	}
	r, err = db.SetAlertArchived(ctx, p, a.ID, false, 5, later, alertAudit("alert.restore"))
	if err != nil {
		t.Fatal(err)
	}
	if r.ArchivedAt != "" || r.State != store.AlertPending || r.Revision != 3 || r.CheckRequestedAt != alertTime(later) {
		t.Errorf("restored: %+v", r)
	}
}

func TestRequestAlertCheck(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := monitorProject(t, db, "p")
	a := newAlert(t, db, p, "x")
	// Created with a request pending, so a second is not accepted.
	r, ok, err := db.RequestAlertCheck(ctx, p, a.ID, alertNow.Add(time.Minute), alertAudit("alert.check"))
	if err != nil || ok || r.CheckRequestedAt != alertAt {
		t.Fatalf("request while pending: %+v %t %v", r, ok, err)
	}
	execAll(t, db, `UPDATE alerts SET last_evaluated_at='`+alertAt+`'`)
	later := alertNow.Add(time.Hour)
	r, ok, err = db.RequestAlertCheck(ctx, p, a.ID, later, alertAudit("alert.check"))
	if err != nil || !ok || r.CheckRequestedAt != alertTime(later) || !r.CheckPending() {
		t.Fatalf("first request: %+v %t %v", r, ok, err)
	}
	if rows := auditRows(t, db, "alert.check"); len(rows) != 1 || rows[0].Subject != alertSubject(a.ID) || rows[0].Detail != "x" {
		t.Errorf("audit = %+v", rows)
	}
	if _, ok, err = db.RequestAlertCheck(ctx, p, a.ID, later.Add(time.Minute), alertAudit("alert.check")); err != nil || ok {
		t.Errorf("second request while pending: %t %v", ok, err)
	}
	if len(auditRows(t, db, "alert.check")) != 1 {
		t.Error("a refused request wrote an audit row")
	}
	execAll(t, db, `UPDATE alerts SET last_evaluated_at='`+alertTime(later)+`'`)
	if _, ok, err = db.RequestAlertCheck(ctx, p, a.ID, later.Add(time.Minute), alertAudit("alert.check")); err != nil || !ok {
		t.Errorf("request after the evaluation: %t %v", ok, err)
	}
	// A request in the very second of the last evaluation is pending too:
	// it clears last_evaluated_at, since both are whole seconds.
	evaluated := later.Add(2 * time.Minute)
	execAll(t, db, `UPDATE alerts SET last_evaluated_at='`+alertTime(evaluated)+`'`)
	r, ok, err = db.RequestAlertCheck(ctx, p, a.ID, evaluated.Add(500*time.Millisecond), alertAudit("alert.check"))
	if err != nil || !ok || r.LastEvaluatedAt != "" || !r.CheckPending() {
		t.Errorf("request in the evaluation's second: %+v %t %v", r, ok, err)
	}

	// An alert nothing would evaluate is refused.
	updAlert(t, db, p, a.ID, store.AlertEdit{Paused: ptr(true)}, later)
	if _, _, err = db.RequestAlertCheck(ctx, p, a.ID, later, alertAudit("alert.check")); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("paused: %v, want ErrInvalid", err)
	}
	if _, err := db.SetAlertArchived(ctx, p, a.ID, true, 5, later, alertAudit("alert.archive")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.RequestAlertCheck(ctx, p, a.ID, later, alertAudit("alert.check")); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("archived: %v, want ErrInvalid", err)
	}
	if _, _, err = db.RequestAlertCheck(ctx, p, a.ID+99, later, alertAudit("alert.check")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown alert: %v, want ErrNotFound", err)
	}
}

func TestArchivingProjectEndsAlertChecks(t *testing.T) {
	db := newTestDB(t)
	p := monitorProject(t, db, "p")
	other := monitorProject(t, db, "q")
	newAlert(t, db, p, "x")
	newAlert(t, db, other, "y")
	if err := db.SetProjectArchived(context.Background(), p, true, alertAudit("project.archive")); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM alerts WHERE project_id=? AND check_requested_at=''`, p); n != 1 {
		t.Errorf("archived project's alerts without a request = %d, want 1", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM alerts WHERE project_id=? AND check_requested_at<>''`, other); n != 1 {
		t.Errorf("other project's request was cleared")
	}
}

func evalOf(a store.Alert, at string, v float64) store.AlertEvaluation {
	return store.AlertEvaluation{AlertID: a.ID, At: at, From: "2026-11-24", To: "2026-11-24", Value: &v, Outcome: store.AlertBreached}
}

func alertRow(t *testing.T, db *DB, p, id int64) store.Alert {
	t.Helper()
	a, err := db.GetAlert(context.Background(), p, id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRecordAlertEvaluation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := monitorProject(t, db, "p")
	a := newAlert(t, db, p, "x")
	at := "2026-11-25T10:05:00Z"
	ok, err := db.RecordAlertEvaluation(ctx, store.AlertResult{AlertID: a.ID, Revision: a.Revision, Eval: evalOf(a, at, 9),
		State: store.AlertBreached, Reason: "9 > 5", LastDay: "2026-11-24", Fails: 0, RetryAt: ""})
	if err != nil || !ok {
		t.Fatalf("record = %v, %v", ok, err)
	}
	got := alertRow(t, db, p, a.ID)
	if got.State != store.AlertBreached || got.Reason != "9 > 5" || got.LastValue == nil || *got.LastValue != 9 ||
		got.LastEvaluatedAt != at || got.LastDay != "2026-11-24" || got.Fails != 0 || got.RetryAt != "" {
		t.Fatalf("row = %+v", got)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM alert_evaluations WHERE alert_id=?`, a.ID); n != 1 {
		t.Fatalf("history rows = %d", n)
	}

	// State "" keeps state, reason, last_value and last_day, writes the rest.
	at2 := "2026-11-25T11:05:00Z"
	failed := store.AlertEvaluation{AlertID: a.ID, At: at2, Outcome: store.AlertError, Error: "boom"}
	ok, err = db.RecordAlertEvaluation(ctx, store.AlertResult{AlertID: a.ID, Revision: a.Revision, Eval: failed,
		Fails: 1, RetryAt: "2026-11-25T12:05:00Z"})
	if err != nil || !ok {
		t.Fatalf("record failed try = %v, %v", ok, err)
	}
	got = alertRow(t, db, p, a.ID)
	if got.State != store.AlertBreached || got.LastValue == nil || *got.LastValue != 9 || got.LastDay != "2026-11-24" ||
		got.Fails != 1 || got.RetryAt != "2026-11-25T12:05:00Z" || got.LastEvaluatedAt != at2 {
		t.Fatalf("row after failed try = %+v", got)
	}
	ev, err := db.ListAlertEvaluations(ctx, a.ID, 10, 0)
	if err != nil || len(ev) != 2 || ev[0].Error != "boom" || ev[0].Value != nil || ev[1].Value == nil || *ev[1].Value != 9 {
		t.Fatalf("history = %+v, %v", ev, err)
	}
}

func TestRecordAlertEvaluationRefusals(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := monitorProject(t, db, "p")
	stale := newAlert(t, db, p, "stale")
	updAlert(t, db, p, stale.ID, store.AlertEdit{Def: ptr(alertDef(3))}, alertNow)
	archived := newAlert(t, db, p, "archived")
	if _, err := db.SetAlertArchived(ctx, p, archived.ID, true, 50, alertNow, alertAudit("alert.archive")); err != nil {
		t.Fatal(err)
	}
	q := monitorProject(t, db, "q")
	inArchivedProject := newAlert(t, db, q, "y")
	if err := db.SetProjectArchived(ctx, q, true, alertAudit("project.archive")); err != nil {
		t.Fatal(err)
	}
	for name, a := range map[string]store.Alert{"stale revision": stale, "archived alert": archived, "archived project": inArchivedProject} {
		rev := a.Revision
		ok, err := db.RecordAlertEvaluation(ctx, store.AlertResult{AlertID: a.ID, Revision: rev, Eval: evalOf(a, alertAt, 1), State: store.AlertOK})
		if err != nil || ok {
			t.Errorf("%s: record = %v, %v; want false", name, ok, err)
		}
		if n := countRows(t, db, `SELECT COUNT(*) FROM alert_evaluations WHERE alert_id=?`, a.ID); n != 0 {
			t.Errorf("%s: %d history rows written", name, n)
		}
	}
}

func TestListAlertEvaluationsPages(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := monitorProject(t, db, "p")
	a := newAlert(t, db, p, "x")
	for i := range 5 {
		if ok, err := db.RecordAlertEvaluation(ctx, store.AlertResult{AlertID: a.ID, Revision: a.Revision,
			Eval: evalOf(a, "2026-11-25T10:0"+strconv.Itoa(i)+":00Z", float64(i))}); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	ids := func(es []store.AlertEvaluation) []int64 {
		var out []int64
		for _, e := range es {
			out = append(out, e.ID)
		}
		return out
	}
	first, err := db.ListAlertEvaluations(ctx, a.ID, 2, 0)
	if err != nil || !reflect.DeepEqual(ids(first), []int64{5, 4}) {
		t.Fatalf("first page = %v, %v", ids(first), err)
	}
	next, err := db.ListAlertEvaluations(ctx, a.ID, 2, 4)
	if err != nil || !reflect.DeepEqual(ids(next), []int64{3, 2}) {
		t.Fatalf("second page = %v, %v", ids(next), err)
	}
}

func TestRecentAlertEvaluations(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := monitorProject(t, db, "p")
	var as []store.Alert
	for _, n := range []string{"a", "b", "c"} {
		as = append(as, newAlert(t, db, p, n))
	}
	for i := range 3 {
		for _, a := range as[:2] {
			if ok, err := db.RecordAlertEvaluation(ctx, store.AlertResult{AlertID: a.ID, Revision: a.Revision,
				Eval: evalOf(a, "2026-11-25T10:0"+strconv.Itoa(i)+":00Z", float64(i))}); err != nil || !ok {
				t.Fatal(ok, err)
			}
		}
	}
	got, err := db.RecentAlertEvaluations(ctx, []int64{as[0].ID, as[1].ID, as[2].ID}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, has := got[as[2].ID]; has || len(got) != 2 {
		t.Fatalf("alert without evaluations is present: %v", got)
	}
	for _, a := range as[:2] {
		es := got[a.ID]
		if len(es) != 2 || *es[0].Value != 2 || *es[1].Value != 1 {
			t.Errorf("alert %d: %+v", a.ID, es)
		}
	}
	if none, err := db.RecentAlertEvaluations(ctx, nil, 2); err != nil || len(none) != 0 {
		t.Errorf("no ids = %v, %v", none, err)
	}
}

func TestListEvaluableAlerts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := monitorProject(t, db, "p")
	keep := newAlert(t, db, p, "keep")
	paused := newAlert(t, db, p, "paused")
	updAlert(t, db, p, paused.ID, store.AlertEdit{Paused: ptr(true)}, alertNow)
	snoozed := newAlert(t, db, p, "snoozed")
	updAlert(t, db, p, snoozed.ID, store.AlertEdit{SnoozedUntil: ptr("2026-12-01T00:00:00Z")}, alertNow)
	gone := newAlert(t, db, p, "gone")
	if _, err := db.SetAlertArchived(ctx, p, gone.ID, true, 50, alertNow, alertAudit("alert.archive")); err != nil {
		t.Fatal(err)
	}
	q := monitorProject(t, db, "q")
	newAlert(t, db, q, "of archived project")
	if err := db.SetProjectArchived(ctx, q, true, alertAudit("project.archive")); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListEvaluableAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range got {
		names = append(names, a.Name)
	}
	if !reflect.DeepEqual(names, []string{"keep", "paused", "snoozed"}) || got[0].ID != keep.ID {
		t.Fatalf("evaluable = %v", names)
	}
}

// TestListCheckPendingAlerts lists the live alerts of live projects whose
// check is pending, as CheckPending reads it, by id.
func TestListCheckPendingAlerts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := monitorProject(t, db, "p")
	asked := newAlert(t, db, p, "asked") // its creation check
	served := newAlert(t, db, p, "served")
	if _, err := db.RecordAlertEvaluation(ctx, store.AlertResult{AlertID: served.ID, Revision: served.Revision,
		Eval: store.AlertEvaluation{AlertID: served.ID, At: alertAt, Outcome: store.AlertOK}, State: store.AlertOK}); err != nil {
		t.Fatal(err)
	}
	gone := newAlert(t, db, p, "gone")
	if _, err := db.SetAlertArchived(ctx, p, gone.ID, true, 50, alertNow, alertAudit("alert.archive")); err != nil {
		t.Fatal(err)
	}
	q := monitorProject(t, db, "q")
	newAlert(t, db, q, "of archived project")
	if err := db.SetProjectArchived(ctx, q, true, alertAudit("project.archive")); err != nil {
		t.Fatal(err)
	}
	again := newAlert(t, db, p, "again")
	if _, err := db.RecordAlertEvaluation(ctx, store.AlertResult{AlertID: again.ID, Revision: again.Revision,
		Eval: store.AlertEvaluation{AlertID: again.ID, At: alertAt, Outcome: store.AlertOK}, State: store.AlertOK}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.RequestAlertCheck(ctx, p, again.ID, alertNow.Add(time.Minute), alertAudit("alert.check")); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListCheckPendingAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, a := range got {
		if !a.CheckPending() {
			t.Errorf("%s listed, not pending: %+v", a.Name, a)
		}
		ids = append(ids, a.ID)
	}
	if !reflect.DeepEqual(ids, []int64{asked.ID, again.ID}) {
		t.Fatalf("pending = %v, want %v", ids, []int64{asked.ID, again.ID})
	}
}

// TestResetInTheSecondOfAnEvaluationLeavesTheCheckPending: a reset to
// pending clears last_evaluated_at, so a check it requests is pending even
// when it lands in the very second the last evaluation was recorded (both
// are whole seconds); a restore does the same.
func TestResetInTheSecondOfAnEvaluationLeavesTheCheckPending(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := monitorProject(t, db, "p")
	a := newAlert(t, db, p, "x")
	evaluate := func(a store.Alert) {
		t.Helper()
		if ok, err := db.RecordAlertEvaluation(ctx, store.AlertResult{AlertID: a.ID, Revision: a.Revision,
			Eval: evalOf(a, alertAt, 9), State: store.AlertBreached}); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	evaluate(a)
	def := alertDef(3)
	e := updAlert(t, db, p, a.ID, store.AlertEdit{Def: &def}, alertNow)
	if e.CheckRequestedAt != alertAt || e.LastEvaluatedAt != "" || !e.CheckPending() {
		t.Fatalf("edited in the evaluation's second: requested %q, last evaluated %q, pending %t",
			e.CheckRequestedAt, e.LastEvaluatedAt, e.CheckPending())
	}

	evaluate(e)
	if _, err := db.SetAlertArchived(ctx, p, a.ID, true, 5, alertNow, alertAudit("alert.archive")); err != nil {
		t.Fatal(err)
	}
	r, err := db.SetAlertArchived(ctx, p, a.ID, false, 5, alertNow, alertAudit("alert.restore"))
	if err != nil || r.LastEvaluatedAt != "" || !r.CheckPending() {
		t.Fatalf("restored in the evaluation's second: %+v, %v", r, err)
	}
}

// TestArchivingProjectResetsAlerts: archiving a project returns its live
// alerts to pending with a new revision at once, so a restore before the
// runner's next tick resumes them from pending and the tracker sees the
// revision change. Archiving it again writes nothing.
func TestArchivingProjectResetsAlerts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := monitorProject(t, db, "p")
	q := monitorProject(t, db, "q")
	mine, other := newAlert(t, db, p, "x"), newAlert(t, db, q, "y")
	for _, a := range []store.Alert{mine, other} {
		if ok, err := db.RecordAlertEvaluation(ctx, store.AlertResult{AlertID: a.ID, Revision: a.Revision, Eval: evalOf(a, alertAt, 9),
			State: store.AlertBreached, Reason: "r", LastDay: "2026-11-24", Fails: 1, RetryAt: "x"}); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	if err := db.SetProjectArchived(ctx, p, true, alertAudit("project.archive")); err != nil {
		t.Fatal(err)
	}
	if err := db.SetProjectArchived(ctx, p, true, alertAudit("project.archive")); err != nil {
		t.Fatal(err)
	}
	if err := db.SetProjectArchived(ctx, p, false, alertAudit("project.restore")); err != nil {
		t.Fatal(err)
	}
	got := alertRow(t, db, p, mine.ID)
	if got.State != store.AlertPending || got.Revision != mine.Revision+1 || got.LastDay != "" || got.Reason != "" ||
		got.LastValue != nil || got.Fails != 0 || got.RetryAt != "" || got.LastEvaluatedAt != "" || got.CheckRequestedAt != "" {
		t.Errorf("restored project's alert = %+v", got)
	}
	if o := alertRow(t, db, q, other.ID); o.State != store.AlertBreached || o.LastDay == "" || o.Revision != other.Revision {
		t.Errorf("live project's alert touched: %+v", o)
	}
}

func TestPruneAlertEvaluations(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	p := monitorProject(t, db, "p")
	a := newAlert(t, db, p, "x")
	for _, at := range []string{"2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z", "2026-03-01T00:00:00Z"} {
		if ok, err := db.RecordAlertEvaluation(ctx, store.AlertResult{AlertID: a.ID, Revision: a.Revision, Eval: evalOf(a, at, 1)}); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	n, err := db.PruneAlertEvaluations(ctx, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || n != 2 {
		t.Fatalf("pruned = %d, %v; want 2", n, err)
	}
	if left := countRows(t, db, `SELECT COUNT(*) FROM alert_evaluations`); left != 1 {
		t.Errorf("left = %d", left)
	}
}
