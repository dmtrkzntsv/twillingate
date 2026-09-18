package api

import (
	"context"
)

type productEventsIn struct {
	rangeIn
	Event string `json:"event,omitempty" jsonschema:"filter to one event name"`
}

// productEventsOut carries the per-event breakdown alongside the daily
// totals from v_product_totals (endpoint spec: product_events must
// surface both). Totals is populated only for the unfiltered call: with
// an event filter, per-event Events already answers the question and
// Totals is left empty.
type productEventsOut struct {
	Events tableOut `json:"events"`
	Totals tableOut `json:"totals"`
}

func (h *host) productEvents(ctx context.Context, in productEventsIn) (productEventsOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return productEventsOut{}, err
	}
	if in.Event != "" {
		events, err := h.table(ctx, `SELECT day, event_name, count, unique_users
			FROM v_product_daily WHERE project=? AND day BETWEEN ? AND ? AND event_name=?
			ORDER BY day`, in.Project, in.From, in.To, in.Event)
		if err != nil {
			return productEventsOut{}, err
		}
		return productEventsOut{Events: events}, nil
	}
	events, err := h.table(ctx, `SELECT day, event_name, count, unique_users
		FROM v_product_daily WHERE project=? AND day BETWEEN ? AND ?
		ORDER BY day, count DESC`, in.Project, in.From, in.To)
	if err != nil {
		return productEventsOut{}, err
	}
	totals, err := h.table(ctx, `SELECT day, total_events, active_users
		FROM v_product_totals WHERE project=? AND day BETWEEN ? AND ? ORDER BY day`,
		in.Project, in.From, in.To)
	if err != nil {
		return productEventsOut{}, err
	}
	return productEventsOut{Events: events, Totals: totals}, nil
}

func (h *host) productAttributes(ctx context.Context, in productEventsIn) (tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return tableOut{}, err
	}
	p := h.reg.Snapshot(ctx).Project(in.Project)
	if p == nil {
		return tableOut{}, h.unknownProjectErr(ctx, in.Project)
	}
	q := `SELECT day, event_name, attr_key, attr_value, count, unique_users
		FROM v_product_attrs WHERE project=? AND day BETWEEN ? AND ?`
	args := []any{in.Project, in.From, in.To}
	if in.Event != "" {
		q += ` AND event_name=?`
		args = append(args, in.Event)
	}
	out, err := h.table(ctx, q+` ORDER BY day, count DESC`, args...)
	return out, err
}

type retentionIn struct {
	rangeIn
	Surface string `json:"surface" jsonschema:"web, app or product (actors first seen through custom events alone); each population is cohorted separately"`
}

type retentionOut struct {
	tableOut
	AggregatedThrough string `json:"aggregated_through"`
}

func (h *host) retention(ctx context.Context, in retentionIn) (retentionOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return retentionOut{}, err
	}
	if in.Surface != "web" && in.Surface != "app" && in.Surface != "product" {
		return retentionOut{}, invalidf("surface must be web, app or product, got %q", in.Surface)
	}
	p := h.reg.Snapshot(ctx).Project(in.Project)
	if p == nil {
		return retentionOut{}, h.unknownProjectErr(ctx, in.Project)
	}
	if p.Identity != "identified" {
		return retentionOut{}, invalidf(
			"project %q is anonymous: retention is undefined because visitor ids rotate daily; it requires the project setting identity=identified (a privacy-significant change — see the README's GDPR section)", in.Project)
	}
	tbl, err := h.table(ctx, `SELECT cohort_day, day_offset, actors, cohort_size, users, user_cohort_size
		FROM v_retention WHERE project=? AND surface=? AND cohort_day BETWEEN ? AND ?
		ORDER BY cohort_day, day_offset`, in.Project, in.Surface, in.From, in.To)
	if err != nil {
		return retentionOut{}, err
	}
	out := retentionOut{tableOut: tbl}
	// v_retention has no live half: report how fresh it is so recent
	// cohorts are read as "not yet aggregated", never as zero.
	_, rows, _, err := queryRows(ctx, h.db, h.timeout, 1,
		`SELECT COALESCE(MAX(cohort_day),'') FROM agg_retention WHERE project=?`, in.Project)
	if err == nil && len(rows) == 1 {
		out.AggregatedThrough = rows[0][0]
	}
	if out.Note == "" {
		out.Note = "retention refreshes at the 03:00 UTC daily pass; cohorts after aggregated_through are absent, not zero"
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
	out, err := h.table(ctx, `SELECT d.id, COALESCE(i.name,'') AS name,
		SUM(d.actors) AS actors, SUM(d.users) AS users, SUM(d.hits) AS hits, SUM(d.views) AS views, SUM(d.events) AS events
		FROM v_identity_daily d
		LEFT JOIN identities i ON i.project=d.project AND i.kind=d.kind AND i.id=d.id
		WHERE d.project=? AND d.kind=? AND d.day BETWEEN ? AND ?
		GROUP BY d.id, i.name
		ORDER BY hits+views+events DESC LIMIT ?`,
		in.Project, in.Kind, in.From, in.To, limit)
	return out, err
}
