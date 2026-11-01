package api

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
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
	live := h.reg.Snapshot(ctx).Projects()
	days, err := h.lastEventDays(ctx, live)
	if err != nil {
		return out, err
	}
	for _, p := range live {
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

// activityTTL is how long the days project_activity reads answer before
// the next read scans again: the sidebar asks on every load.
const activityTTL = 5 * time.Minute

// activityMonthsBack is how many months before the newest file's the
// scan for a project's newest event goes; a project with none in that
// window reads null.
const activityMonthsBack = 3

// activityDays caches lastEventDays for activityTTL.
type activityDays struct {
	mu   sync.Mutex
	now  func() time.Time
	at   time.Time
	days map[int64]string
}

// lastEventDays is the day of the newest event (checks aside) of each live
// project with event files, from a scan of the newest file's month and at
// most activityMonthsBack before it (the file names say which month, and
// the newest file may hold nothing but checks), chunked like usage's and
// cached for activityTTL. Archived projects are not read, and a project
// whose scan fails reads its newest file's day, the failure logged: the
// endpoint does not fail with it. A project with none in the window is
// absent.
func (h *host) lastEventDays(ctx context.Context, live []*manage.Project) (map[int64]string, error) {
	latest, err := archive.LatestDays(h.eventsDir)
	if err != nil {
		return nil, err
	}
	if h.native == nil {
		return latest, nil
	}
	c := &h.activity
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	if c.days != nil && now().Sub(c.at) < activityTTL {
		return c.days, nil
	}
	oldest, err := archive.OldestDays(h.eventsDir)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, p := range live {
		if _, ok := latest[p.ID]; ok && !p.Archived {
			ids = append(ids, p.ID)
		}
	}
	days := map[int64]string{}
	for chunk := range slices.Chunk(ids, usageProjectsPerCall) {
		ts, failed, _, err := h.newestRow(ctx, chunk, latest, oldest, "ts", nil, newestOpts{maxBack: activityMonthsBack, tolerate: true})
		if err != nil {
			return nil, err
		}
		for id, t := range ts {
			days[id] = t[:min(len(t), 10)]
		}
		for id, ferr := range failed {
			h.logger.Error("project_activity: last_event_day read from the file names, the events cannot be scanned", "project_id", id, "error", ferr)
			days[id] = latest[id]
		}
	}
	c.days, c.at = days, now()
	return days, nil
}
