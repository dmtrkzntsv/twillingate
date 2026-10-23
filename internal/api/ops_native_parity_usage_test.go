package api

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The retention and usage tools' SQL before the native engine answered
// them, kept to check the native answers against
// (TestRetentionAndUsageMatchTheirOldSQL).

// oldRetentionSQL is retention's cohorts SQL, :actor its actor.
const oldRetentionSQL = `WITH acts AS (
  SELECT DISTINCT actor_id, actor_kind, day
  FROM events
  WHERE project_id = :project AND family IN ('views', 'product') AND actor_id <> ''
    AND day < current_date
),
firsts AS (
  SELECT actor_id, MIN(day) AS cohort_day, MIN(day) FILTER (WHERE actor_kind = 'user') AS user_day
  FROM acts
  WHERE actor_kind IN ('user', 'install')
  GROUP BY actor_id
),
returned AS (
  SELECT CASE WHEN f.user_day <= a.day THEN 'user' ELSE 'install' END AS actor_kind,
         f.cohort_day, a.day - f.cohort_day AS day_offset, count(DISTINCT a.actor_id) AS actors
  FROM (SELECT DISTINCT actor_id, day FROM acts) a
  JOIN firsts f ON f.actor_id = a.actor_id AND a.day >= f.cohort_day
  WHERE f.cohort_day BETWEEN :from AND :to
  GROUP BY 1, f.cohort_day, 3
)
SELECT r.cohort_day, r.day_offset, r.actors, c.actors AS cohort_size
FROM returned r
JOIN returned c ON c.actor_kind = r.actor_kind AND c.cohort_day = r.cohort_day AND c.day_offset = 0
WHERE r.actor_kind = :actor
ORDER BY r.cohort_day, r.day_offset`

// oldAggregatedThroughSQL is retention's aggregated_through SQL.
const oldAggregatedThroughSQL = `SELECT max(day) FROM events
WHERE project_id = :project AND family IN ('views', 'product') AND day < current_date`

