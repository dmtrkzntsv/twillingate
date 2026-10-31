package api

import (
	"cmp"
	"context"
	"slices"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
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
	// One deadline for every query the answer runs.
	ctx, cancel := context.WithTimeout(ctx, h.queryTimeout)
	defer cancel()
	p, ok, err := h.rangeOf(ctx, in.rangeIn)
	if err != nil {
		return productEventsOut{}, err
	}
	out := productEventsOut{
		Events: tableOut{Columns: []string{"day", "event_name", "count", "unique_users"}, Rows: [][]string{}},
		Totals: tableOut{Columns: []string{"day", "total_events", "active_users"}, Rows: [][]string{}},
	}
	if in.Event != "" {
		out.Totals = tableOut{}
	}
	if !ok {
		return out, nil
	}
	var where []native.Filter
	if in.Event != "" {
		where = []native.Filter{{Column: "event_name", Op: "eq", Values: []string{in.Event}}}
	}
	// Both breakdowns read the product events: one call, one scan.
	eventsQ, cutEvents := h.capQuery(productByDay([]string{"day", "event_name"}, where,
		[]native.Metric{{Op: "count", As: "count"}, {Op: "actors", As: "unique_users"}}), 0)
	reqs := []native.Request{{Query: eventsQ, Params: p}}
	totalsQ, cutTotals := h.capQuery(productByDay([]string{"day"}, nil,
		[]native.Metric{{Op: "count", As: "total_events"}, {Op: "actors", As: "active_users"}}), 0)
	if in.Event == "" {
		reqs = append(reqs, native.Request{Query: totalsQ, Params: p})
	}
	ans := h.answerAll(ctx, reqs)
	for _, a := range ans {
		if a.Err != nil {
			return productEventsOut{}, a.Err
		}
	}
	events := cutEvents(ans[0].Result)
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
	if in.Event == "" {
		out.Totals = cutTotals(ans[1].Result)
	}
	return out, nil
}

// productByDay is a native breakdown of the product events by dims, day first,
// by day; capped (capQuery) to at most CONSOLE_QUERY_ROWS_MAX rows (and
// the engine's 1,000), the earliest days', the rest noted as left out.
func productByDay(dims []string, where []native.Filter, metrics []native.Metric) native.Query {
	return native.Query{Kind: "breakdown", Table: "product", Dimensions: dims, Order: "day",
		Where: where, Metrics: metrics}
}

// capped answers q with its first rows in q's order: at most limit (all
// of them when 0) and at most CONSOLE_QUERY_ROWS_MAX (rowCap). The table
// is marked truncated when the row cap, not limit, left rows out, or the
// engine left groups untracked; at the engine's 1,000 there is no asking
// for one more, so 1,000 rows past a limit of the tool's own are marked
// too.
func (h *host) capped(ctx context.Context, q native.Query, p native.Params, limit int) (tableOut, error) {
	q, cut := h.capQuery(q, limit)
	res, err := h.answer(ctx, q, p)
	if err != nil {
		return tableOut{}, err
	}
	return cut(res), nil
}

// capQuery is capped in two halves, for a query answered with others
// (answerAll): q with the limit capped asks, and how capped cuts and
// marks its answer.
func (h *host) capQuery(q native.Query, limit int) (native.Query, func(native.Result) tableOut) {
	n := h.rowCap()
	own := limit > 0 && limit <= n
	if own {
		q.Limit = limit
	} else {
		q.Limit = min(n+1, maxBreakdownLimit)
	}
	return q, func(res native.Result) tableOut {
		out := tableOut{Columns: res.Columns, Rows: res.Rows}
		if out.Rows == nil {
			out.Rows = [][]string{}
		}
		cut := len(out.Rows) > n
		if cut {
			out.Rows = out.Rows[:n]
		}
		h.markPartial(&out, res.Truncated || cut || !own && len(out.Rows) == maxBreakdownLimit, res.Approximate)
		return out
	}
}

