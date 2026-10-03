package api

import (
	"context"
	"strconv"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// ---- project_stats ----

type statsIn struct {
	ProjectID int64 `json:"project_id,omitempty" jsonschema:"one project; absent answers every project"`
	usageRangeIn
}

type statsDay struct {
	Day      string `json:"day"`
	Views    int64  `json:"views"`
	Events   int64  `json:"events"`
	Measures int64  `json:"measures"`
}

type statsTotals struct {
	Views    int64 `json:"views"`
	Events   int64 `json:"events"`
	Measures int64 `json:"measures"`
}

type statsSize struct {
	RawBytes       int64 `json:"raw_bytes"`
	AggregateBytes int64 `json:"aggregate_bytes"`
	TotalBytes     int64 `json:"total_bytes"`
}

type projectStats struct {
	ProjectID        int64       `json:"project_id"`
	Series           []statsDay  `json:"series" jsonschema:"every day of the range, zeros included"`
	Totals           statsTotals `json:"totals"`
	LastReceivedAt   *string     `json:"last_received_at" jsonschema:"when the newest raw row arrived; null with none"`
	FirstDay         *string     `json:"first_day" jsonschema:"the oldest day with data, raw or rolled up"`
	RawDays          int         `json:"raw_days"`
	RolledUpDays     int         `json:"rolled_up_days"`
	Size             *statsSize  `json:"size" jsonschema:"an estimate from table sizes; null when they cannot be read"`
	UnusedAttributes []string    `json:"unused_attributes" jsonschema:"declared keys no event carried in the range"`
}

type statsOut struct {
	From          string         `json:"from"`
	To            string         `json:"to"`
	DatabaseBytes int64          `json:"database_bytes"`
	Projects      []projectStats `json:"projects"`
}

func (h *host) projectStats(ctx context.Context, in statsIn) (statsOut, error) {
	snap := h.reg.Snapshot(ctx)
	var projects []*manage.Project
	if in.ProjectID != 0 {
		p := snap.Project(in.ProjectID)
		if p == nil {
			return statsOut{}, h.unknownProjectErr(ctx, in.ProjectID)
		}
		projects = append(projects, p)
	} else {
		projects = snap.Projects()
	}
	fromD, toD, err := usageRange(in.usageRangeIn, time.Now())
	if err != nil {
		return statsOut{}, err
	}
	out := statsOut{From: fromD.String(), To: toD.String(), Projects: []projectStats{}}
	for _, p := range projects {
		ps, err := h.statsFor(ctx, p, fromD, toD)
		if err != nil {
			return statsOut{}, err
		}
		out.Projects = append(out.Projects, ps)
	}
	return out, nil
}

func (h *host) statsFor(ctx context.Context, p *manage.Project, fromD, toD civil.Date) (projectStats, error) {
	ps := projectStats{ProjectID: p.ID, UnusedAttributes: []string{}}
	from, to := fromD.String(), toD.String()
	index := map[string]int{}
	for d := fromD; !toD.Before(d); d = d.AddDays(1) {
		index[d.String()] = len(ps.Series)
		ps.Series = append(ps.Series, statsDay{Day: d.String()})
	}
	for _, s := range []struct {
		q   string
		set func(*statsDay, int64)
	}{
		{`SELECT day, SUM(views) FROM v_views_daily WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY day`,
			func(d *statsDay, n int64) { d.Views = n }},
		{`SELECT day, SUM(total_events) FROM v_product_totals WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY day`,
			func(d *statsDay, n int64) { d.Events = n }},
		{`SELECT day, SUM(samples) FROM v_measures_daily WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY day`,
			func(d *statsDay, n int64) { d.Measures = n }},
	} {
		res, err := h.run(ctx, s.q, p.ID, from, to)
		if err != nil {
			return projectStats{}, err
		}
		for _, r := range res.Rows {
			if i, ok := index[r[0]]; ok {
				n, _ := strconv.ParseInt(r[1], 10, 64)
				s.set(&ps.Series[i], n)
			}
		}
	}
	for _, d := range ps.Series {
		ps.Totals.Views += d.Views
		ps.Totals.Events += d.Events
		ps.Totals.Measures += d.Measures
	}

	res, err := h.run(ctx, `SELECT
		  (SELECT MAX(r) FROM (SELECT MAX(received_at) AS r FROM raw_views WHERE project_id = ?1
		                        UNION ALL SELECT MAX(received_at) FROM raw_product WHERE project_id = ?1
		                        UNION ALL SELECT MAX(received_at) FROM raw_measures WHERE project_id = ?1)),
		  (SELECT MIN(d) FROM (SELECT MIN(day) AS d FROM agg_views_daily WHERE project_id = ?1
		                        UNION ALL SELECT MIN(day) FROM agg_product_totals WHERE project_id = ?1
		                        UNION ALL SELECT MIN(day) FROM agg_measures_daily WHERE project_id = ?1
		                        UNION ALL SELECT MIN(day) FROM raw_views WHERE project_id = ?1
		                        UNION ALL SELECT MIN(day) FROM raw_product WHERE project_id = ?1
		                        UNION ALL SELECT MIN(day) FROM raw_measures WHERE project_id = ?1)),
		  (SELECT COUNT(*) FROM (SELECT day FROM raw_views WHERE project_id = ?1
		                          UNION SELECT day FROM raw_product WHERE project_id = ?1
		                          UNION SELECT day FROM raw_measures WHERE project_id = ?1)),
		  (SELECT COUNT(*) FROM (SELECT day FROM agg_views_daily WHERE project_id = ?1
		                          UNION SELECT day FROM agg_product_totals WHERE project_id = ?1
		                          UNION SELECT day FROM agg_measures_daily WHERE project_id = ?1))`, p.ID)
	if err != nil {
		return projectStats{}, err
	}
	r := res.Rows[0]
	if r[0] != "" {
		ps.LastReceivedAt = &r[0]
	}
	if r[1] != "" {
		ps.FirstDay = &r[1]
	}
	ps.RawDays, _ = strconv.Atoi(r[2])
	ps.RolledUpDays, _ = strconv.Atoi(r[3])

	if len(p.Attributes) > 0 {
		res, err := h.run(ctx, `SELECT attr_key FROM v_product_attrs WHERE project_id = ?1 AND day BETWEEN ?2 AND ?3
			UNION SELECT attr_key FROM v_measures_attrs WHERE project_id = ?1 AND day BETWEEN ?2 AND ?3`, p.ID, from, to)
		if err != nil {
			return projectStats{}, err
		}
		carried := map[string]bool{}
		for _, r := range res.Rows {
			carried[r[0]] = true
		}
		for _, k := range p.Attributes {
			if !carried[k] {
				ps.UnusedAttributes = append(ps.UnusedAttributes, k)
			}
		}
	}
	return ps, nil
}
