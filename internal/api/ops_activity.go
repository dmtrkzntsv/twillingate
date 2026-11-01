package api

import (
	"context"
	"maps"
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

// activityRecheck is how often, within activityTTL, a live project with
// event files but no day yet is looked at again (its newest file's month
// only): the first event of a new project must not wait out the TTL.
const activityRecheck = 30 * time.Second

// activityDays caches lastEventDays for activityTTL.
type activityDays struct {
	mu   sync.Mutex
	now  func() time.Time
	at   time.Time // the last full scan
	days map[int64]string
	// checked is the last full scan or recheck of projects with no day.
	checked time.Time
}

// lastEventDays is the day of the newest event (checks aside) of each live
// project with event files, from a scan of the newest file's month and,
// while it holds none, the months before it down to the oldest file (the
// file names say which month, and the newest may hold nothing but checks),
// chunked like usage's and
// cached for activityTTL (not an answer with a fallback in it, nor a read
// whose context ended). Archived projects are not read, and a project
// whose scan fails reads its newest file's day, the failure logged: the
// endpoint does not fail with it. A file name's day stops at today, a month
// file counting as its last day. A project with no event at all is
// absent. A live project with files but no day in the cached answer is
// looked at again every activityRecheck, in its newest file's month only,
// so its first event shows within seconds.
func (h *host) lastEventDays(ctx context.Context, live []*manage.Project) (map[int64]string, error) {
	c := &h.activity
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	if c.days != nil && now().Sub(c.at) < activityTTL {
		h.recheckMissing(ctx, live, now())
		return c.days, nil
	}
	today := now().UTC().Format(time.DateOnly)
	latest, err := archive.LatestDays(h.eventsDir)
	if err != nil {
		return nil, err
	}
	if h.native == nil {
		for id, d := range latest {
			latest[id] = min(d, today)
		}
		return latest, nil
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
	fellBack := false
	for chunk := range slices.Chunk(ids, usageProjectsPerCall) {
		ts, failed, _, err := h.newestRow(ctx, chunk, latest, oldest, "ts", nil, newestOpts{tolerate: true})
		if err != nil {
			return nil, err
		}
		for id, t := range ts {
			days[id] = t[:min(len(t), 10)]
		}
		if len(failed) > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err // a gone client, not a bad scan
			}
			fellBack = true
		}
		for id, ferr := range failed {
			h.logger.Error("project_activity: last_event_day read from the file names, the events cannot be scanned", "project_id", id, "error", ferr)
			days[id] = min(latest[id], today)
		}
	}
	if !fellBack { // a fallback day is not kept: the next read scans again
		c.days, c.at, c.checked = days, now(), now()
	}
	return days, nil
}

// recheckMissing looks again at the live projects the cached answer has no
// day for, when it has been activityRecheck since the last look: their
// newest file's month only, so a monitoring-only project does not pay for
// a step back to its oldest file at every read. A day found joins the
// cached answer. c.mu is held. A failure leaves the answer as it was.
func (h *host) recheckMissing(ctx context.Context, live []*manage.Project, now time.Time) {
	c := &h.activity
	var missing []int64
	for _, p := range live {
		if _, ok := c.days[p.ID]; !ok && !p.Archived {
			missing = append(missing, p.ID)
		}
	}
	if len(missing) == 0 || h.native == nil || now.Sub(c.checked) < activityRecheck {
		return
	}
	c.checked = now
	latest, err := archive.LatestDays(h.eventsDir)
	if err != nil {
		return
	}
	var ids []int64
	for _, id := range missing {
		if _, ok := latest[id]; ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	found := map[int64]string{}
	for chunk := range slices.Chunk(ids, usageProjectsPerCall) {
		ts, _, _, err := h.newestRow(ctx, chunk, latest, nil, "ts", nil, newestOpts{onlyNewest: true, tolerate: true})
		if err != nil {
			return
		}
		for id, t := range ts {
			found[id] = t[:min(len(t), 10)]
		}
	}
	if len(found) == 0 {
		return
	}
	days := maps.Clone(c.days) // the map a reader may still hold is not written
	maps.Copy(days, found)
	c.days = days
}