// productAttributes answers a native attributes query of the product
// events: per day, event, attribute and value (every custom key, and
// every reserved key of native.ReservedKeyColumns read from its column,
// empty left out), the events, their distinct actors (unique users) and
// groups, by day, the most events first, then event, key and value: at
// most CONSOLE_QUERY_ROWS_MAX rows (and the engine's 1,000), the earliest
// days'. A range the engine cannot hold is refused (native.ErrTooLarge).
func (h *host) productAttributes(ctx context.Context, in productEventsIn) (tableOut, error) {
	p, ok, err := h.rangeOf(ctx, in.rangeIn)
	if err != nil {
		return tableOut{}, err
	}
	cols := []string{"day", "event_name", "attr_key", "attr_value", "count", "unique_users", "unique_groups"}
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

// retentionOffsets is the days back the retention tool counts: the
// native retention's maximum.
const retentionOffsets = 90

// retentionColumns are the retention tool's columns, as its SQL named them.
var retentionColumns = []string{"cohort_day", "day_offset", "actors", "cohort_size"}

// retention answers the native retention grid of the actor's kind, every
// offset up to 90: per cohort of the range and day back, the actors back
// and the cohort's size, by cohort, then offset, at most
// CONSOLE_QUERY_ROWS_MAX rows (the earliest cohorts'). An actor
// identified by a $user_id or a stable $install_id joins the cohort of
// its first day with either, and is back at offset k when it sent a view
// or product event k days later; it is a user from the first day it was
// identified as one, an install before. Days before today only: today is
// partial. aggregated_through is the newest such day with a view or
// product event in the project's files.
func (h *host) retention(ctx context.Context, in retentionIn) (retentionOut, error) {
	params, grid, err := h.rangeOf(ctx, in.rangeIn)
	if err != nil {
		return retentionOut{}, err
	}
	if in.Actor != "user" && in.Actor != "install" {
		return retentionOut{}, invalidf("actor must be user or install, got %q", in.Actor)
	}
	p := h.reg.Snapshot(ctx).Project(in.ProjectID)
	if p == nil {
		return retentionOut{}, h.unknownProjectErr(ctx, in.ProjectID)
	}
	// One deadline for every query the answer runs.
	ctx, cancel := context.WithTimeout(ctx, h.queryTimeout)
	defer cancel()
	out := retentionOut{tableOut: tableOut{Columns: retentionColumns, Rows: [][]string{}}}
	if h.native == nil {
		return retentionOut{}, invalidf("the event files cannot be queried by this process")
	}
	// The grid and how far it reaches, in one call.
	var reqs []native.Request
	if grid {
		reqs = append(reqs, native.Request{Query: native.Query{Kind: "retention", Output: "grid", IdentifiedBy: in.Actor, Offsets: retentionOffsets}, Params: params})
	}
	throughReq, reach := h.aggregatedThrough(in.ProjectID)
	if reach {
		reqs = append(reqs, throughReq)
	}
	ans := h.answerAll(ctx, reqs)
	for _, a := range ans {
		if a.Err != nil {
			return retentionOut{}, a.Err
		}
	}
	if grid {
		// cohort, identified_by, offset, size, returned, retention, by
		// cohort descending: the tool's columns, by cohort, then offset.
		for _, r := range ans[0].Result.Rows {
			out.Rows = append(out.Rows, []string{r[0], r[2], r[4], r[3]})
		}
		slices.SortStableFunc(out.Rows, func(a, b []string) int {
			if c := cmp.Compare(a[0], b[0]); c != 0 {
				return c
			}
			return cmp.Compare(cellInt(a[1]), cellInt(b[1]))
		})
		if h.queryRows > 0 && len(out.Rows) > h.queryRows {
			out.Rows = out.Rows[:h.queryRows]
			h.markPartial(&out.tableOut, true, nil)
		}
	}
	if reach {
		if res := ans[len(ans)-1].Result; len(res.Rows) == 1 {
			out.AggregatedThrough = res.Rows[0][0]
		}
	}
	if out.Note == "" {
		out.Note = retentionNote
	}
	return out, nil
}

// aggregatedThrough is the query of how far a project's cohorts reach:
// the newest day before today (UTC) with a view or product event in its
// files, rules applied, "" with none; so recent cohorts read as "not
// counted yet", never as zero. ok is false when there is no day before
// today to read, which reads as "".
func (h *host) aggregatedThrough(project int64) (req native.Request, ok bool) {
	first, _ := civil.Parse("1970-01-01")
	last := h.native.Today().AddDays(-1) // the retention's today
	if last.Before(first) {
		return native.Request{}, false
	}
	return native.Request{Query: native.Query{Kind: "stat", Table: "events",
		Metrics: []native.Metric{{Op: "max", Column: "day"}},
		Where:   []native.Filter{{Column: "family", Op: "in", Values: []string{"views", "product"}}}},
		Params: native.Params{Project: project, From: first, To: last}}, true
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
	p, ok, err := h.rangeOf(ctx, in.rangeIn)
	if err != nil {
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
