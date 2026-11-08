package manage

import (
	"context"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const (
	defaultNotificationLimit = 50
	maxNotificationLimit     = 200
)

// NotificationLimit clamps a page size to 1..200, 50 when unset.
func NotificationLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultNotificationLimit
	case limit > maxNotificationLimit:
		return maxNotificationLimit
	}
	return limit
}

// ListNotifications pages the inbox, newest first, as f selects it (the
// limit clamped to 1..200, 50 when unset), with the count of active
// notifications across every project. Console state, like MarkFormSeen:
// not audited. A ProjectID must name a project that exists.
func (o *Ops) ListNotifications(ctx context.Context, f store.NotificationFilter) ([]store.Notification, int, error) {
	if f.ProjectID != 0 {
		if err := o.requireProject(ctx, f.ProjectID); err != nil {
			return nil, 0, err
		}
	}
	f.Limit = NotificationLimit(f.Limit)
	ns, err := o.St.ListNotifications(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	active, err := o.St.CountActiveNotifications(ctx)
	if err != nil {
		return nil, 0, err
	}
	return ns, active, nil
}

// ArchiveNotifications archives the notifications with these ids, or with
// all every active one (exactly one of the two), and returns how many
// changed. Not audited.
func (o *Ops) ArchiveNotifications(ctx context.Context, ids []int64, all bool) (int, error) {
	if (len(ids) > 0) == all {
		return 0, fmt.Errorf("%w: give exactly one of ids or all", ErrInvalid)
	}
	return o.St.SetNotificationsArchived(ctx, ids, all, true, o.now())
}

// RestoreNotifications brings archived notifications with these ids back
// into the inbox and returns how many changed. Not audited.
func (o *Ops) RestoreNotifications(ctx context.Context, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, fmt.Errorf("%w: ids must name at least one notification", ErrInvalid)
	}
	return o.St.SetNotificationsArchived(ctx, ids, false, false, o.now())
}
