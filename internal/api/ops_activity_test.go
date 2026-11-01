package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

type activityRow struct {
	ProjectID      int64   `json:"project_id"`
	LastEventDay   *string `json:"last_event_day"`
	NewSubmissions int     `json:"new_submissions"`
	Forms          int     `json:"forms"`
	DraftForms     int     `json:"draft_forms"`
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

// TestProjectActivityReadsTheFiles: every live project in list order with
// the newest day of its event files and the submissions its forms have not
// been read past, and how many live forms it has, drafts among them.
// Archived projects are left out.
func TestProjectActivityReadsTheFiles(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := t.Context()
	// Project 1: a view newer than the template's files, and a form with
	// two unseen submissions. Project 2: a measure in a log of its own.
	// Project 3:
	// nothing at all. Project 4: archived.
	value := 120.0
	writeHostEvents(t, h,
		store.Event{ProjectID: 1, Family: store.FamilyViews, EventName: "$page_view", Kind: "web", Path: "/x",
			TS: time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)},
		store.Event{ProjectID: 2, Family: store.FamilyMeasures, EventName: "load", Value: &value, Measure: store.MeasureTime,
			TS: time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)})
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
	// An archived submission is not new to anyone.
	seed.add("contact", "s5", map[string]string{"email": "e@x.io"}, nil)
	if _, err := h.ops.ArchiveSubmissions(ctx, "test", 1, []string{"s5"}, "ids"); err != nil {
		t.Fatal(err)
	}
	// An approved form counts as a form, not a draft.
	seed.add("signup", "s4", map[string]string{"email": "d@x.io"}, nil)
	if err := h.ops.ApproveForm(ctx, "test", 1, "signup", []string{"email"}); err != nil {
		t.Fatal(err)
	}
	if err := h.ops.MarkFormSeen(ctx, 1, "signup", time.Now()); err != nil {
		t.Fatal(err)
	}
	// An archived form's submissions are not new to anyone, nor is it a form.
	seed.add("old", "s3", map[string]string{"email": "c@x.io"}, nil)
	if err := h.ops.ArchiveForm(ctx, "test", 1, "old"); err != nil {
		t.Fatal(err)
	}

	got := getActivity(t, h)
	want := []activityRow{
		{ProjectID: 1, LastEventDay: dayPtr("2026-09-30"), NewSubmissions: 2, Forms: 2, DraftForms: 1},
		{ProjectID: 2, LastEventDay: dayPtr("2026-07-01")},
		{ProjectID: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("projects = %+v, want %+v", got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.ProjectID != w.ProjectID || g.NewSubmissions != w.NewSubmissions || g.Forms != w.Forms || g.DraftForms != w.DraftForms ||
			(g.LastEventDay == nil) != (w.LastEventDay == nil) || (g.LastEventDay != nil && *g.LastEventDay != *w.LastEventDay) {
			t.Errorf("row %d = %+v (day %v), want %+v (day %v)", i, g, deref(g.LastEventDay), w, deref(w.LastEventDay))
		}
	}

	// Reading the form clears the count.
	if err := h.ops.MarkFormSeen(ctx, 1, "contact", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := getActivity(t, h); got[0].NewSubmissions != 0 || got[0].DraftForms != 1 {
		t.Errorf("after reading, project 1 = %+v, want no new submissions and the draft still waiting", got[0])
	}
}

// TestMarkFormSeenClampsFuture: a read mark far in the future must not hide
// the submissions that arrive after it; seen_at never passes now.
func TestMarkFormSeenClampsFuture(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := t.Context()
	seed := newFormSeeder(t, h)
	seed.add("contact", "s1", map[string]string{"email": "a@x.io"}, nil)
	if err := h.ops.MarkFormSeen(ctx, 1, "contact", time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if got := getActivity(t, h); got[0].NewSubmissions != 0 {
		t.Fatalf("after reading, project 1 = %+v", got[0])
	}
	// A submission an hour from now (the seeder's clock moved on).
	seed.base = time.Now().UTC().Add(time.Hour)
	seed.add("contact", "s2", map[string]string{"email": "b@x.io"}, nil)
	if got := getActivity(t, h); got[0].NewSubmissions != 1 {
		t.Errorf("a later submission = %+v, want it counted as new", got[0])
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
	if !strings.Contains(rec.Body.String(), `{"project_id":3,"last_event_day":null,"new_submissions":0,"forms":0,"draft_forms":0}`) {
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

// scanNative records the projects each AnswerMany asked for, failing those
// of fail.
type scanNative struct {
	*native.Engine
	fail  int64
	asked []int64
}

func (n *scanNative) AnswerMany(ctx context.Context, reqs []native.Request, done func(int, native.Answered)) {
	for i, r := range reqs {
		n.asked = append(n.asked, r.Params.Project)
		if r.Params.Project == n.fail {
			done(i, native.Answered{Err: errors.New("the files are gone")})
			continue
		}
	}
	// The others are answered by the engine, with their indexes kept.
	var rest []native.Request
	var at []int
	for i, r := range reqs {
		if r.Params.Project != n.fail {
			rest, at = append(rest, r), append(at, i)
		}
	}
	n.Engine.AnswerMany(ctx, rest, func(j int, a native.Answered) { done(at[j], a) })
}

// TestProjectActivityScansLiveProjectsOnceInTheTTL: an archived project is
// not scanned, a second read within the TTL scans nothing and one past it
// scans again, and a project whose scan fails reads its newest file's day
// without failing the endpoint.
func TestProjectActivityScansLiveProjectsOnceInTheTTL(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := t.Context()
	if err := h.ops.ArchiveProject(ctx, "test", 2); err != nil {
		t.Fatal(err)
	}
	n := &scanNative{Engine: h.native.(*native.Engine)}
	h.native = n
	clock := time.Now()
	h.activity.now = func() time.Time { return clock }
	out, err := h.projectActivity(ctx, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if len(n.asked) == 0 || slices.ContainsFunc(n.asked, func(id int64) bool { return id != 1 }) {
		t.Errorf("projects scanned = %v, want project 1 only", n.asked)
	}
	if len(out.Projects) != 1 || out.Projects[0].LastEventDay == nil || *out.Projects[0].LastEventDay != "2026-08-26" {
		t.Errorf("activity = %+v", out.Projects)
	}
	asked := len(n.asked)
	if _, err := h.projectActivity(ctx, struct{}{}); err != nil || len(n.asked) != asked {
		t.Errorf("a read within the TTL scanned again: %d -> %d (%v)", asked, len(n.asked), err)
	}
	clock = clock.Add(activityTTL)
	n.fail = 1
	var logs bytes.Buffer
	h.logger = slog.New(slog.NewTextHandler(&logs, nil))
	out, err = h.projectActivity(ctx, struct{}{})
	if err != nil {
		t.Fatalf("a failing scan failed the endpoint: %v", err)
	}
	latest, _ := archive.LatestDays(h.eventsDir)
	if len(n.asked) == asked || out.Projects[0].LastEventDay == nil || *out.Projects[0].LastEventDay != latest[1] {
		t.Errorf("after the TTL: asked %d -> %d, activity %+v, want a rescan and the file name's day %s", asked, len(n.asked), out.Projects, latest[1])
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("failure not logged: %q", logs.String())
	}
	// An answer with a fallback in it is not kept: the next read scans.
	n.fail = 0
	asked = len(n.asked)
	if out, err = h.projectActivity(ctx, struct{}{}); err != nil || len(n.asked) == asked || *out.Projects[0].LastEventDay != "2026-08-26" {
		t.Errorf("read after a fallback: asked %d -> %d, %+v, %v; want a rescan", asked, len(n.asked), out.Projects, err)
	}
}

// TestProjectActivityCacheHitReadsNoFiles: within the TTL an answer costs
// the mutex, not a walk of the event files' directories.
func TestProjectActivityCacheHitReadsNoFiles(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := t.Context()
	if _, err := h.projectActivity(ctx, struct{}{}); err != nil {
		t.Fatal(err)
	}
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.eventsDir = notDir // listing it fails
	if _, err := h.projectActivity(ctx, struct{}{}); err != nil {
		t.Errorf("a read within the TTL went to the files: %v", err)
	}
}

// TestProjectActivityFallbackDayIsNeverFuture: a month file counts as its
// last day, which can lie ahead; a day read from the file names stops at
// today, with and without an engine.
func TestProjectActivityFallbackDayIsNeverFuture(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := t.Context()
	latest, err := archive.LatestDays(h.eventsDir)
	if err != nil {
		t.Fatal(err)
	}
	day, err := time.Parse(time.DateOnly, latest[1])
	if err != nil {
		t.Fatal(err)
	}
	today := day.AddDate(0, 0, -3)
	h.activity.now = func() time.Time { return today }
	want := today.Format(time.DateOnly)

	n := &scanNative{Engine: h.native.(*native.Engine), fail: 1}
	h.native = n
	h.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	out, err := h.projectActivity(ctx, struct{}{})
	if err != nil || out.Projects[0].LastEventDay == nil || *out.Projects[0].LastEventDay != want {
		t.Errorf("failed scan: %+v, %v; want %s, not the file name's %s", out.Projects, err, want, latest[1])
	}
	h.native = nil
	out, err = h.projectActivity(ctx, struct{}{})
	if err != nil || out.Projects[0].LastEventDay == nil || *out.Projects[0].LastEventDay != want {
		t.Errorf("no engine: %+v, %v; want %s, not the file name's %s", out.Projects, err, want, latest[1])
	}
}

// TestProjectActivityCancelledReadCachesNothing: a read whose context has
// ended fails without logging per project and leaves the cache empty, so
// the next read scans.
func TestProjectActivityCancelledReadCachesNothing(t *testing.T) {
	h, _ := newTestHost(t)
	n := &scanNative{Engine: h.native.(*native.Engine)}
	h.native = n
	var logs bytes.Buffer
	h.logger = slog.New(slog.NewTextHandler(&logs, nil))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := h.projectActivity(ctx, struct{}{}); err == nil {
		t.Fatal("a cancelled read answered")
	}
	if logs.Len() != 0 {
		t.Errorf("cancellation logged: %q", logs.String())
	}
	asked := len(n.asked)
	out, err := h.projectActivity(t.Context(), struct{}{})
	if err != nil || len(n.asked) == asked || out.Projects[0].LastEventDay == nil || *out.Projects[0].LastEventDay != "2026-08-26" {
		t.Errorf("read after cancel: asked %d -> %d, %+v, %v; want a scan", asked, len(n.asked), out.Projects, err)
	}
}

// A project whose real events are far older than its newest file (a
// monitored, quiet site) still reads its last real day, not null.
func TestProjectActivityStepsBackToAnOldEvent(t *testing.T) {
	h, _ := newTestHost(t)
	ms := 120.0
	writeHostEvents(t, h, store.Event{Family: store.FamilyChecks, ProjectID: 1, EventName: "uptime", Value: &ms,
		Measure: store.MeasureTime, TS: time.Date(2027, 3, 3, 9, 0, 0, 0, time.UTC), Attributes: map[string]string{"up": "1"}})
	act := getActivity(t, h)
	if len(act) == 0 || act[0].LastEventDay == nil || *act[0].LastEventDay != "2026-08-26" {
		t.Errorf("last_event_day = %+v, want 2026-08-26 seven months behind the newest file", act)
	}
}

// Within the TTL a project that has files but no day is looked at again,
// its newest file's month only, at most every 30 s.
func TestProjectActivityRechecksAProjectWithNoDay(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := t.Context()
	n := &scanNative{Engine: h.native.(*native.Engine)}
	h.native = n
	clock := time.Now()
	h.activity.now = func() time.Time { return clock }
	if _, err := h.projectActivity(ctx, struct{}{}); err != nil {
		t.Fatal(err)
	}
	// The cached answer lost project 1's day, as when it had no event yet.
	delete(h.activity.days, 1)
	asked := len(n.asked)
	clock = clock.Add(10 * time.Second)
	out, _ := h.projectActivity(ctx, struct{}{})
	if len(n.asked) != asked || out.Projects[0].LastEventDay != nil {
		t.Fatalf("rechecked within 30 s: asked %d -> %d, %+v", asked, len(n.asked), out.Projects)
	}
	clock = clock.Add(25 * time.Second)
	out, err := h.projectActivity(ctx, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if len(n.asked) != asked+1 || slices.ContainsFunc(n.asked[asked:], func(id int64) bool { return id != 1 }) {
		t.Errorf("recheck asked %v, want one scan of project 1", n.asked[asked:])
	}
	if out.Projects[0].LastEventDay == nil || *out.Projects[0].LastEventDay != "2026-08-26" {
		t.Errorf("after the recheck: %+v, want 2026-08-26", out.Projects)
	}
	// And the day is kept: the next read within the TTL asks nothing.
	asked = len(n.asked)
	clock = clock.Add(time.Minute)
	if _, err := h.projectActivity(ctx, struct{}{}); err != nil || len(n.asked) != asked {
		t.Errorf("a read after the recheck scanned again: %d -> %d (%v)", asked, len(n.asked), err)
	}
}
