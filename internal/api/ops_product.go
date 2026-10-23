package api

import (
	"cmp"
	"context"
	"slices"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
)

type productEventsIn struct {
	rangeIn
	Event string `json:"event,omitempty" jsonschema:"filter to one event name"`
}

// productEventsOut carries the per-event breakdown alongside the daily
// totals (endpoint spec: product_events must surface both). Totals is
// populated only for the unfiltered call: with an event filter, per-event
// Events already answers the question and Totals is left empty.
type productEventsOut struct {
	Events tableOut `json:"events"`
	Totals tableOut `json:"totals"`
}

// productEvents answers two native breakdowns of the product events: per
// day and name, the events and the actors among them (unique users, as
// v_product_daily kept them), by day, the most events first, then by
// name; and per day, the events and the actors (active users).
func (h *host) productEvents(ctx context.Context, in productEventsIn) (productEventsOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return productEventsOut{}, err
	}
	out := productEventsOut{
		Events: tableOut{Columns: []string{"day", "event_name", "count", "unique_users"}, Rows: [][]string{}},
		Totals: tableOut{Columns: []string{"day", "total_events", "active_users"}, Rows: [][]string{}},
	}
	if in.Event != "" {
		out.Totals = tableOut{}
	}
	p, ok := rangeParams(in.rangeIn)
	if !ok {
		return out, nil
	}
	var where []native.Filter
	if in.Event != "" {
		where = []native.Filter{{Column: "event_name", Op: "eq", Values: []string{in.Event}}}
	}
	events, err := h.byDay(ctx, p, []string{"day", "event_name"}, where,
		[]native.Metric{{Op: "count", As: "count"}, {Op: "actors", As: "unique_users"}})
	if err != nil {
		return productEventsOut{}, err
	}
	slices.SortStableFunc(events.Rows, func(a, b []string) int {
		if c := cmp.Compare(a[0], b[0]); c != 0 {
			return c
		}
		if c := cmp.Compare(cellInt(b[2]), cellInt(a[2])); c != 0 {
			return c
		}
		return cmp.Compare(a[1], b[1])
	})
	out.Events = events
	if in.Event != "" {
		return out, nil
	}
	if out.Totals, err = h.byDay(ctx, p, []string{"day"}, nil,
		[]native.Metric{{Op: "count", As: "total_events"}, {Op: "actors", As: "active_users"}}); err != nil {
		return productEventsOut{}, err
	}
	return out, nil
}

// byDay answers a native breakdown of the product events by dims, day
// first, over p, by day: at most CONSOLE_QUERY_ROWS_MAX rows (and the
// engine's 1,000), the earliest days', the rest noted as left out.
func (h *host) byDay(ctx context.Context, p native.Params, dims []string, where []native.Filter, metrics []native.Metric) (tableOut, error) {
	return h.capped(ctx, native.Query{Kind: "breakdown", Table: "product", Dimensions: dims, Order: "day",
		Where: where, Metrics: metrics}, p, 0)
}

// capped answers q with its first rows in q's order: at most limit (all
// of them when 0) and at most CONSOLE_QUERY_ROWS_MAX (rowCap). The table
// is marked truncated when the row cap, not limit, left rows out, or the
// engine left groups untracked; at the engine's 1,000 there is no asking
// for one more, so 1,000 rows past a limit of the tool's own are marked
// too.
func (h *host) capped(ctx context.Context, q native.Query, p native.Params, limit int) (tableOut, error) {
	n := h.rowCap()
	own := limit > 0 && limit <= n
	if own {
		q.Limit = limit
	} else {
		q.Limit = min(n+1, maxBreakdownLimit)
	}
	res, err := h.answer(ctx, q, p)
	if err != nil {
		return tableOut{}, err
	}
	out := tableOut{Columns: res.Columns, Rows: res.Rows}
	if out.Rows == nil {
		out.Rows = [][]string{}
	}
	cut := len(out.Rows) > n
	if cut {
		out.Rows = out.Rows[:n]
	}
	h.markPartial(&out, res.Truncated || cut || !own && len(out.Rows) == maxBreakdownLimit, res.Approximate)
	return out, nil
}

// productAttributes answers a native attributes query of the product
// events: per day, event, attribute and value (every custom key, and
// every reserved key of native.ReservedKeyColumns read from its column,
// empty left out), the events, their distinct actors (unique users) and
// groups, by day, the most events first, then event, key and value: at
// most CONSOLE_QUERY_ROWS_MAX rows (and the engine's 1,000), the earliest
// days'. A range the engine cannot hold is refused (native.ErrTooLarge).
func (h *host) productAttributes(ctx context.Context, in productEventsIn) (tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return tableOut{}, err
	}
	cols := []string{"day", "event_name", "attr_key", "attr_value", "count", "unique_users", "unique_groups"}
	p, ok := rangeParams(in.rangeIn)
	if !ok {
		return tableOut{Columns: cols, Rows: [][]string{}}, nil
	}
	var where []native.Filter
	if in.Event != "" {
		where = []native.Filter{{Column: "event_name", Op: "eq", Values: []string{in.Event}}}
	}
	out, err := h.capped(ctx, native.Query{Kind: "attributes", Table: "product", Output: "values", Reserved: true,
		Dimensions: []string{"day", "event_name"}, Where: where, Metrics: []native.Metric{
			{Op: "count", As: "count"}, {Op: "actors", As: "unique_users"}, {Op: "groups", As: "unique_groups"}}}, p, 0)
	if err != nil {
		return tableOut{}, err
	}
	out.Columns = cols
	return out, nil
}

