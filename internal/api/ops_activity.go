package api

import (
	"context"
	"strconv"
)

// Project activity (spec 2026-10-08 D6, D7): per live project, the newest
// day with data and the form submissions the console hasn't read. Kept off
// list_projects, which reads only the registry snapshot because the web
// app waits on it before loading any widget.

type projectActivityOut struct {
	ProjectID      int64   `json:"project_id"`
	LastEventDay   *string `json:"last_event_day" jsonschema:"the newest day with views, product events or measures, raw or rolled up; null with none"`
	NewSubmissions int     `json:"new_submissions" jsonschema:"submissions to live forms received after each form's seen_at"`
}

type projectActivityListOut struct {
	Projects []projectActivityOut `json:"projects"`
}

func (h *host) projectActivity(ctx context.Context, _ struct{}) (projectActivityListOut, error) {
	out := projectActivityListOut{Projects: []projectActivityOut{}}
	for _, p := range h.reg.Snapshot(ctx).Projects() {
		if p.Archived {
			continue
		}
		a := projectActivityOut{ProjectID: p.ID}
		// The newest day of each family is one index seek.
		res, err := h.run(ctx, `SELECT MAX(d) FROM (
			SELECT MAX(day) AS d FROM raw_views WHERE project_id = ?1
			UNION ALL SELECT MAX(day) FROM raw_product WHERE project_id = ?1
			UNION ALL SELECT MAX(day) FROM raw_measures WHERE project_id = ?1
			UNION ALL SELECT MAX(day) FROM agg_views_daily WHERE project_id = ?1
			UNION ALL SELECT MAX(day) FROM agg_product_totals WHERE project_id = ?1
			UNION ALL SELECT MAX(day) FROM agg_measures_daily WHERE project_id = ?1)`, p.ID)
		if err != nil {
			return out, err
		}
		if d := res.Rows[0][0]; d != "" {
			a.LastEventDay = &d
		}
		// Submissions are read through h.subs, the handle that may.
		// One seek per live form on (project_id, form, received_at), not a
		// scan of every submission the project holds.
		sub, err := h.runSubs(ctx, `SELECT COALESCE(SUM((SELECT COUNT(*) FROM submissions s
			WHERE s.project_id = f.project_id AND s.form = f.name
			  AND s.received_at > COALESCE(f.seen_at, ''))), 0)
			FROM forms f WHERE f.project_id = ?1 AND f.archived_at IS NULL`, p.ID)
		if err != nil {
			return out, err
		}
		a.NewSubmissions, _ = strconv.Atoi(sub.Rows[0][0])
		out.Projects = append(out.Projects, a)
	}
	return out, nil
}
