package reporting

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/docs"
	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// systemPresets is every fixed preset a system dashboard's range
// switcher offers (Presets minus custom), in switcher order.
var systemPresets = []string{"today", "yesterday", "7d", "30d", "90d"}

// presetDates resolves preset to the from/to dates the UI sends for it
// (web/src/lib/ranges.ts), with today the UTC calendar date.
func presetDates(preset string, today civil.Date) (from, to string) {
	switch preset {
	case "today":
		return today.String(), today.String()
	case "yesterday":
		y := today.AddDays(-1).String()
		return y, y
	case "7d":
		return today.AddDays(-7).String(), today.AddDays(-1).String()
	case "30d":
		return today.AddDays(-30).String(), today.AddDays(-1).String()
	case "90d":
		return today.AddDays(-90).String(), today.AddDays(-1).String()
	}
	panic("presetDates: unknown preset " + preset)
}

// mayBeEmpty names the system widgets ("dashboard/widget") allowed to
// return no rows for 7d on the seeded project, each with the reason. It
// is empty today: every system widget has rows for 7d once
// systemEvents are in the files. A widget added here needs a reason that holds
// for any project, not only for this seed.
var mayBeEmpty = map[string]string{}

// TestSystemDashboards is the parity-before-release gate (spec Tests,
// "system dashboards"): on a database migrated to latest and synced with
// this release's embedded system definition, every system widget runs
// for every preset on a project whose events are in the files (day files
// and derived tables for the past, today's open log), its rows fit its
// component, and it has rows for 7d.
func TestSystemDashboards(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)
	svc, pid, today := f.svc, f.project, f.today

	ds, err := svc.Dashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, d := range ds.Dashboards {
		if d.Owner == store.OwnerSystem {
			titles = append(titles, fmt.Sprintf("%d %s %s", d.ID, d.Title, d.Range))
		}
	}
	want := "1 Views 7d, 2 Product 7d, 3 Users 7d, 4 Groups 7d, 5 Retention 90d, 6 Web Vitals 30d, 7 Measures 7d"
	if got := strings.Join(titles, ", "); got != want {
		t.Fatalf("system dashboards = %s, want %s", got, want)
	}

	seen := map[string]bool{}
	for _, d := range ds.Dashboards {
		if d.Owner != store.OwnerSystem {
			continue
		}
		detail, err := svc.Dashboard(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(detail.Widgets) == 0 {
			t.Errorf("dashboard %s has no widgets", d.Title)
		}
		for _, w := range detail.Widgets {
			key := strings.ToLower(d.Title) + "/" + w.Name
			seen[key] = true
			t.Run(key, func(t *testing.T) {
				for _, preset := range systemPresets {
					from, to := presetDates(preset, today)
					got, err := svc.WidgetData(ctx, DataRequest{WidgetID: w.ID, ProjectID: pid, From: from, To: to})
					if err != nil {
						t.Fatalf("%s: %v", preset, err)
					}
					if got.Removed {
						t.Fatalf("%s: removed", preset)
					}
					res, ok := got.Data.(readsql.Result)
					if !ok {
						continue // md: nothing to count
					}
					if preset != "7d" {
						continue
					}
					if len(res.Rows) == 0 {
						if _, ok := mayBeEmpty[key]; !ok {
							t.Errorf("7d: no rows (columns %s)", strings.Join(res.Columns, ", "))
						}
						continue
					}
					if w.Component != nil && *w.Component == "stat" {
						if v := column(t, res, 0, "value"); v == "" {
							t.Errorf("7d: stat value is empty")
						}
					}
				}
			})
		}
	}
	for key := range mayBeEmpty {
		if !seen[key] {
			t.Errorf("mayBeEmpty names %s, which is not a system widget", key)
		}
	}
}

// TestSystemDashboardGroups is D16: Views (id 1) names no group of its
// own, so it is group 1; every other system dashboard (ids 2-7) names
// "group": 1 in its dashboard.json, so the system dashboards are one
// sidebar entry. This release's
// embedded system definition (loaded by the real Migrate, not a test
// fixture) must give each dashboard exactly that GroupID.
func TestSystemDashboardGroups(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)

	ds, err := f.svc.Dashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]int64{}
	for _, d := range ds.Dashboards {
		if d.Owner == store.OwnerSystem {
			got[d.ID] = d.GroupID
		}
	}
	want := map[int64]int64{1: 1, 2: 1, 3: 1, 4: 1, 5: 1, 6: 1, 7: 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("system dashboard GroupIDs = %v, want %v", got, want)
	}
}

// TestSystemGroupNames: Views (id 1) founds the Reports group and gives
// it its name (D6); the seven system dashboards are one group, so all of
// them read it.
func TestSystemGroupNames(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)

	ds, err := f.svc.Dashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]string{}
	for _, d := range ds.Dashboards {
		if d.Owner == store.OwnerSystem {
			got[d.ID] = d.GroupTitle
		}
	}
	want := map[int64]string{1: "Reports", 2: "Reports", 3: "Reports", 4: "Reports", 5: "Reports", 6: "Reports", 7: "Reports"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("system dashboard group titles = %v, want %v", got, want)
	}
}

// systemFixture is a store migrated to latest and synced with this
// release's embedded system definition, a Service over it with the cache
// off, and the event files of two projects: project, with systemEvents'
// 100 days, and neighbour, with views of its own on the same days, so a
// widget that reads past its project reads wrong numbers.
type systemFixture struct {
	svc                *Service
	st                 store.Store
	files              *testFiles
	project, neighbour int64
	today              civil.Date
}

