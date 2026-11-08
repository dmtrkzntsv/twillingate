package api

import (
	"context"
	"strconv"

	"github.com/dmtrkzntsv/twillingate/internal/inbox"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The inbox (spec 2026-11-07-notifications-design): every notification,
// rendered as the channel gets it. Console state: archiving and restoring
// are not audited.

type listNotificationsIn struct {
	Archived  bool   `json:"archived,omitempty" jsonschema:"true lists the archived notifications instead of the active ones"`
	ProjectID int64  `json:"project_id,omitempty" jsonschema:"only this project's notifications; omit for every one"`
	Limit     int    `json:"limit,omitempty" jsonschema:"page size, 1 to 200; default 50"`
	Cursor    string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous page; omit for the first"`
}

type notificationOut struct {
	ID            int64             `json:"id"`
	Type          string            `json:"type" jsonschema:"one of the notification types (docs://twillingate, Notifications)"`
	ProjectID     int64             `json:"project_id,omitempty" jsonschema:"absent when the notification belongs to no project"`
	Project       string            `json:"project,omitempty" jsonschema:"the project's name when the notification was created"`
	Subject       string            `json:"subject" jsonschema:"what it is about: an origin, a domain or a form"`
	Title         string            `json:"title" jsonschema:"the message's title, as the channel got it"`
	Body          string            `json:"body" jsonschema:"the message's body, as the channel got it"`
	Path          string            `json:"path" jsonschema:"the console page it leads to"`
	Fields        map[string]string `json:"fields" jsonschema:"the facts the message was rendered from; never a form field value"`
	CreatedAt     string            `json:"created_at"`
	ArchivedAt    string            `json:"archived_at,omitempty" jsonschema:"absent while active"`
	Delivery      string            `json:"delivery" jsonschema:"pending, sent, failed, none (no NOTIFY_URL) or skipped"`
	DeliveryError string            `json:"delivery_error,omitempty" jsonschema:"why the last send failed"`
}

type listNotificationsOut struct {
	Notifications []notificationOut `json:"notifications"`
	Active        int               `json:"active" jsonschema:"active notifications across every project"`
	NextCursor    string            `json:"next_cursor,omitempty" jsonschema:"pass as cursor for the next page; absent on the last"`
}

type archiveNotificationsIn struct {
	IDs []int64 `json:"ids,omitempty" jsonschema:"notification ids, from list_notifications"`
	All bool    `json:"all,omitempty" jsonschema:"true archives every active notification"`
}

type restoreNotificationsIn struct {
	IDs []int64 `json:"ids" jsonschema:"archived notification ids, from list_notifications with archived: true"`
}

func toNotificationOut(n store.Notification) notificationOut {
	m := inbox.Render(n)
	fields := n.Fields
	if fields == nil {
		fields = map[string]string{}
	}
	return notificationOut{ID: n.ID, Type: n.Type, ProjectID: n.ProjectID, Project: n.Fields["project"],
		Subject: n.Subject, Title: m.Title, Body: m.Body, Path: m.Link, Fields: fields,
		CreatedAt: fmtTime(n.CreatedAt), ArchivedAt: fmtOptTime(n.ArchivedAt),
		Delivery: n.Delivery, DeliveryError: n.DeliveryError}
}

func (h *host) listNotifications(ctx context.Context, in listNotificationsIn) (listNotificationsOut, error) {
	f := store.NotificationFilter{Archived: in.Archived, ProjectID: in.ProjectID, Limit: in.Limit}
	if in.Cursor != "" {
		before, err := strconv.ParseInt(in.Cursor, 10, 64)
		if err != nil || before <= 0 {
			return listNotificationsOut{}, invalidf("cursor %q is not a next_cursor list_notifications returned", in.Cursor)
		}
		f.Before = before
	}
	ns, active, err := h.ops.ListNotifications(ctx, f)
	if err != nil {
		return listNotificationsOut{}, err
	}
	out := listNotificationsOut{Notifications: []notificationOut{}, Active: active}
	limit := manage.NotificationLimit(in.Limit)
	more := len(ns) > limit
	if more {
		ns = ns[:limit]
	}
	for _, n := range ns {
		out.Notifications = append(out.Notifications, toNotificationOut(n))
	}
	// The store was asked for one row past the page: it is there only when
	// a next page is; the last id of this one is where that starts.
	if more {
		out.NextCursor = strconv.FormatInt(ns[len(ns)-1].ID, 10)
	}
	return out, nil
}

func (h *host) archiveNotifications(ctx context.Context, in archiveNotificationsIn) (archiveSubmissionsOut, error) {
	n, err := h.ops.ArchiveNotifications(ctx, in.IDs, in.All)
	if err != nil {
		return archiveSubmissionsOut{}, err
	}
	return archiveSubmissionsOut{Archived: n}, nil
}

func (h *host) restoreNotifications(ctx context.Context, in restoreNotificationsIn) (restoreSubmissionsOut, error) {
	n, err := h.ops.RestoreNotifications(ctx, in.IDs)
	if err != nil {
		return restoreSubmissionsOut{}, err
	}
	return restoreSubmissionsOut{Restored: n}, nil
}

func (h *host) registerNotifications(r *registrar) {
	expose(r, spec{Name: "list_notifications", Annotations: ro, Method: "GET", Path: "/api/notifications",
		Description: "The inbox: every notification twillingate raised, newest first, the active ones (archived: true lists the archived ones instead), of every project or of project_id. Each has id, type (uptime_down, uptime_still_down, uptime_up, cert_failing, cert_still_failing, cert_recovered, domain_failing, domain_still_failing, domain_recovered, cert_expiring, domain_expiring, form_draft_created, form_draft_discarded, form_submission), project_id and project (absent for none), subject (the origin, domain or form), title and body (the message exactly as the NOTIFY_URL channel got it), path (the console page it leads to), fields (the facts it was rendered from; never a form field value), created_at, archived_at, and delivery (pending, sent, failed, none when no NOTIFY_URL is configured, or skipped) with delivery_error after a failed send. active counts the active ones across every project. Pages of limit (default 50, at most 200); pass next_cursor back as cursor. Reading never archives anything."},
		h.listNotifications)
	expose(r, spec{Name: "archive_notifications", Annotations: idem, Method: "POST", Path: "/api/notifications/archive",
		Description: "Archive notifications chosen by exactly one of: ids (from list_notifications), or all: true (every active one). An archived notification leaves the inbox and its active count, is reversible with restore_notifications, and is purged for good RETENTION_ARCHIVED_DAYS (default 30) after archiving. Archiving never stops a failure from notifying again: a site still down reminds after NOTIFY_REMIND_HOURS. Console state, not audited. Returns how many were archived."},
		h.archiveNotifications)
	expose(r, spec{Name: "restore_notifications", Annotations: idem, Method: "POST", Path: "/api/notifications/restore",
		Description: "Restore archived notifications by ids (from list_notifications with archived: true) into the inbox, before RETENTION_ARCHIVED_DAYS purges them. Not audited. Returns how many were restored."},
		h.restoreNotifications)
}
