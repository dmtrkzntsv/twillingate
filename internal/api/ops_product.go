package api

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

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

// eventFilter is the condition and value for an optional event name.
func eventFilter(event string) (string, map[string]enginesql.Value) {
	if event == "" {
		return "", nil
	}
	return ` AND event_name = :event`, map[string]enginesql.Value{":event": {Type: "VARCHAR", Text: event}}
}

func (h *host) productEvents(ctx context.Context, in productEventsIn) (productEventsOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return productEventsOut{}, err
	}
	// Unique users are distinct per day and event, as v_product_daily kept
	// them; the totals' active users distinct per day.
	cond, vals := eventFilter(in.Event)
	order := `day, "count" DESC, event_name`
	if in.Event != "" {
		order = `day`
	}
	events, err := h.filesTable(ctx, in.rangeIn, `SELECT day, event_name, count(*) AS "count", count(DISTINCT actor_id) AS unique_users
FROM product WHERE project_id = :project AND day BETWEEN :from AND :to`+cond+`
GROUP BY day, event_name
ORDER BY `+order, vals)
	if err != nil || in.Event != "" {
		return productEventsOut{Events: events}, err
	}
	totals, err := h.filesTable(ctx, in.rangeIn, `SELECT day, count(*) AS total_events, count(DISTINCT actor_id) AS active_users
FROM product WHERE project_id = :project AND day BETWEEN :from AND :to
GROUP BY day
ORDER BY day`, nil)
	if err != nil {
		return productEventsOut{}, err
	}
	return productEventsOut{Events: events, Totals: totals}, nil
}

// productAttributesMaxDays bounds product_attributes' range: its rows are
// per day, event and value, so a year at the cap is millions of groups,
// each with two distinct counts DuckDB cannot spill, and it runs out of
// ENGINE_MEMORY_LIMIT (measured 2026-10-19). A quarter fits.
const productAttributesMaxDays = 92

// productAttributes breaks the product events down by every attribute key
// they carried (one attr_<key> column each, unpivoted as the Product
// dashboard's attribute values are) and by every reserved key in
// native.ReservedKeyColumns, read from its typed column under an "attr_$…" name no key
// can produce (ingest never stores a $ key as an attribute), so the
// unpivot always has a column. The COLUMNS pattern takes the fixed columns too: one that
// matched nothing (no event ever carried an attribute) would be an error.
// The unpivot drops NULLs, a key an event did not carry.
func (h *host) productAttributes(ctx context.Context, in productEventsIn) (tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return tableOut{}, err
	}
	from, _ := time.Parse(time.DateOnly, in.From)
	to, _ := time.Parse(time.DateOnly, in.To)
	if days := int(to.Sub(from).Hours()/24) + 1; days > productAttributesMaxDays {
		return tableOut{}, invalidf("product_attributes covers at most %d days a call, got %d: its rows are per day, event and value, and a longer range "+
			"runs out of the engine's memory before the row cap would cut it anyway. Narrow from and to, or ask query (source files) "+
			"for a coarser grouping, such as each value over the whole range", productAttributesMaxDays, days)
	}
	p := h.reg.Snapshot(ctx).Project(in.ProjectID)
	if p == nil {
		return tableOut{}, h.unknownProjectErr(ctx, in.ProjectID)
	}
	cond, vals := eventFilter(in.Event)
	q := `WITH a AS (
  UNPIVOT (
    SELECT COLUMNS('^(day|event_name|actor_id|group_id|attr_.*)$'),
           ` + reservedKeys() + `
    FROM product
    WHERE project_id = :project AND day BETWEEN :from AND :to` + cond + `
  )
  ON COLUMNS('^attr_') INTO NAME col VALUE attr_value
)
SELECT day, event_name, substr(col, 6) AS attr_key, attr_value, count(*) AS "count",
       count(DISTINCT actor_id) AS unique_users, count(DISTINCT group_id) AS unique_groups
FROM a
GROUP BY day, event_name, col, attr_value
ORDER BY day, "count" DESC, event_name, attr_key, attr_value`
	return h.filesTable(ctx, in.rangeIn, q, vals)
}

// reservedKeys selects each reserved key's column, empty read as NULL, as
// "attr_<key>", in key order.
func reservedKeys() string {
	keys := make([]string, 0, len(native.ReservedKeyColumns))
	for k := range native.ReservedKeyColumns {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		keys[i] = "nullif(" + native.ReservedKeyColumns[k] + `, '') AS "attr_` + k + `"`
	}
	return strings.Join(keys, ",\n           ")
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
	// v_identity_daily summed over the range, as before the files: per day
	// and id, its actors and its users (itself for a user, its members for
	// a group), its views and product events; then each added up over the
	// days. The name is the one the identities table derives from the
	// events.
	id, users := "user_id", "1"
	if in.Kind == "group" {
		id, users = "group_id", "count(DISTINCT user_id)"
	}
	return h.filesTable(ctx, in.rangeIn, `WITH d AS (
  SELECT `+id+` AS id, day, count(DISTINCT actor_id) AS actors, `+users+` AS users,
         count(*) FILTER (WHERE family = 'views') AS views,
         count(*) FILTER (WHERE family = 'product') AS events
  FROM events
  WHERE project_id = :project AND family IN ('views', 'product') AND `+id+` <> ''
    AND day BETWEEN :from AND :to
  GROUP BY 1, day
)
SELECT d.id, COALESCE(i.name, '') AS name, SUM(d.actors) AS actors, SUM(d.users) AS users,
       SUM(d.views) AS views, SUM(d.events) AS events
FROM d
LEFT JOIN identities i ON i.project_id = :project AND i.kind = :kind AND i.id = d.id
GROUP BY d.id, i.name
ORDER BY SUM(d.views) + SUM(d.events) DESC, d.id
LIMIT :limit`, map[string]enginesql.Value{
		":kind":  {Type: "VARCHAR", Text: in.Kind},
		":limit": {Type: "BIGINT", Text: strconv.Itoa(limit)},
	})
}
