package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

type activityRow struct {
	ProjectID      int64   `json:"project_id"`
	LastEventDay   *string `json:"last_event_day"`
	NewSubmissions int     `json:"new_submissions"`
}

func getActivity(t *testing.T, h *host) []activityRow {
	t.Helper()
	rec := serveREST(t, newTestRegistrar(t, h), "GET", "/api/project-activity", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET project-activity = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Projects []activityRow `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body)
	}
	return out.Projects
}

func dayPtr(s string) *string { return &s }

// TestProjectActivity: every live project in list order with the newest day
// across the raw and rolled-up tables and the submissions its forms have not
// been read past. Archived projects are left out.
func TestProjectActivity(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := t.Context()
	// Project 1: a raw view newer than the template's data, and a form with
	// two unseen submissions. Project 2: only a rolled-up day. Project 3:
	// nothing at all. Project 4: archived.
	if _, err := rawExec(h.ops.St, `INSERT INTO events (id, project_id, ts, day, received_at, kind, actor_id, actor_kind, path, family, event_name)
		VALUES ('act1',1,'2026-09-30T10:00:00Z','2026-09-30','2026-09-30T10:00:00Z','web','a1','user','/x','views','$page_view')`); err != nil {
		t.Fatal(err)
	}
	if _, err := rawExec(h.ops.St, `INSERT INTO agg_product_totals (project_id, day, total_events, active_users) VALUES (2,'2026-07-01',3,2)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"quiet", "gone"} {
		if _, err := h.ops.CreateProject(ctx, "test", manage.ProjectSpec{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.ops.ArchiveProject(ctx, "test", 4); err != nil {
		t.Fatal(err)
	}
	seed := newFormSeeder(t, h)
	seed.add("contact", "s1", map[string]string{"email": "a@x.io"}, nil)
	seed.add("contact", "s2", map[string]string{"email": "b@x.io"}, nil)
	// An archived form's submissions are not new to anyone.
	seed.add("old", "s3", map[string]string{"email": "c@x.io"}, nil)
	if err := h.ops.ArchiveForm(ctx, "test", 1, "old"); err != nil {
		t.Fatal(err)
	}

	got := getActivity(t, h)
	want := []activityRow{
		{ProjectID: 1, LastEventDay: dayPtr("2026-09-30"), NewSubmissions: 2},
		{ProjectID: 2, LastEventDay: dayPtr("2026-07-01")},
		{ProjectID: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("projects = %+v, want %+v", got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.ProjectID != w.ProjectID || g.NewSubmissions != w.NewSubmissions ||
			(g.LastEventDay == nil) != (w.LastEventDay == nil) || (g.LastEventDay != nil && *g.LastEventDay != *w.LastEventDay) {
			t.Errorf("row %d = %+v (day %v), want %+v (day %v)", i, g, deref(g.LastEventDay), w, deref(w.LastEventDay))
		}
	}

	// Reading the form clears the count.
	if err := h.ops.MarkFormSeen(ctx, 1, "contact", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := getActivity(t, h); got[0].NewSubmissions != 0 {
		t.Errorf("after reading, project 1 = %+v", got[0])
	}
}

func deref(s *string) string {
	if s == nil {
		return "null"
	}
	return *s
}

// TestProjectActivityJSONNull: a project with no data answers
// "last_event_day": null, not a missing key or an empty string.
func TestProjectActivityJSONNull(t *testing.T) {
	h, _ := newTestHost(t)
	if _, err := h.ops.CreateProject(t.Context(), "test", manage.ProjectSpec{Name: "empty"}); err != nil {
		t.Fatal(err)
	}
	rec := serveREST(t, newTestRegistrar(t, h), "GET", "/api/project-activity", "")
	if !strings.Contains(rec.Body.String(), `{"project_id":3,"last_event_day":null,"new_submissions":0}`) {
		t.Errorf("body = %s", rec.Body)
	}
}

// TestProjectActivityNotMCP: the badges' numbers and the read mark are the
// console's own, not tools.
func TestProjectActivityNotMCP(t *testing.T) {
	h, cs := newTestHost(t)
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name == "project_activity" || tool.Name == "mark_form_seen" {
			t.Errorf("MCP lists %s; it is REST-only", tool.Name)
		}
	}
	routes := map[string]bool{}
	for _, s := range newTestRegistrar(t, h).specs {
		if s.Name == "project_activity" || s.Name == "mark_form_seen" {
			routes[s.Name] = s.RESTOnly
		}
	}
	if len(routes) != 2 || !routes["project_activity"] || !routes["mark_form_seen"] {
		t.Errorf("REST-only specs = %v", routes)
	}
}