func newSystemFixture(t *testing.T) systemFixture {
	t.Helper()
	ctx := context.Background()
	st, _ := newTestStore(t)
	// A generous timeout: this runs under -race in make test.
	files := &testFiles{Files: archivetest.New(t), st: st, timeout: 60 * time.Second, maxRows: 1000}
	if err := Migrate(ctx, st, files); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	today := civil.Today(time.Now().UTC())
	pid := mustCreateProject(t, st, "demo")
	other := mustCreateProject(t, st, "neighbour")
	archivetest.CopyDir(t, systemTemplate(t, pid, other, today), files.Dir)
	return systemFixture{svc: New(st, files, Options{Native: native.New(native.Options{Dir: files.Dir})}), st: st, files: files, project: pid, neighbour: other, today: today}
}

// systemFiles is the raw/ directory every system fixture starts from,
// built once per test binary and day by systemTemplate, and removed by
// TestMain.
var systemFiles struct {
	sync.Mutex
	dir                string
	project, neighbour int64
	today              civil.Date
}

// systemTemplate returns a directory holding systemEvents for project and
// neighbourEvents for neighbour as of today, after the daily pass: every
// day before today a day (or month) file with its sessions, identities
// derived, today an open log. Writing and passing 100 days takes seconds,
// so the fixtures copy it rather than each running the pass.
func systemTemplate(t *testing.T, project, neighbour int64, today civil.Date) string {
	t.Helper()
	systemFiles.Lock()
	defer systemFiles.Unlock()
	if systemFiles.dir != "" && systemFiles.today == today {
		if systemFiles.project != project || systemFiles.neighbour != neighbour {
			t.Fatalf("projects %d and %d; the template holds %d and %d", project, neighbour, systemFiles.project, systemFiles.neighbour)
		}
		return systemFiles.dir
	}
	if systemFiles.dir != "" {
		os.RemoveAll(systemFiles.dir)
		systemFiles.dir = ""
	}
	f := archivetest.New(t)
	f.Write(t, append(systemEvents(project, today), neighbourEvents(neighbour, today)...)...)
	f.Maintain(t, today.String())
	dir, err := os.MkdirTemp("", "reporting-system-")
	if err != nil {
		t.Fatal(err)
	}
	archivetest.CopyDir(t, f.Dir, dir)
	systemFiles.dir, systemFiles.project, systemFiles.neighbour, systemFiles.today = dir, project, neighbour, today
	return dir
}

// removeSystemTemplate removes systemTemplate's directory, if one was built.
func removeSystemTemplate() {
	if systemFiles.dir != "" {
		os.RemoveAll(systemFiles.dir)
	}
}

// rows runs q over the fixture's project in the files, every cell as the
// widgets show it: a reference to hold a widget to.
func (f systemFixture) rows(t *testing.T, q string) [][]string {
	t.Helper()
	res, err := f.files.Reader.QueryScoped(context.Background(),
		archive.Scoped{SQL: enginesql.Text(q), Scope: true, Project: f.project}, archive.Rules{})
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	out := make([][]string, len(res.Rows))
	for i, r := range res.Rows {
		out[i] = make([]string, len(r))
		for j, c := range r {
			if c != nil {
				out[i][j] = fmt.Sprint(c)
			}
		}
	}
	return out
}

