package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

var notifyNow = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

// newNotifyDB is a migrated database with project 1001 "blog".
func newNotifyDB(t *testing.T) *DB {
	t.Helper()
	db := newTestDB(t)
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1001, 'blog', '[]')`)
	return db
}

func addNotification(t *testing.T, db *DB, typ, subject string, at time.Time) int64 {
	t.Helper()
	id, err := db.AddNotification(context.Background(), store.NewNotification{
		Type: typ, ProjectID: 1001, Subject: subject, Fields: map[string]string{"origin": subject}, At: at})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func notificationIDs(ns []store.Notification) []int64 {
	out := make([]int64, len(ns))
	for i, n := range ns {
		out[i] = n.ID
	}
	return out
}

func listNotifications(t *testing.T, db *DB, f store.NotificationFilter) []store.Notification {
	t.Helper()
	ns, err := db.ListNotifications(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

func TestAddAndListNotifications(t *testing.T) {
	db := newNotifyDB(t)
	a := addNotification(t, db, store.NotifyUptimeDown, "https://a.io", notifyNow)
	b := addNotification(t, db, store.NotifyUptimeUp, "https://a.io", notifyNow.Add(time.Minute))
	c := addNotification(t, db, store.NotifyCertExpiring, "https://b.io", notifyNow.Add(2*time.Minute))

	ns := listNotifications(t, db, store.NotificationFilter{})
	if got := notificationIDs(ns); len(got) != 3 || got[0] != c || got[1] != b || got[2] != a {
		t.Fatalf("ids = %v, want [%d %d %d]", got, c, b, a)
	}
	n := ns[2]
	if n.Type != store.NotifyUptimeDown || n.ProjectID != 1001 || n.Subject != "https://a.io" ||
		n.Fields["project"] != "blog" || n.Fields["origin"] != "https://a.io" ||
		!n.CreatedAt.Equal(notifyNow) || n.ArchivedAt != nil ||
		n.Delivery != store.DeliveryPending || n.Attempts != 0 || n.NextAttemptAt != nil {
		t.Errorf("notification = %+v", n)
	}

	page := listNotifications(t, db, store.NotificationFilter{Limit: 2})
	if got := notificationIDs(page); len(got) != 2 || got[0] != c || got[1] != b {
		t.Fatalf("page 1 = %v", got)
	}
	page = listNotifications(t, db, store.NotificationFilter{Limit: 2, Before: page[1].ID})
	if got := notificationIDs(page); len(got) != 1 || got[0] != a {
		t.Fatalf("page 2 = %v", got)
	}
	if got := listNotifications(t, db, store.NotificationFilter{ProjectID: 2}); len(got) != 0 {
		t.Errorf("another project's: %v", notificationIDs(got))
	}
	if got := listNotifications(t, db, store.NotificationFilter{Archived: true}); len(got) != 0 {
		t.Errorf("archived: %v", notificationIDs(got))
	}

	// No project: no "project" field, and a caller's own value is kept.
	id, err := db.AddNotification(context.Background(), store.NewNotification{
		Type: store.NotifyFormSubmission, Subject: "x", At: notifyNow})
	if err != nil {
		t.Fatal(err)
	}
	if ns := listNotifications(t, db, store.NotificationFilter{Limit: 1}); ns[0].ID != id ||
		ns[0].ProjectID != 0 || len(ns[0].Fields) != 0 {
		t.Errorf("projectless = %+v", ns[0])
	}
	fields := map[string]string{"project": "old name"}
	if _, err := db.AddNotification(context.Background(), store.NewNotification{
		Type: store.NotifyUptimeDown, ProjectID: 1001, Fields: fields, At: notifyNow}); err != nil {
		t.Fatal(err)
	}
	if ns := listNotifications(t, db, store.NotificationFilter{Limit: 1}); ns[0].Fields["project"] != "old name" {
		t.Errorf("project field = %q, want the caller's", ns[0].Fields["project"])
	}
}

func TestArchiveRestoreAll(t *testing.T) {
	db := newNotifyDB(t)
	ctx := context.Background()
	a := addNotification(t, db, store.NotifyUptimeDown, "https://a.io", notifyNow)
	addNotification(t, db, store.NotifyUptimeUp, "https://a.io", notifyNow)
	addNotification(t, db, store.NotifyUptimeUp, "https://b.io", notifyNow)

	if n, err := db.SetNotificationsArchived(ctx, []int64{a, 99999}, false, true, notifyNow); err != nil || n != 1 {
		t.Fatalf("archive one: n=%d err=%v", n, err)
	}
	if n, err := db.SetNotificationsArchived(ctx, []int64{a}, false, true, notifyNow); err != nil || n != 0 {
		t.Fatalf("archive again: n=%d err=%v", n, err)
	}
	if c, err := db.CountActiveNotifications(ctx); err != nil || c != 2 {
		t.Fatalf("active = %d, err %v", c, err)
	}
	if n, err := db.SetNotificationsArchived(ctx, nil, true, true, notifyNow); err != nil || n != 2 {
		t.Fatalf("archive all: n=%d err=%v", n, err)
	}
	if c, err := db.CountActiveNotifications(ctx); err != nil || c != 0 {
		t.Fatalf("active = %d, err %v", c, err)
	}
	archived := listNotifications(t, db, store.NotificationFilter{Archived: true})
	if len(archived) != 3 || archived[0].ArchivedAt == nil || !archived[0].ArchivedAt.Equal(notifyNow) {
		t.Fatalf("archived = %+v", archived)
	}
	if n, err := db.SetNotificationsArchived(ctx, []int64{a}, false, false, notifyNow); err != nil || n != 1 {
		t.Fatalf("restore: n=%d err=%v", n, err)
	}
	if ns := listNotifications(t, db, store.NotificationFilter{}); len(ns) != 1 || ns[0].ID != a || ns[0].ArchivedAt != nil {
		t.Fatalf("active after restore = %+v", ns)
	}
	if _, err := db.SetNotificationsArchived(ctx, nil, true, false, notifyNow); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("restore all: err = %v, want ErrInvalid", err)
	}
}

func TestPutNotifyStateWithNotification(t *testing.T) {
	db := newNotifyDB(t)
	ctx := context.Background()
	s := store.NotifyState{ProjectID: 1001, Kind: "uptime", Subject: "https://a.io", Data: `{"down_since":"x"}`}
	if err := db.PutNotifyState(ctx, s, &store.NewNotification{
		Type: store.NotifyUptimeDown, ProjectID: 1001, Subject: "https://a.io", At: notifyNow}); err != nil {
		t.Fatal(err)
	}
	states, err := db.ListNotifyStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0] != s {
		t.Fatalf("states = %+v, want %+v", states, s)
	}
	if ns := listNotifications(t, db, store.NotificationFilter{}); len(ns) != 1 || ns[0].Type != store.NotifyUptimeDown {
		t.Fatalf("notifications = %+v", ns)
	}

	s.Data = `{"down_since":"y"}`
	if err := db.PutNotifyState(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	if states, err = db.ListNotifyStates(ctx); err != nil || len(states) != 1 || states[0].Data != s.Data {
		t.Fatalf("states = %+v, err %v", states, err)
	}
	if ns := listNotifications(t, db, store.NotificationFilter{}); len(ns) != 1 {
		t.Fatalf("notifications = %d, want 1", len(ns))
	}

	// A failing insert rolls the state back with it.
	bad := store.NotifyState{ProjectID: 1001, Kind: "cert", Subject: "https://a.io", Data: "{}"}
	if err := db.PutNotifyState(ctx, bad, &store.NewNotification{Type: store.NotifyCertFailing, ProjectID: 4242, At: notifyNow}); err == nil {
		t.Fatal("notification for an unknown project: want an error")
	}
	if states, _ = db.ListNotifyStates(ctx); len(states) != 1 {
		t.Fatalf("states after a failed put = %+v", states)
	}

	if err := db.DeleteNotifyState(ctx, 1001, "uptime", "https://a.io"); err != nil {
		t.Fatal(err)
	}
	if states, _ = db.ListNotifyStates(ctx); len(states) != 0 {
		t.Fatalf("states after delete = %+v", states)
	}
}

func TestPendingDeliveriesAndSetDelivery(t *testing.T) {
	db := newNotifyDB(t)
	ctx := context.Background()
	a := addNotification(t, db, store.NotifyUptimeDown, "https://a.io", notifyNow)
	b := addNotification(t, db, store.NotifyUptimeDown, "https://b.io", notifyNow)
	c := addNotification(t, db, store.NotifyUptimeDown, "https://c.io", notifyNow)

	later := notifyNow.Add(time.Minute)
	if err := db.SetDelivery(ctx, b, store.DeliveryPending, "timeout", 1, &later); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingDeliveries(ctx, notifyNow, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := notificationIDs(pending); len(got) != 2 || got[0] != a || got[1] != c {
		t.Fatalf("pending = %v, want [%d %d]", got, a, c)
	}
	if pending, _ = db.PendingDeliveries(ctx, later, 10); len(pending) != 3 || pending[1].ID != b ||
		pending[1].Attempts != 1 || pending[1].DeliveryError != "timeout" || !pending[1].NextAttemptAt.Equal(later) {
		t.Fatalf("pending at retry time = %+v", pending)
	}
	if pending, _ = db.PendingDeliveries(ctx, later, 1); len(pending) != 1 || pending[0].ID != a {
		t.Fatalf("limit 1 = %v", notificationIDs(pending))
	}

	if err := db.SetDelivery(ctx, a, store.DeliverySent, "", 1, nil); err != nil {
		t.Fatal(err)
	}
	if pending, _ = db.PendingDeliveries(ctx, later, 10); len(pending) != 2 || pending[0].ID != b {
		t.Fatalf("pending after sent = %v", notificationIDs(pending))
	}
	if err := db.SetDelivery(ctx, 99999, store.DeliverySent, "", 1, nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
	if err := db.SetDelivery(ctx, c, "bogus", "", 1, nil); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("unknown delivery: err = %v, want ErrInvalid", err)
	}
}

func TestPurgeArchivedNotifications(t *testing.T) {
	db := newNotifyDB(t)
	ctx := context.Background()
	old := addNotification(t, db, store.NotifyUptimeDown, "https://a.io", notifyNow)
	recent := addNotification(t, db, store.NotifyUptimeDown, "https://b.io", notifyNow)
	active := addNotification(t, db, store.NotifyUptimeDown, "https://c.io", notifyNow.AddDate(0, 0, -60))
	if _, err := db.SetNotificationsArchived(ctx, []int64{old}, false, true, notifyNow.AddDate(0, 0, -31)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetNotificationsArchived(ctx, []int64{recent}, false, true, notifyNow.AddDate(0, 0, -29)); err != nil {
		t.Fatal(err)
	}
	if n, err := db.PurgeArchivedNotifications(ctx, notifyNow, 0); err != nil || n != 0 {
		t.Fatalf("days 0: n=%d err=%v", n, err)
	}
	if n, err := db.PurgeArchivedNotifications(ctx, notifyNow, 30); err != nil || n != 1 {
		t.Fatalf("purged %d, err %v", n, err)
	}
	left := append(listNotifications(t, db, store.NotificationFilter{}),
		listNotifications(t, db, store.NotificationFilter{Archived: true})...)
	if got := notificationIDs(left); len(got) != 2 || got[0] != active || got[1] != recent {
		t.Fatalf("left = %v, want [%d %d]", got, active, recent)
	}
}

func TestDeleteProjectDataNotifications(t *testing.T) {
	db := newNotifyDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1002, 'shop', '[]')`)
	addNotification(t, db, store.NotifyUptimeDown, "https://a.io", notifyNow)
	if err := db.PutNotifyState(ctx, store.NotifyState{ProjectID: 1001, Kind: "uptime", Subject: "https://a.io", Data: "{}"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.PutNotifyState(ctx, store.NotifyState{ProjectID: 1002, Kind: "uptime", Subject: "https://s.io", Data: "{}"}, nil); err != nil {
		t.Fatal(err)
	}
	execAll(t, db, `INSERT INTO form_bursts (project_id, form, window_start, pending) VALUES (1001, 'contact', '2026-11-07T12:00:00Z', 2)`)

	if err := db.DeleteProjectData(ctx, 1001, store.AuditEntry{Actor: "cli", Action: "project.delete", Subject: "1001"}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"notifications", "notify_states", "form_bursts"} {
		if c := countRows(t, db, `SELECT COUNT(*) FROM `+table+` WHERE project_id=1001`); c != 0 {
			t.Errorf("%s still has %d rows", table, c)
		}
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM notify_states WHERE project_id=1002`); c != 1 {
		t.Errorf("another project's state: %d rows, want 1", c)
	}
}
