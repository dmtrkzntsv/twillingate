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
