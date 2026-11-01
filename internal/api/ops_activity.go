package api

import (
	"context"
	"maps"
	"slices"
	"strconv"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
)

// Project activity (spec 2026-10-08 D6, D7): per live project, the newest
// day of its event files, the form submissions the console hasn't read,
// and its live forms with the drafts among them, which wait to be
// approved. Kept off list_projects, which reads only the registry
// snapshot because the web app waits on it before loading any widget.

type projectActivityOut struct {
	ProjectID      int64   `json:"project_id"`
	LastEventDay   *string `json:"last_event_day" jsonschema:"the day of the newest event in the event files (availability checks aside); null with none"`
	NewSubmissions int     `json:"new_submissions" jsonschema:"submissions to live forms received after each form's seen_at"`
	Forms          int     `json:"forms" jsonschema:"live (not archived) forms"`
	DraftForms     int     `json:"draft_forms" jsonschema:"live forms still drafts, waiting to be approved"`
}

type projectActivityListOut struct {
	Projects []projectActivityOut `json:"projects"`
}

func (h *host) projectActivity(ctx context.Context, _ struct{}) (projectActivityListOut, error) {
	out := projectActivityListOut{Projects: []projectActivityOut{}}
	// The file names say each project's newest day with a file, which may
	// hold nothing but availability checks, not the site's events: the
	// newest event's own day is read from the files (newestRow), a month's
	// scan for a project with traffic.
	latest, err := archive.LatestDays(h.eventsDir)
	if err != nil {
		return out, err
	}
	days := latest
	if h.native != nil {
		oldest, err := archive.OldestDays(h.eventsDir)
		if err != nil {
			return out, err
		}
		ids := slices.Collect(maps.Keys(latest))
		ts, _, err := h.newestRow(ctx, ids, latest, oldest, "ts", nil)
		if err != nil {
			return out, err
		}
		days = map[int64]string{}
		for id, t := range ts {
			days[id] = t[:min(len(t), 10)]
		}
	}
	for _, p := range h.reg.Snapshot(ctx).Projects() {
		if p.Archived {
			continue
		}
		a := projectActivityOut{ProjectID: p.ID}
		if d, ok := days[p.ID]; ok {
			a.LastEventDay = &d
		}
		// Submissions are read through h.subs, the handle that may.
		// One seek per live form on (project_id, form, received_at), not a
		// scan of every submission the project holds.
		sub, err := h.runSubs(ctx, `SELECT COALESCE(SUM((SELECT COUNT(*) FROM submissions s
			WHERE s.project_id = f.project_id AND s.form = f.name AND s.archived_at IS NULL
			  AND s.received_at > COALESCE(f.seen_at, ''))), 0),
			COUNT(*), COALESCE(SUM(f.status = 'draft'), 0)
			FROM forms f WHERE f.project_id = ?1 AND f.archived_at IS NULL`, p.ID)
		if err != nil {
			return out, err
		}
		row := sub.Rows[0]
		a.NewSubmissions, _ = strconv.Atoi(row[0])
		a.Forms, _ = strconv.Atoi(row[1])
		a.DraftForms, _ = strconv.Atoi(row[2])
		out.Projects = append(out.Projects, a)
	}
	return out, nil
}
