package manage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func newNotificationOps(t *testing.T) (*Ops, store.Store, int64) {
	t.Helper()
	ctx := context.Background()
	st := testStore(t)
	reg := New(st, discard())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	ops.now = func() time.Time { return time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC) }
	p, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "Blog"})
	if err != nil {
		t.Fatal(err)
	}
	return ops, st, p.ID
}

func addNotifications(t *testing.T, st store.Store, projectID int64, n int) []int64 {
	t.Helper()
	var ids []int64
	for i := range n {
		id, err := st.AddNotification(context.Background(), store.NewNotification{Type: store.NotifyUptimeDown,
			ProjectID: projectID, Subject: "https://blog.example.com", Fields: map[string]string{"error": "timeout"},
			At: time.Date(2026, 11, 7, 10, i, 0, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestArchiveNotificationsSelector(t *testing.T) {
	ops, st, pid := newNotificationOps(t)
	ctx := context.Background()
	ids := addNotifications(t, st, pid, 2)
	if _, err := ops.ArchiveNotifications(ctx, nil, false); !errors.Is(err, ErrInvalid) {
		t.Errorf("neither: err = %v, want ErrInvalid", err)
	}
	if _, err := ops.ArchiveNotifications(ctx, ids, true); !errors.Is(err, ErrInvalid) {
		t.Errorf("both: err = %v, want ErrInvalid", err)
	}
	if _, err := ops.RestoreNotifications(ctx, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("restore without ids: err = %v, want ErrInvalid", err)
	}
	if n, err := ops.ArchiveNotifications(ctx, ids[:1], false); err != nil || n != 1 {
		t.Fatalf("archive by id: n=%d err=%v", n, err)
	}
	if n, err := ops.ArchiveNotifications(ctx, nil, true); err != nil || n != 1 {
		t.Fatalf("archive all: n=%d err=%v", n, err)
	}
	if n, err := ops.RestoreNotifications(ctx, ids); err != nil || n != 2 {
		t.Fatalf("restore: n=%d err=%v", n, err)
	}
}

func TestListNotifications(t *testing.T) {
	ops, st, pid := newNotificationOps(t)
	ctx := context.Background()
	ids := addNotifications(t, st, pid, 3)
	if _, _, err := ops.ListNotifications(ctx, store.NotificationFilter{ProjectID: 99}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: err = %v, want ErrNotFound", err)
	}
	ns, active, err := ops.ListNotifications(ctx, store.NotificationFilter{ProjectID: pid, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if active != 3 || len(ns) != 2 || ns[0].ID != ids[2] {
		t.Fatalf("list: active=%d %+v", active, ns)
	}
	// An unset limit is the default, one past the maximum is clamped.
	for _, c := range []struct{ limit, want int }{{0, defaultNotificationLimit}, {1000, maxNotificationLimit}} {
		f := store.NotificationFilter{Limit: c.limit}
		if got := NotificationLimit(f.Limit); got != c.want {
			t.Errorf("limit %d -> %d, want %d", c.limit, got, c.want)
		}
	}
}