type retentionIn struct {
	rangeIn
	Actor string `json:"actor" jsonschema:"user or install: cohorts are kept apart by how the actor was identified"`
}

type retentionOut struct {
	tableOut
	AggregatedThrough string `json:"aggregated_through"`
}

// retentionNote is the note retention carries when nothing else needs it.
const retentionNote = "retention counts the days before today (UTC), today being partial; cohorts after aggregated_through are absent, not zero"

func (h *host) retention(ctx context.Context, in retentionIn) (retentionOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return retentionOut{}, err
	}
	if in.Actor != "user" && in.Actor != "install" {
		return retentionOut{}, invalidf("actor must be user or install, got %q", in.Actor)
	}
	p := h.reg.Snapshot(ctx).Project(in.ProjectID)
	if p == nil {
		return retentionOut{}, h.unknownProjectErr(ctx, in.ProjectID)
	}
	// One deadline for both queries.
	ctx, cancel := context.WithTimeout(ctx, h.queryTimeout)
	defer cancel()
	// The Retention dashboard's old cohorts SQL (reporting's
	// testdata/system-sql/retention/cohorts.sql), every
	// offset: an actor identified by a $user_id or a stable $install_id
	// joins the cohort of its first day with either, and is returned at
	// offset k when it sent a view or product event k days later. It is a
	// user from the first day it was identified as one, an install before.
	// Days before today only: today is partial.
	tbl, err := h.filesTable(ctx, in.rangeIn, `WITH acts AS (
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
ORDER BY r.cohort_day, r.day_offset`, map[string]enginesql.Value{":actor": {Type: "VARCHAR", Text: in.Actor}})
	if err != nil {
		return retentionOut{}, err
	}
	out := retentionOut{tableOut: tbl}
	// How far the cohorts reach: the newest complete day with activity in
	// the project's files, so recent cohorts read as "not counted yet",
	// never as zero.
	through, err := h.filesTable(ctx, in.rangeIn, `SELECT max(day) FROM events
WHERE project_id = :project AND family IN ('views', 'product') AND day < current_date`, nil)
	if err != nil {
		return retentionOut{}, err
	}
	if len(through.Rows) == 1 {
		out.AggregatedThrough = through.Rows[0][0]
	}
	if out.Note == "" {
		out.Note = retentionNote
	}
	return out, nil
}

type identitiesIn struct {
	rangeIn
	Kind  string `json:"kind" jsonschema:"user or group"`
	Limit int    `json:"limit,omitempty" jsonschema:"top-N by activity, default 50"`
}

// identities answers a native breakdown of the views and product events
// by user_id (or group_id) and its label: per id, its actors and its
// users (itself for a user, its members for a group), each distinct per
// day and added up over the days, its views and its product events, the
// busiest first (views and events together), then by id. The name is the
// label's, the identities' name of the id; the label reads the id itself
// for an id without a name, which reads as no name here.
func (h *host) identities(ctx context.Context, in identitiesIn) (tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return tableOut{}, err
	}
	if in.Kind != "user" && in.Kind != "group" {
		return tableOut{}, invalidf("kind must be user or group, got %q", in.Kind)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	cols := []string{"id", "name", "actors", "users", "views", "events"}
	p, ok := rangeParams(in.rangeIn)
	if !ok {
		return tableOut{Columns: cols, Rows: [][]string{}}, nil
	}
	id, label := "user_id", "user_label"
	if in.Kind == "group" {
		id, label = "group_id", "group_label"
	}
	family := func(f string) []native.Filter {
		return []native.Filter{{Column: "family", Op: "eq", Values: []string{f}}}
	}
	out, err := h.capped(ctx, native.Query{Kind: "breakdown", Table: "events", Dimensions: []string{id, label}, Order: "actions",
		Where: []native.Filter{{Column: "family", Op: "in", Values: []string{"views", "product"}}, {Column: id, Op: "is_set"}},
		Metrics: []native.Metric{{Op: "count", As: "actions"}, {Op: "daily_sum", Of: "actors", As: "actors"},
			{Op: "daily_sum", Of: "users", As: "users"}, {Op: "count", As: "views", Where: family("views")},
			{Op: "count", As: "events", Where: family("product")}}}, p, limit)
	if err != nil {
		return tableOut{}, err
	}
	for i, r := range out.Rows {
		name := r[1]
		if name == r[0] {
			name = ""
		}
		out.Rows[i] = []string{r[0], name, r[3], r[4], r[5], r[6]}
	}
	out.Columns = cols
	return out, nil
}
