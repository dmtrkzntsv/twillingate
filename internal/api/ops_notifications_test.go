package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

type listedNotification struct {
	ID         int64             `json:"id"`
	Type       string            `json:"type"`
	ProjectID  int64             `json:"project_id"`
	Project    string            `json:"project"`
	Subject    string            `json:"subject"`
	Title      string            `json:"title"`
	Body       string            `json:"body"`
	Path       string            `json:"path"`
	Fields     map[string]string `json:"fields"`
	CreatedAt  string            `json:"created_at"`
	ArchivedAt string            `json:"archived_at"`
	Delivery   string            `json:"delivery"`
}

type notificationsPage struct {
	Notifications []listedNotification `json:"notifications"`
	Active        int                  `json:"active"`
	NextCursor    string               `json:"next_cursor"`
}

// seedNotification adds an uptime_down notification for project 1.
func seedNotification(t *testing.T, h *host, origin string) int64 {
	t.Helper()
	st, ok := h.ops.St.(store.Store)
	if !ok {
		t.Fatal("host store is not a store.Store")
	}
	id, err := st.AddNotification(context.Background(), store.NewNotification{Type: store.NotifyUptimeDown,
		ProjectID: 1, Subject: origin,
		Fields: map[string]string{"since": "2026-11-07T10:00:00Z", "reason": "timeout"},
		At:     time.Date(2026, 11, 7, 10, 1, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func getNotifications(t *testing.T, r *registrar, query string) notificationsPage {
	t.Helper()
	rec := serveREST(t, r, "GET", "/api/notifications"+query, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/notifications%s = %d %s", query, rec.Code, rec.Body)
	}
	var out notificationsPage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNotificationsREST(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	first := seedNotification(t, h, "https://a.example")
	second := seedNotification(t, h, "https://b.example")

	page := getNotifications(t, r, "")
	if page.Active != 2 || len(page.Notifications) != 2 || page.NextCursor != "" {
		t.Fatalf("list = %+v", page)
	}
	n := page.Notifications[1]
	if n.ID != first || n.Type != store.NotifyUptimeDown || n.ProjectID != 1 || n.Project == "" ||
		n.Subject != "https://a.example" || n.Title != "https://a.example is down" || n.Body == "" ||
		n.Path != "/projects/1/settings/domains" || n.CreatedAt != "2026-11-07T10:01:00Z" ||
		n.ArchivedAt != "" || n.Delivery != store.DeliveryPending || n.Fields["reason"] != "timeout" {
		t.Fatalf("notification = %+v", n)
	}

	// Paging: one per page, newest first, the cursor leading to the next.
	page = getNotifications(t, r, "?limit=1&project_id=1")
	if len(page.Notifications) != 1 || page.Notifications[0].ID != second || page.NextCursor == "" {
		t.Fatalf("first page = %+v", page)
	}
	page = getNotifications(t, r, "?limit=1&cursor="+page.NextCursor)
	if len(page.Notifications) != 1 || page.Notifications[0].ID != first {
		t.Fatalf("second page = %+v", page)
	}

	// A page of exactly limit items is the last one: no cursor.
	if page = getNotifications(t, r, "?limit=2"); len(page.Notifications) != 2 || page.NextCursor != "" {
		t.Fatalf("exact last page = %+v", page)
	}

	rec := serveREST(t, r, "POST", "/api/notifications/archive", `{"all":true}`)
	if rec.Code != http.StatusOK || rec.Body.String() == "" {
		t.Fatalf("archive all = %d %s", rec.Code, rec.Body)
	}
	var archived struct {
		Archived int `json:"archived"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &archived); err != nil || archived.Archived != 2 {
		t.Fatalf("archive all = %s", rec.Body)
	}
	if page = getNotifications(t, r, ""); page.Active != 0 || len(page.Notifications) != 0 {
		t.Fatalf("after archive all = %+v", page)
	}
	page = getNotifications(t, r, "?archived=true")
	if len(page.Notifications) != 2 || page.Notifications[0].ArchivedAt == "" {
		t.Fatalf("archived list = %+v", page)
	}

	rec = serveREST(t, r, "POST", "/api/notifications/restore", `{"ids":[`+strconv.FormatInt(first, 10)+`]}`)
	var restored struct {
		Restored int `json:"restored"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &restored); rec.Code != http.StatusOK || err != nil || restored.Restored != 1 {
		t.Fatalf("restore = %d %s", rec.Code, rec.Body)
	}
	if page = getNotifications(t, r, ""); page.Active != 1 || len(page.Notifications) != 1 || page.Notifications[0].ID != first {
		t.Fatalf("after restore = %+v", page)
	}

	for _, c := range []struct {
		method, target, body string
		code                 int
	}{
		{"POST", "/api/notifications/archive", `{}`, http.StatusBadRequest},
		{"POST", "/api/notifications/archive", `{"ids":[1],"all":true}`, http.StatusBadRequest},
		{"POST", "/api/notifications/restore", `{}`, http.StatusBadRequest},
		{"GET", "/api/notifications?project_id=99", "", http.StatusNotFound},
		{"GET", "/api/notifications?cursor=abc", "", http.StatusBadRequest},
	} {
		if rec := serveREST(t, r, c.method, c.target, c.body); rec.Code != c.code {
			t.Errorf("%s %s %s = %d %s, want %d", c.method, c.target, c.body, rec.Code, rec.Body, c.code)
		}
	}
}

func TestUpdateFormNotifySubmissions(t *testing.T) {
	h, cs := newTestHost(t)
	newFormSeeder(t, h).add("contact", "s1", map[string]string{"email": "a@x.io"}, nil)
	type form struct {
		Name   string `json:"name"`
		Notify *bool  `json:"notify_submissions"`
	}
	notifyOf := func() *bool {
		t.Helper()
		out := callAs[struct {
			Forms []form `json:"forms"`
		}](t, cs, "list_forms", map[string]any{"project_id": 1})
		for _, f := range out.Forms {
			if f.Name == "contact" {
				return f.Notify
			}
		}
		t.Fatal("contact not listed")
		return nil
	}
	if n := notifyOf(); n == nil || *n {
		t.Fatalf("draft notify_submissions = %v, want false", n)
	}
	callAs[okOut](t, cs, "approve_form", map[string]any{"project_id": 1, "name": "contact", "expected_fields": []string{"email"}})
	if n := notifyOf(); n == nil || !*n {
		t.Fatalf("approved notify_submissions = %v, want true", n)
	}
	callAs[okOut](t, cs, "update_form", map[string]any{"project_id": 1, "name": "contact", "notify_submissions": false})
	if n := notifyOf(); n == nil || *n {
		t.Fatalf("after notify_submissions false = %v", n)
	}
	callAs[okOut](t, cs, "update_form", map[string]any{"project_id": 1, "name": "contact", "purpose": "Leads"})
	if n := notifyOf(); n == nil || *n {
		t.Fatalf("an update without notify_submissions changed it: %v", n)
	}
}