// count is rows for a q that returns one number.
func (f systemFixture) count(t *testing.T, q string) int {
	t.Helper()
	n, err := strconv.Atoi(f.rows(t, q)[0][0])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// column returns row i's value in res's column name, failing the test
// when res has no such column or row.
func column(t *testing.T, res readsql.Result, i int, name string) string {
	t.Helper()
	for c, col := range res.Columns {
		if col == name {
			if i >= len(res.Rows) {
				t.Fatalf("no row %d (%d rows)", i, len(res.Rows))
			}
			return res.Rows[i][c]
		}
	}
	t.Fatalf("no column %s in %s", name, strings.Join(res.Columns, ", "))
	return ""
}

// TestRetentionMilestonesMatchCurve checks the two ways Retention reads
// the same curve agree: each milestone (D7, D30, D45) equals the curve's
// y at that offset, or is empty where the curve has not reached it.
func TestRetentionMilestonesMatchCurve(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)
	d, err := f.svc.Dashboard(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, w := range d.Widgets {
		ids[w.Name] = w.ID
	}
	load := func(name, preset string) readsql.Result {
		t.Helper()
		id, ok := ids[name]
		if !ok {
			t.Fatalf("retention has no widget %s", name)
		}
		from, to := presetDates(preset, f.today)
		got, err := f.svc.WidgetData(ctx, DataRequest{WidgetID: id, ProjectID: f.project, From: from, To: to})
		if err != nil {
			t.Fatal(err)
		}
		return got.Data.(readsql.Result)
	}
	for _, pair := range [][2]string{{"signed-in-milestones", "signed-in-retention"}, {"install-milestones", "install-retention"}} {
		for _, preset := range systemPresets {
			milestones, curve := load(pair[0], preset), load(pair[1], preset)
			ys := map[string]string{}
			for i := range curve.Rows {
				ys[column(t, curve, i, "x")] = column(t, curve, i, "y")
			}
			for _, k := range []string{"7", "30", "45"} {
				if got, want := column(t, milestones, 0, "D"+k), ys[k]; got != want {
					t.Errorf("%s %s: D%s = %q, curve y at %s = %q", pair[0], preset, k, got, k, want)
				}
			}
			if preset == "90d" && ys["45"] == "" {
				t.Errorf("%s 90d: curve never reaches 45; the seed does not exercise the milestones", pair[1])
			}
		}
	}
}

// TestRetentionKindIsSticky: an actor first seen as an install that later
// signs in under the same actor_id is cohorted on its first day, counts
// under install until the day it is first a user and under user from then
// on, even on a later day it is an install again: the rule SQLite's
// retention rollup applied before raw-first PR 6.
func TestRetentionKindIsSticky(t *testing.T) {
	ctx := context.Background()
	st, files := newTestStoreAndFiles(t, 1000)
	if err := Migrate(ctx, st, files); err != nil {
		t.Fatal(err)
	}
	pid := mustCreateProject(t, st, "sticky")
	today := civil.Today(time.Now().UTC())
	cohort := today.AddDays(-10)
	var evs []store.Event
	view := func(actor, kind string, off int) {
		e := event(t, fmt.Sprintf("%s-%d-%s", actor, off, kind), pid, store.FamilyViews, "$page_view", cohort.AddDays(off).String())
		e.ActorID, e.ActorKind = actor, kind
		evs = append(evs, e)
	}
	view("x", "connection", -1) // before x is cohorted: no row
	view("x", "install", 0)
	view("x", "install", 2)
	view("x", "user", 5) // a user from here on
	view("x", "install", 7)
	view("u", "user", 0)
	view("u", "user", 2)
	view("u", "user", 5)
	view("i", "install", 0)
	view("i", "install", 5)
	files.Write(t, evs...)
	svc := New(st, files, Options{Native: native.New(native.Options{Dir: files.Dir})})
	got, err := svc.WidgetData(ctx, DataRequest{WidgetID: systemWidgetOf(t, svc, 5, "cohorts"), ProjectID: pid,
		From: cohort.AddDays(-3).String(), To: cohort.String()})
	if err != nil {
		t.Fatal(err)
	}
	res := got.Data.(readsql.Result)
	var rows [][]string
	for _, r := range res.Rows {
		rows = append(rows, r[:5])
	}
	// x is under install at offset 2 and under user at 5 and 7.
	for _, w := range [][]string{{"install", "2", "1"}, {"install", "5", "1"}, {"user", "5", "2"}, {"user", "7", "1"}} {
		found := false
		for _, r := range rows {
			if r[1] == w[0] && r[2] == w[1] {
				found = true
				if r[4] != w[2] {
					t.Errorf("%s at offset %s: %s returned, want %s", w[0], w[1], r[4], w[2])
				}
			}
		}
		if !found {
			t.Errorf("no %s row at offset %s in %v", w[0], w[1], rows)
		}
	}
}

// systemWidgetOf finds system dashboard id's widget name on svc.
func systemWidgetOf(t *testing.T, svc *Service, id int64, name string) int64 {
	t.Helper()
	d, err := svc.Dashboard(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range d.Widgets {
		if w.Name == name {
			return w.ID
		}
	}
	t.Fatalf("dashboard %d has no widget %s", id, name)
	return 0
}

// attributeValuesWidget finds the Product dashboard's attribute-values
// widget, failing the test when it is missing.
func attributeValuesWidget(t *testing.T, f systemFixture) int64 {
	t.Helper()
	return systemWidget(t, f, 2, "attribute-values")
}

// systemWidget finds system dashboard id's widget name, failing the test
// when it is missing.
func systemWidget(t *testing.T, f systemFixture, id int64, name string) int64 {
	t.Helper()
	d, err := f.svc.Dashboard(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range d.Widgets {
		if w.Name == name {
			return w.ID
		}
	}
	t.Fatalf("dashboard %d has no widget %s", id, name)
	return 0
}

// TestAttributeValuesCountEachValueOnce: "Attribute values" is a table
// with one row per (attribute, value) of the range's product events,
// however many days carried it. For every preset it holds exactly the
// (attribute, value) pairs the files hold, a quiet value among a burst of
// others included. And its figures are exact for the range: Count the
// events carrying the value, Users and Groups distinct over the whole
// range, not per day or event, with no group a 0.
func TestAttributeValuesCountEachValueOnce(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)
	id := attributeValuesWidget(t, f)
	// One day far busier than the rest: 120 values of their own, as a
	// launch or a backfill leaves them, in a log of that past day.
	var extra []store.Event
	for i := 0; i < 120; i++ {
		e := event(t, fmt.Sprintf("burst-%d", i), f.project, store.FamilyProduct, "click", f.today.AddDays(-10).String())
		e.Attributes = map[string]string{"ref": fmt.Sprintf("r%03d", i)}
		extra = append(extra, e)
	}
	// coupon, a key only these events carry, on three days in the 7d range:
	// SAVE to u1 twice on one day and once on another in group g1, and to
	// u2 with no group, two events of different names; FREE to u3 in g2
	// and u4 in g3; SPRING once with no group. Before the range and in the
	// neighbour project, SAVE again from others, which must not count.
	coupon := func(id, actor, group, name string, back int, value string, project int64) {
		e := event(t, id, project, store.FamilyProduct, name, f.today.AddDays(-back).String())
		e.ActorID, e.GroupID, e.Attributes = actor, group, map[string]string{"coupon": value}
		extra = append(extra, e)
	}
	coupon("c1", "u1", "g1", "checkout", 1, "SAVE", f.project)
	coupon("c2", "u1", "g1", "checkout", 1, "SAVE", f.project)
	coupon("c3", "u1", "g1", "checkout", 3, "SAVE", f.project)
	coupon("c4", "u2", "", "signup", 2, "SAVE", f.project)
	coupon("c5", "u3", "g2", "checkout", 1, "FREE", f.project)
	coupon("c6", "u4", "g3", "checkout", 4, "FREE", f.project)
	coupon("c7", "u1", "", "checkout", 2, "SPRING", f.project)
	coupon("c8", "u9", "g9", "checkout", 20, "SAVE", f.project)
	coupon("c9", "u8", "g8", "checkout", 1, "SAVE", f.neighbour)
	f.files.Write(t, extra...)

	load := func(preset string) readsql.Result {
		t.Helper()
		from, to := presetDates(preset, f.today)
		got, err := f.svc.WidgetData(ctx, DataRequest{WidgetID: id, ProjectID: f.project, From: from, To: to})
		if err != nil {
			t.Fatal(err)
		}
		res := got.Data.(readsql.Result)
		if res.Truncated {
			t.Fatalf("%s: truncated at %d rows; the seed is meant to fit under the cap", preset, len(res.Rows))
		}
		return res
	}
	res := load("7d")
	if want := []string{"Attribute", "Value", "Count", "Users", "Groups"}; !slices.Equal(res.Columns, want) {
		t.Fatalf("columns %v, want %v", res.Columns, want)
	}
	var coupons [][]string
	for i, r := range res.Rows {
		if column(t, res, i, "Attribute") == "coupon" {
			coupons = append(coupons, r)
		}
	}
	want := [][]string{
		{"coupon", "SAVE", "4", "2", "1"},
		{"coupon", "FREE", "2", "2", "2"},
		{"coupon", "SPRING", "1", "1", "0"},
	}
	if !slices.EqualFunc(coupons, want, slices.Equal) {
		t.Errorf("coupon over 7d: rows %v, want %v", coupons, want)
	}

	for _, preset := range systemPresets {
		from, to := presetDates(preset, f.today)
		res := load(preset)
		shown := map[string]bool{}
		for i := range res.Rows {
			pair := column(t, res, i, "Attribute") + "=" + column(t, res, i, "Value")
			if shown[pair] {
				t.Errorf("%s: %s appears twice", preset, pair)
			}
			shown[pair] = true
		}
		pairs := f.rows(t, fmt.Sprintf(`SELECT DISTINCT substr(attr_key, 6), attr_value FROM (UNPIVOT (
			SELECT COLUMNS('^attr_') FROM product WHERE day BETWEEN DATE '%s' AND DATE '%s'
		) ON COLUMNS('^attr_') INTO NAME attr_key VALUE attr_value)`, from, to))
		if len(res.Rows) != len(pairs) {
			t.Errorf("%s: %d rows, the files hold %d pairs", preset, len(res.Rows), len(pairs))
		}
		if preset == "30d" && len(pairs) < 120 {
			t.Errorf("30d: the files hold %d attribute values; the seed does not exercise the burst", len(pairs))
		}
		for _, r := range pairs {
			if !shown[r[0]+"="+r[1]] {
				t.Errorf("%s: %s=%s is in the files but the widget does not show it", preset, r[0], r[1])
			}
		}
	}
}

// TestAttributeValuesWithoutAttributes: on files where no event has ever
// carried an attribute, there is no attr_* column to unpivot. Both the
// system widget and docs/reporting.md's remote-table example (Filtering
// and paging a table) still run, with no rows.
func TestAttributeValuesWithoutAttributes(t *testing.T) {
	ctx := context.Background()
	st, files := newTestStoreAndFiles(t, 1000)
	if err := Migrate(ctx, st, files); err != nil {
		t.Fatal(err)
	}
	pid := mustCreateProject(t, st, "plain")
	today := civil.Today(time.Now().UTC())
	files.Write(t, event(t, "p1", pid, store.FamilyProduct, "signup", today.AddDays(-1).String()))
	svc := New(st, files, Options{Native: native.New(native.Options{Dir: files.Dir})})
	from, to := presetDates("7d", today)

	d, err := svc.Dashboard(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	for _, w := range d.Widgets {
		if w.Name == "attribute-values" {
			id = w.ID
		}
	}
	got, err := svc.WidgetData(ctx, DataRequest{WidgetID: id, ProjectID: pid, From: from, To: to})
	if err != nil {
		t.Fatalf("attribute-values: %v", err)
	}
	if res := got.Data.(readsql.Result); len(res.Rows) != 0 {
		t.Errorf("attribute-values: rows %v; want none", res.Rows)
	}

	example := regexp.MustCompile(`"source": \{"type": "sql", "content": ("(?:[^"\\]|\\.)*")\}\}`).
		FindStringSubmatch(docSection(t, docs.Reporting, "### Filtering and paging a table"))
	if example == nil {
		t.Fatal("docs/reporting.md's Filtering and paging a table has no add_widget example")
	}
	var q string
	if err := json.Unmarshal([]byte(example[1]), &q); err != nil {
		t.Fatal(err)
	}
	p, err := svc.Preview(ctx, PreviewIn{Component: "table", Props: json.RawMessage(`{"mode":"remote"}`),
		Source: Source{Type: "sql", Content: q}, ProjectID: pid, From: from, To: to})
	if err != nil {
		t.Fatalf("the docs' remote-table example: %v", err)
	}
	if len(p.Rows) != 0 || p.Problem != "" {
		t.Errorf("the docs' remote-table example: rows %v, problem %q; want none", p.Rows, p.Problem)
	}
}

// docSection is the part of doc from the line heading through the next
// heading of the same or a higher level.
func docSection(t *testing.T, doc, heading string) string {
	t.Helper()
	i := strings.Index(doc, "\n"+heading+"\n")
	if i < 0 {
		t.Fatalf("no %q heading", heading)
	}
	rest := doc[i+1+len(heading):]
	level := strings.Repeat("#", strings.Index(heading, " "))
	for _, h := range regexp.MustCompile(`(?m)^#+ `).FindAllStringIndex(rest, -1) {
		if h[1]-1-h[0] <= len(level) {
			return rest[:h[0]]
		}
	}
	return rest
}

// TestTopEventsKeepsTheBusiest: "Top events" draws one line for each of
// the range's five busiest events, however many an app sends, so the
// legend stays readable; a quieter event is left to the Events table.
func TestTopEventsKeepsTheBusiest(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)
	id := systemWidget(t, f, 2, "events-by-name")
	// Twelve more events on one past day: e00 the busiest of all, down to
	// e11 below the seed's signup (5 a day) over a week.
	var extra []store.Event
	for i := 0; i < 12; i++ {
		for n := 0; n < 400-45*i; n++ {
			extra = append(extra, event(t, fmt.Sprintf("e%02d-%d", i, n), f.project, store.FamilyProduct, fmt.Sprintf("e%02d", i), f.today.AddDays(-5).String()))
		}
	}
	f.files.Write(t, extra...)
	from, to := presetDates("7d", f.today)
	got, err := f.svc.WidgetData(ctx, DataRequest{WidgetID: id, ProjectID: f.project, From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	res := got.Data.(readsql.Result)
	series := map[string]bool{}
	for i := range res.Rows {
		series[column(t, res, i, "series")] = true
	}
	// e00-e04 (400 down to 220) are the five busiest; e05 (175) is next.
	want := map[string]bool{}
	for i := 0; i < 5; i++ {
		want[fmt.Sprintf("e%02d", i)] = true
	}
	if !reflect.DeepEqual(series, want) {
		t.Errorf("series = %v, want %v", series, want)
	}
}

// TestBrowsersAndSystemsByName: the Browsers and Operating systems pies
// name each browser or system without its version, summing its versions'
// visitors, and keep five slices: up to five names, or past five the four
// busiest and Other.
func TestBrowsersAndSystemsByName(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)
	day := f.today.AddDays(-5).String()
	// A second chrome version, and five more browsers, of which opera,
	// brave and vivaldi fall into Other: one view per visitor, with no
	// operating system.
	var extra []store.Event
	for i, b := range [][2]string{{"chrome", "125"}, {"firefox", "1"}, {"edge", "1"}, {"opera", "1"}, {"brave", "1"}, {"vivaldi", "1"}} {
		for a := 0; a < []int{3, 5, 4, 3, 2, 1}[i]; a++ {
			e := event(t, fmt.Sprintf("%s-%d", b[0], a), f.project, store.FamilyViews, "$page_view", day)
			e.ActorID, e.Browser, e.BrowserVersion = fmt.Sprintf("%s-actor-%d", b[0], a), b[0], b[1]
			extra = append(extra, e)
		}
	}
	f.files.Write(t, extra...)
	from, to := presetDates("7d", f.today)
	for _, c := range []struct {
		widget, col, version string
		want                 []string
	}{
		{"browsers", "browser", "browser_version", []string{"chrome", "safari", "firefox", "edge", "Other"}},
		{"operating-systems", "os", "os_version", []string{"macos", "windows", "ios"}},
	} {
		got, err := f.svc.WidgetData(ctx, DataRequest{WidgetID: systemWidget(t, f, 1, c.widget), ProjectID: f.project, From: from, To: to})
		if err != nil {
			t.Fatal(err)
		}
		res := got.Data.(readsql.Result)
		var labels []string
		total, prev := 0, 0
		for i := range res.Rows {
			label := column(t, res, i, "label")
			labels = append(labels, label)
			n, err := strconv.Atoi(column(t, res, i, "value"))
			if err != nil {
				t.Fatal(err)
			}
			if label != "Other" && i > 0 && n > prev {
				t.Errorf("%s: %s (%d) follows %d; slices are not busiest first", c.widget, label, n, prev)
			}
			if label == "Other" && i != len(res.Rows)-1 {
				t.Errorf("%s: Other is slice %d of %d, not the last", c.widget, i+1, len(res.Rows))
			}
			total, prev = total+n, n
		}
		slices.Sort(labels)
		slices.Sort(c.want)
		if !reflect.DeepEqual(labels, c.want) {
			t.Errorf("%s: labels %v, want %v", c.widget, labels, c.want)
		}
		// Visitors distinct per day and version, as the Views dashboard
		// counts every breakdown.
		want := f.count(t, fmt.Sprintf(`SELECT sum(n) FROM (SELECT count(DISTINCT actor_id) AS n FROM views
			WHERE day BETWEEN DATE '%s' AND DATE '%s' AND %s <> '' GROUP BY day, %[3]s, %s)`, from, to, c.col, c.version))
		if total != want {
			t.Errorf("%s: slices add up to %d visitors, the files hold %d", c.widget, total, want)
		}
	}
}

// TestVitalsStatsBlankWithoutSamples: collection is opt-in, so a
// project without vitals is the common case, and it must not read 0 — a
// perfect score. Each vital stat answers one row whose value is blank
// (the stat component draws a dash) for the neighbour (no measures), and
// a value for the seeded project; none carries a previous period (stat
// reads a rise as good, and a vital is better lower).
func TestVitalsStatsBlankWithoutSamples(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)
	d, err := f.svc.Dashboard(ctx, 6)
	if err != nil {
		t.Fatal(err)
	}
	from, to := presetDates("7d", f.today)
	stats := 0
	for _, w := range d.Widgets {
		if w.Component == nil || *w.Component != "stat" {
			continue
		}
		stats++
		for _, pid := range []int64{f.project, f.neighbour} {
			got, err := f.svc.WidgetData(ctx, DataRequest{WidgetID: w.ID, ProjectID: pid, From: from, To: to})
			if err != nil {
				t.Fatalf("%s: %v", w.Name, err)
			}
			res := got.Data.(readsql.Result)
			if strings.Join(res.Columns, ",") != "value" {
				t.Errorf("%s: columns %v, want only value", w.Name, res.Columns)
			}
			if len(res.Rows) != 1 || (res.Rows[0][0] == "") != (pid != f.project) {
				t.Errorf("%s project %d: rows %v, want one, blank only without samples", w.Name, pid, res.Rows)
			}
		}
	}
	if stats != 5 {
		t.Errorf("Web Vitals has %d stats, want 5", stats)
	}
}

// neighbourEvents gives projectID views on the same 100 days as
// systemEvents' project: on the last three, five actors with three views
// each, the last 50 minutes after the second (two sessions without an
// id); on every older day, three actors with two views.
func neighbourEvents(projectID int64, today civil.Date) []store.Event {
	var evs []store.Event
	for back := 0; back < 100; back++ {
		day := today.AddDays(-back)
		actors, minutes := 3, []int{0, 10}
		if back < 3 {
			actors, minutes = 5, []int{0, 10, 60}
		}
		for i := 0; i < actors; i++ {
			for v, minute := range minutes {
				ts := day.Time().Add(time.Duration(2+i)*time.Hour + time.Duration(minute)*time.Minute)
				evs = append(evs, store.Event{ID: fmt.Sprintf("n-%d-%d-%d", back, i, v), ProjectID: projectID,
					Family: store.FamilyViews, EventName: "$page_view", TS: ts, ReceivedAt: ts,
					Kind: "web", ActorID: fmt.Sprintf("n%d", i), ActorKind: "user", Path: "/"})
			}
		}
	}
	return evs
}

// systemEvents gives projectID 100 days of events ending today, the way a
// running install's files hold them. The last three days (today,
// yesterday and the day before) carry the detailed scenarios: sessions,
// bounces, both kinds, consent states, attributes and measures. Every
// older day carries the history the dashboards draw over a range: views
// across every breakdown, users and groups, product events, measures, and
// retention cohorts of both actor kinds, followed up to 45 days.
func systemEvents(projectID int64, today civil.Date) []store.Event {
	var evs []store.Event
	add := func(e store.Event) {
		e.ProjectID, e.ReceivedAt = projectID, e.TS
		evs = append(evs, e)
	}
	at := func(day civil.Date, h, m, s int) time.Time {
		return day.Time().Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second)
	}
	consents := []store.Consent{store.ConsentGiven, store.ConsentNone, store.ConsentUnknown}
	userName := func(id string) string {
		if strings.HasPrefix(id, "u") && len(id) == 2 {
			return "User " + id[1:]
		}
		return ""
	}
	groupName := func(id string) string {
		if strings.HasPrefix(id, "g") && len(id) == 2 {
			return "Team " + id[1:]
		}
		return ""
	}

	// The last three days. Twelve actors a day; a third are an iOS app,
	// the rest web browsers. Each sends two views a few minutes apart (one
	// session, not a bounce) — every fourth only one (a bounce) — and two
	// product events. Sessionization gets both of its rules exercised:
	// actors 2, 6 and 10 send client session ids, a third view 42 minutes
	// after the second in the same session (an id wins over the 30-minute
	// gap) and a fourth 3 minutes later in a new one (a new id splits even
	// without a gap); actors 4 and 8 send no id and a second view 40
	// minutes after the first (two sessions, both bounces).
	for back := 0; back < 3; back++ {
		day := today.AddDays(-back)
		for i := 0; i < 12; i++ {
			actor := fmt.Sprintf("a%d-%d", back, i)
			userID, groupID := "", ""
			if i < 8 {
				userID = fmt.Sprintf("u%d", i%5)
			}
			if i < 9 {
				groupID = fmt.Sprintf("g%d", i%3)
			}
			base := store.Event{Kind: "web", ActorID: actor, ActorKind: "user",
				UserID: userID, UserName: userName(userID), GroupID: groupID, GroupName: groupName(groupID),
				Platform: "web", OS: "macos", OSVersion: "14", Browser: "chrome", BrowserVersion: "126",
				BrowserLocale: "en-US", Device: "desktop", Country: []string{"US", "DE", "FR", "GB"}[i%4],
				Consent: consents[i%3]}
			view := base
			view.EventName, view.Host, view.Path = "$page_view", "example.com", []string{"/", "/pricing", "/docs"}[i%3]
			view.ReferrerSource = []string{"", "google", "twitter"}[i%3]
			view.DisplayWidth, view.DisplayHeight = 1920, 1080
			if i%4 == 1 {
				view.UTMSource, view.UTMMedium, view.UTMCampaign = "newsletter", "email", "launch"
			}
			if i%3 == 0 {
				view.Kind, view.EventName, view.Platform, view.OS, view.OSVersion = "app", "$screen_view", "ios", "ios", "17.4"
				view.Host, view.Path, view.ReferrerSource, view.Browser, view.BrowserVersion = "", "/home", "", "", ""
				view.Device, view.DeviceModel, view.DisplayWidth, view.DisplayHeight = "mobile", "iPhone15,3", 390, 844
				view.AppVersion, view.AppLocale = []string{"2.4.1", "2.5.0"}[i%2], "en"
			}
			minutes, sessions := []int{0, 3}, []string{"", ""}
			switch {
			case i%4 == 3:
				minutes = []int{0}
			case i%4 == 2:
				minutes = []int{0, 3, 45, 48}
				sessions = []string{"s1-" + actor, "s1-" + actor, "s1-" + actor, "s2-" + actor}
			case i == 4 || i == 8:
				minutes = []int{0, 40}
			}
			for v, minute := range minutes {
				e := view
				e.ID, e.Family, e.TS = fmt.Sprintf("v-%s-%d", actor, v), store.FamilyViews, at(day, 1+i, minute, 0)
				if v < len(sessions) {
					e.SessionID = sessions[v]
				}
				add(e)
			}
			for n, name := range []string{"signup", "click"} {
				e := base
				e.ID, e.Family, e.EventName, e.TS = fmt.Sprintf("e-%s-%d", actor, n), store.FamilyProduct, name, at(day, 1+i, n, 30)
				e.Kind, e.Platform, e.OS, e.OSVersion = view.Kind, view.Platform, view.OS, view.OSVersion
				e.Browser, e.BrowserVersion, e.Device = view.Browser, view.BrowserVersion, view.Device
				e.AppVersion, e.AppLocale = []string{"2.4.1", "2.5.0"}[i%2], "en"
				e.Attributes = map[string]string{"plan": []string{"free", "pro"}[i%2]}
				add(e)
			}
		}
		// Measures on the same days: each web actor's page load reports the
		// five Web Vitals (CLS often exactly 0, the zero bucket), a backend
		// reports a custom checkout_api timing, and every third actor
		// samples at half rate so weight differs from samples.
		for i := 0; i < 12; i++ {
			if i%3 == 0 {
				continue // the app actors: no Web Vitals
			}
			rate := 1.0
			if i%3 == 2 {
				rate = 0.5
			}
			for _, m := range []struct {
				name, measure string
				value         float64
			}{
				{"$lcp", "time", 1500 + 350*float64(i)},
				{"$inp", "time", 80 + 45*float64(i)},
				{"$cls", "number", []float64{0, 0, 0.02, 0.05, 0.08, 0.15, 0.3}[i%7]},
				{"$fcp", "time", 900 + 250*float64(i)},
				{"$ttfb", "time", 200 + 160*float64(i)},
				{"checkout_api", "time", 250 + 30*float64(i)},
			} {
				e := store.Event{ID: fmt.Sprintf("m-%d-%d-%s", back, i, m.name), Family: store.FamilyMeasures,
					EventName: m.name, TS: at(day, 1+i, 0, 10), Kind: "web", ActorID: fmt.Sprintf("a%d-%d", back, i),
					ActorKind: "user", Platform: "web", Value: &m.value, Measure: m.measure, SampleRate: rate}
				if m.name != "checkout_api" {
					e.Path, e.Browser = []string{"/", "/pricing", "/docs", "/"}[i%4], []string{"chrome", "safari"}[i%2]
					e.Device = []string{"desktop", "mobile"}[i%4/2]
				}
				add(e)
			}
		}
	}

	// Every older day: ten anonymous visitors (actor kind connection, not
	// cohorted), seven in browsers and three in the iOS app, across every
	// breakdown the Views dashboard draws; two views each, a few minutes
	// apart, but every fourth only one. Five regular users each on
	// alternate days, and one user seen only that day; three groups of
	// two visitors each, and one group seen only that day.
	for back := 3; back < 100; back++ {
		day := today.AddDays(-back)
		var actors []store.Event
		for i := 0; i < 10; i++ {
			e := store.Event{Kind: "web", ActorID: fmt.Sprintf("h%d-%d", back, i), ActorKind: "connection",
				Platform: "web", BrowserLocale: "en-US", Country: []string{"US", "DE", "FR"}[i%3], Consent: consents[i%3]}
			switch {
			case i < 5 && (back+i)%2 == 0:
				e.UserID = fmt.Sprintf("u%d", i)
			case i == 5:
				e.UserID = fmt.Sprintf("once-%d", back)
			}
			switch {
			case i < 6:
				e.GroupID = fmt.Sprintf("g%d", i%3)
			case i == 9:
				e.GroupID = fmt.Sprintf("team-%d", back)
			}
			e.UserName, e.GroupName = userName(e.UserID), groupName(e.GroupID)
			actors = append(actors, e)
		}
		for i, a := range actors {
			view := a
			view.EventName, view.Path = "$page_view", []string{"/", "/pricing", "/docs"}[i%3]
			if i < 7 {
				view.Host = []string{"example.com", "app.example.com"}[i%2]
				view.ReferrerSource = []string{"", "google", "twitter"}[i%3]
				view.OS, view.OSVersion, view.Browser, view.BrowserVersion = "macos", "14", "chrome", "126"
				if i >= 4 {
					view.OS, view.OSVersion = "windows", ""
				}
				if i >= 5 {
					view.Browser, view.BrowserVersion = "safari", "17"
				}
				view.Device, view.DisplayWidth, view.DisplayHeight = "desktop", 1920, 1080
				if i%4 == 1 {
					view.UTMSource, view.UTMMedium, view.UTMCampaign = "newsletter", "email", "launch"
				}
			} else {
				view.Kind, view.EventName, view.Platform, view.OS, view.OSVersion = "app", "$screen_view", "ios", "ios", "17.4"
				view.Path, view.Device, view.DeviceModel, view.DisplayWidth, view.DisplayHeight = "/home", "mobile", "iPhone15,3", 390, 844
				view.AppVersion, view.AppLocale = []string{"2.4.1", "2.5.0"}[i%2], "en"
			}
			minutes := []int{0, 5}
			if i%4 == 3 {
				minutes = []int{0}
			}
			for v, minute := range minutes {
				e := view
				e.ID, e.Family, e.TS = fmt.Sprintf("hv-%d-%d-%d", back, i, v), store.FamilyViews, at(day, 1+i, minute, 0)
				add(e)
			}
			actors[i] = view
		}
		// signup 5 times by four actors, click 20 times by six; signup
		// on 2.4.1 and click on 2.5.0, and three signups on plan pro.
		for n := 0; n < 25; n++ {
			name, a, version := "signup", []int{0, 1, 2, 3, 0}[min(n, 4)], "2.4.1"
			if n >= 5 {
				name, a, version = "click", n%6, "2.5.0"
			}
			e := actors[a]
			e.ID, e.Family, e.EventName, e.TS = fmt.Sprintf("hp-%d-%d", back, n), store.FamilyProduct, name, at(day, 1+a, 10, n)
			e.AppVersion = version
			if n < 3 {
				e.Attributes = map[string]string{"plan": "pro"}
			}
			add(e)
		}
		// Measures: three buckets per metric (good, needs improvement,
		// poor for the vitals) with 3, 2 and 1 samples, drifting with the
		// day of the week so the p75 lines move; the first two in chrome,
		// the last in safari, and only the first on desktop.
		drift := 1 + 0.03*float64(back%7)
		for _, m := range []struct {
			name, measure string
			values        [3]float64
		}{
			{"$lcp", "time", [3]float64{1800, 3000, 4800}},
			{"$inp", "time", [3]float64{120, 300, 650}},
			{"$cls", "number", [3]float64{0, 0.05, 0.3}},
			{"$fcp", "time", [3]float64{1200, 2200, 3600}},
			{"$ttfb", "time", [3]float64{400, 1100, 2200}},
			{"checkout_api", "time", [3]float64{180, 340, 900}},
		} {
			for j, v := range m.values {
				v *= drift
				for s := 0; s < 3-j; s++ {
					e := store.Event{ID: fmt.Sprintf("hm-%d-%s-%d-%d", back, m.name, j, s), Family: store.FamilyMeasures,
						EventName: m.name, TS: at(day, 12, j, s), Kind: "web", ActorID: fmt.Sprintf("h%d-%d", back, j),
						ActorKind: "connection", Platform: "web", Value: &v, Measure: m.measure, SampleRate: 1}
					if m.name != "checkout_api" {
						e.Path, e.Browser, e.Device = "/", []string{"chrome", "chrome", "safari"}[j], []string{"desktop", "mobile", "mobile"}[j]
					}
					add(e)
				}
			}
		}
	}

	// Retention: four actors of each kind first seen on every day through
	// yesterday, each coming back with one view on some of the days after
	// (fewer the later the day), as far as today. A user is named by its
	// user_id, an install by its install_id.
	returning := map[int]int{0: 4, 1: 3, 2: 3, 3: 2, 7: 2, 14: 2, 30: 1, 45: 1}
	for c := 1; c < 100; c++ {
		for _, kind := range []string{"user", "install"} {
			for k := 0; k < 4; k++ {
				actor := fmt.Sprintf("%s-%d-%d", kind, c, k)
				for off, n := range returning {
					if k >= n || c-off < 0 {
						continue
					}
					e := store.Event{ID: fmt.Sprintf("r-%s-%d", actor, off), Family: store.FamilyViews, EventName: "$page_view",
						TS: at(today.AddDays(off-c), 23, k, 0), Kind: "web", ActorID: actor, ActorKind: kind,
						Platform: "web", Host: "example.com", Path: "/"}
					if kind == "user" {
						e.UserID = actor
					} else {
						e.InstallID = actor
					}
					add(e)
				}
			}
		}
	}
	return evs
}