// oldUsageCounts is usage's counting before the native engine: the series
// of one project (project, not 0) or of every project, and the newest
// arrival of the projects in newestOf, in one DuckDB query.
func oldUsageCounts(t *testing.T, h *host, project int64, newestOf []int64, fromD, toD civil.Date) fileCounts {
	t.Helper()
	latest, err := archive.LatestDays(h.eventsDir)
	if err != nil {
		t.Fatal(err)
	}
	vals := map[string]enginesql.Value{
		":from": {Type: "DATE", Text: fromD.String()},
		":to":   {Type: "DATE", Text: toD.String()},
	}
	where := `day BETWEEN :from AND :to`
	if project != 0 {
		where += ` AND project_id = :project`
		vals[":project"] = enginesql.Value{Type: "BIGINT", Text: strconv.FormatInt(project, 10)}
	}
	q := `SELECT project_id, day, family, count(*), NULL FROM events WHERE ` + where + ` GROUP BY ALL`
	if arm := oldNewestArrival(latest, newestOf, vals); arm != "" {
		q += "\nUNION ALL\n" + arm
	}
	bound, args, err := enginesql.Bind(q, vals)
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.runFiles(context.Background(), archive.Scoped{SQL: bound, Args: args})
	if err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	out := fileCounts{days: map[int64]map[string]*usageTotals{}, received: map[int64]string{}}
	for _, row := range res.Rows {
		cells := make([]string, len(row))
		for i, v := range row {
			cells[i] = cellText(v)
		}
		id, err := strconv.ParseInt(cells[0], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if cells[1] == "" {
			if cells[4] != "" {
				out.received[id] = cells[4]
			}
			continue
		}
		n, _ := strconv.ParseInt(cells[3], 10, 64)
		if out.days[id] == nil {
			out.days[id] = map[string]*usageTotals{}
		}
		c := out.days[id][cells[1]]
		if c == nil {
			c = &usageTotals{}
			out.days[id][cells[1]] = c
		}
		switch store.Family(cells[2]) {
		case store.FamilyViews:
			c.Views = n
		case store.FamilyProduct:
			c.Events = n
		case store.FamilyMeasures:
			c.Measures = n
		}
	}
	return out
}

// oldNewestArrival is usage's newest-arrival arm: each project of ids
// from the first day of its newest file's month to that file's last day,
// behind one condition on day spanning them all.
func oldNewestArrival(latest map[int64]string, ids []int64, vals map[string]enginesql.Value) string {
	var conds []string
	var lo, hi string
	for _, id := range ids {
		day, ok := latest[id]
		if !ok {
			continue
		}
		last, err := civil.Parse(day)
		if err != nil {
			continue
		}
		first, to := day[:8]+"01", last.String()
		i := strconv.Itoa(len(conds))
		vals[":np"+i] = enginesql.Value{Type: "BIGINT", Text: strconv.FormatInt(id, 10)}
		vals[":nf"+i] = enginesql.Value{Type: "DATE", Text: first}
		vals[":nt"+i] = enginesql.Value{Type: "DATE", Text: to}
		conds = append(conds, "(project_id = :np"+i+" AND day BETWEEN :nf"+i+" AND :nt"+i+")")
		if lo == "" || first < lo {
			lo = first
		}
		if to > hi {
			hi = to
		}
	}
	if len(conds) == 0 {
		return ""
	}
	vals[":nlo"] = enginesql.Value{Type: "DATE", Text: lo}
	vals[":nhi"] = enginesql.Value{Type: "DATE", Text: hi}
	return `SELECT project_id, NULL, NULL, NULL, max(received_at) FROM events WHERE day BETWEEN :nlo AND :nhi AND (` +
		strings.Join(conds, " OR ") + `) GROUP BY project_id`
}

// writeRetentionEdges adds to the host's files (blog's August cohorts)
// the cases retention and usage must agree on with their old SQL:
//   - s1, an install from 2026-05-10 that signs in on 2026-05-13 (the
//     same actor_id sent as a user), back on 05-14 (a renamed event), 05-20
//     and 2026-08-25: its install cohort holds it on days 0 and 1 only,
//     and no user cohort of that day has a size;
//   - p0, first seen on 2026-04-01 and active again in May: in no May
//     cohort;
//   - u9, a user from its first day, 2026-05-12;
//   - z1, whose only event is blocked (spam), and b2, whose first is,
//     then a view on 2026-05-16, its cohort;
//   - m9, a measure on 2026-05-10 (not counted), a view on 05-12;
//   - o1's views on 2025-10-01 and 2026-01-15, 106 days on: past the 90
//     offsets the native grid counts, the one difference allowed; with
//     the template's August days, a 391-day usage range has rows in both
//     of its 333-day breakdowns;
//   - s1's view on 2026-08-26
//     received at a fraction of a second, the newest arrival;
//   - docs (2) views on 2026-08-21, then archived; and a third project,
//     "empty", with no files.
func writeRetentionEdges(t *testing.T, h *host) {
	t.Helper()
	ctx := context.Background()
	at := func(day string, sec int) time.Time {
		d, err := time.Parse(time.DateOnly, day)
		if err != nil {
			t.Fatal(err)
		}
		return d.Add(9*time.Hour + time.Duration(sec)*time.Second)
	}
	ev := func(project int64, family store.Family, name, day, actor, kind string) store.Event {
		return store.Event{ProjectID: project, Family: family, EventName: name, TS: at(day, 0), Kind: "web", Platform: "web",
			Path: "/", ActorID: actor, ActorKind: kind}
	}
	view := func(day, actor, kind string) store.Event {
		return ev(1, store.FamilyViews, "$page_view", day, actor, kind)
	}
	v := 3.0
	measure := ev(1, store.FamilyMeasures, "load", "2026-05-10", "m9", store.ActorInstall)
	measure.Measure, measure.Value = store.MeasureTime, &v
	late := view("2026-08-26", "s1", store.ActorUser)
	late.TS = time.Date(2026, 8, 26, 11, 0, 0, 123456000, time.UTC)
	writeHostEvents(t, h,
		view("2026-05-10", "s1", store.ActorInstall),
		view("2026-05-11", "s1", store.ActorInstall),
		view("2026-05-13", "s1", store.ActorUser),
		ev(1, store.FamilyProduct, "old_evt", "2026-05-14", "s1", store.ActorUser),
		view("2026-05-20", "s1", store.ActorUser),
		view("2026-08-25", "s1", store.ActorUser),
		view("2026-04-01", "p0", store.ActorInstall),
		view("2026-05-12", "p0", store.ActorInstall),
		view("2026-05-14", "p0", store.ActorInstall),
		view("2026-05-12", "u9", store.ActorUser),
		view("2026-05-13", "u9", store.ActorUser),
		view("2026-05-15", "u9", store.ActorUser),
		ev(1, store.FamilyProduct, "spam", "2026-05-12", "z1", store.ActorInstall),
		ev(1, store.FamilyProduct, "spam", "2026-05-10", "b2", store.ActorInstall),
		view("2026-05-16", "b2", store.ActorInstall),
		measure,
		view("2026-05-12", "m9", store.ActorInstall),
		view("2025-10-01", "o1", store.ActorInstall),
		view("2026-01-15", "o1", store.ActorInstall),
		late,
		ev(2, store.FamilyViews, "$page_view", "2026-08-21", "d1", store.ActorInstall),
	)
	if _, err := h.ops.RenameEvent(ctx, "test", 1, "product", "old_evt", "new_evt"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ops.BlockEvent(ctx, "test", 1, "product", "spam"); err != nil {
		t.Fatal(err)
	}
	if err := h.ops.ArchiveProject(ctx, "test", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ops.CreateProject(ctx, "test", manage.ProjectSpec{Name: "empty"}); err != nil {
		t.Fatal(err)
	}
}

// TestRetentionAndUsageMatchTheirOldSQL runs retention and usage natively
// and checks each answer against the tool's old SQL over the same files,
// rules applied. The one difference allowed is retention's: the native
// grid counts 90 days back, so the old rows past offset 90 are left out
// (o1's day 106 shows it, and the test fails if no row does). The old
// SQL's acts also kept an actor's days as a connection; a connection's id
// is a daily hash, never an install's or a user's, so no file shows that.
func TestRetentionAndUsageMatchTheirOldSQL(t *testing.T) {
	h, _ := newTestHost(t)
	writeRetentionEdges(t, h)
	ctx := context.Background()

	t.Run("retention", func(t *testing.T) {
		sawPast90, sawLateSignIn := false, false
		for _, r := range []rangeIn{
			{ProjectID: 1, From: "2026-05-01", To: "2026-05-31"},
			{ProjectID: 1, From: "2026-05-12", To: "2026-05-12"},
			{ProjectID: 1, From: "2026-08-01", To: "2026-08-31"},
			{ProjectID: 1, From: "2025-09-01", To: "2026-10-31"},
			{ProjectID: 1, From: "2026-05-21", To: "2026-05-20"}, // to before from
			{ProjectID: 2, From: "2026-08-01", To: "2026-08-31"}, // archived
			{ProjectID: 3, From: "2026-08-01", To: "2026-08-31"}, // no files
		} {
			for _, actor := range []string{"user", "install"} {
				name := fmt.Sprintf("%d %s..%s %s", r.ProjectID, r.From, r.To, actor)
				got, err := h.retention(ctx, retentionIn{rangeIn: r, Actor: actor})
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				want, err := h.filesTable(ctx, r, oldRetentionSQL, map[string]enginesql.Value{":actor": {Type: "VARCHAR", Text: actor}})
				if err != nil {
					t.Fatal(err)
				}
				// The allowed difference: no offset past 90.
				want.Rows = slices.DeleteFunc(want.Rows, func(row []string) bool {
					if cellInt(row[1]) > retentionOffsets {
						sawPast90 = true
						return true
					}
					return false
				})
				if actor == "install" && r.From == "2026-05-01" {
					var s1 []string // cohort 2026-05-10 holds s1 alone
					for _, row := range want.Rows {
						if row[0] == "2026-05-10" {
							s1 = append(s1, row[1])
						}
					}
					sawLateSignIn = slices.Equal(s1, []string{"0", "1"})
				}
				sameAnswer(t, name, got.tableOut, want)
				through, err := h.filesTable(ctx, r, oldAggregatedThroughSQL, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(through.Rows) != 1 || got.AggregatedThrough != through.Rows[0][0] {
					t.Errorf("%s: aggregated_through %q, want %q", name, got.AggregatedThrough, through.Rows)
				}
				if got.Truncated || got.Note != retentionNote {
					t.Errorf("%s: truncated %v, note %q", name, got.Truncated, got.Note)
				}
			}
		}
		if !sawPast90 || !sawLateSignIn {
			t.Errorf("the fixture shows no offset past 90 (%v) or no install signing in later (%v): a weak case", sawPast90, sawLateSignIn)
		}
	})

	t.Run("retention under the row cap", func(t *testing.T) {
		h.queryRows = 3
		defer func() { h.queryRows = 1000 }()
		r := rangeIn{ProjectID: 1, From: "2026-08-01", To: "2026-08-31"}
		got, err := h.retention(ctx, retentionIn{rangeIn: r, Actor: "user"})
		if err != nil {
			t.Fatal(err)
		}
		want, err := h.filesTable(ctx, r, oldRetentionSQL, map[string]enginesql.Value{":actor": {Type: "VARCHAR", Text: "user"}})
		if err != nil {
			t.Fatal(err)
		}
		sameAnswer(t, "cut", got.tableOut, want)
		if !got.Truncated || got.Note != want.Note || !want.Truncated {
			t.Errorf("cut: truncated %v note %q, want %v %q", got.Truncated, got.Note, want.Truncated, want.Note)
		}
	})

	t.Run("usage", func(t *testing.T) {
		day := func(s string) civil.Date {
			d, err := civil.Parse(s)
			if err != nil {
				t.Fatal(err)
			}
			return d
		}
		snap := h.reg.Snapshot(ctx)
		all := snap.Projects()
		if len(all) != 3 {
			t.Fatalf("projects %d, want 3", len(all))
		}
		var live []int64
		for _, p := range all {
			if !p.Archived {
				live = append(live, p.ID)
			}
		}
		sawSecondChunk := false
		for _, c := range []struct{ from, to string }{
			{"2025-09-25", "2026-10-20"}, // 391 days: two breakdowns
			{"2026-08-01", "2026-08-31"},
			{"2026-08-26", "2026-08-26"},
			{"2026-06-01", "2026-06-30"}, // no rows
		} {
			from, to := day(c.from), day(c.to)
			check := func(name string, projects []*manage.Project, got, want fileCounts) {
				t.Helper()
				for _, p := range projects {
					g, w := got.days[p.ID], want.days[p.ID]
					if len(g) != len(w) {
						t.Errorf("%s project %d: %d days, want %d", name, p.ID, len(g), len(w))
					}
					for d, n := range w {
						if g[d] == nil || *g[d] != *n {
							t.Errorf("%s project %d %s: %+v, want %+v", name, p.ID, d, g[d], n)
						}
					}
				}
				if !maps.Equal(got.received, want.received) {
					t.Errorf("%s: received %v, want %v", name, got.received, want.received)
				}
			}
			got, err := h.usageCounts(ctx, all, false, from, to)
			if err != nil {
				t.Fatal(err)
			}
			want := oldUsageCounts(t, h, 0, live, from, to)
			delete(want.days, 0) // no project 0
			check(c.from+" every project", all, got, want)
			if want.days[1]["2025-10-01"] != nil {
				for d := range want.days[1] {
					sawSecondChunk = sawSecondChunk || d >= from.AddDays(usageChunkDays).String()
				}
			}
			for _, p := range all {
				one, err := h.usageCounts(ctx, []*manage.Project{p}, true, from, to)
				if err != nil {
					t.Fatal(err)
				}
				check(fmt.Sprintf("%s project %d", c.from, p.ID), []*manage.Project{p}, one, oldUsageCounts(t, h, p.ID, []int64{p.ID}, from, to))
			}
		}
		if !sawSecondChunk {
			t.Error("no row past the first 333 days: a weak case")
		}
	})
}
