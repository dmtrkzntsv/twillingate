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
// a read starts another scan: the sidebar asks on every load.
const activityTTL = 5 * time.Minute

// activityFallbackTTL is how long an answer from a scan that failed stands
// before a read starts another, so a failing scan is not repeated per read.
const activityFallbackTTL = 30 * time.Second

// activityRecheck is how often, within activityTTL, a live project with
// event files but no day yet is looked at again (its newest file's month
// only): the first event of a new project must not wait out the TTL.
const activityRecheck = 30 * time.Second

// activityDays caches lastEventDays. Scans run in the background, one at a
// time, on a context of their own, as eventCount's do (the host has no
// shutdown hook; each request of a scan is bounded by CONSOLE_QUERY_TIMEOUT).
// mu is never held across one, so a read never waits for it but the very
// first, which has nothing to answer.
type activityDays struct {
	mu  sync.Mutex
	now func() time.Time
	// days is the last answer, nil before the first; err how the last scan
	// failed when it listed no files.
	days map[int64]string
	err  error
	// fresh is when days or err stop answering without a new scan.
	fresh time.Time
	// checked is the last full scan or recheck of projects with no day.
	checked time.Time
	// running is closed when the scan in flight ends; nil when none is.
	running chan struct{}
}

func (c *activityDays) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// lastEventDays is the day of the newest event (checks aside) of each live
// project with event files (see scanEventDays). A read with an answer cached
// returns it at once, whatever its age, and past activityTTL (activityFallbackTTL
// after a failed scan) starts a refresh unless one is running; only a read with
// none waits, as long as its own context lets it, sharing the scan with any
// other. Within the TTL a live project with files but no day in the answer is
// looked at again every activityRecheck (recheckMissing), so its first event
// shows within seconds. In a process with no engine it reads the file names.
func (h *host) lastEventDays(ctx context.Context, live []*manage.Project) (map[int64]string, error) {
	c := &h.activity
	if h.native == nil {
		latest, err := archive.LatestDays(h.eventsDir)
		if err != nil {
			return nil, err
		}
		today := c.clock().UTC().Format(time.DateOnly)
		for id, d := range latest {
			latest[id] = min(d, today)
		}
		return latest, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	now := c.clock()
	expired := !now.Before(c.fresh)
	if c.days != nil {
		switch {
		case expired:
			h.startActivityLocked(func() { h.scanEventDays(live) })
		case now.Sub(c.checked) >= activityRecheck:
			if missing := c.missingLocked(live); len(missing) > 0 {
				c.checked = now
				h.startActivityLocked(func() { h.recheckMissing(missing) })
			}
		}
		days := c.days
		c.mu.Unlock()
		return days, nil
	}
	if c.err != nil && !expired {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	done := h.startActivityLocked(func() { h.scanEventDays(live) })
	c.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.days, c.err
}

// startActivityLocked runs scan in the background unless a scan is running,
// returning the channel closed when the one in flight ends.
func (h *host) startActivityLocked(scan func()) chan struct{} {
	c := &h.activity
	if c.running != nil {
		return c.running
	}
	done := make(chan struct{})
	c.running = done
	go func() {
		defer func() {
			c.mu.Lock()
			c.running = nil
			c.mu.Unlock()
			close(done)
		}()
		scan()
	}()
	return done
}

// missingLocked is the live projects the answer has no day for.
func (c *activityDays) missingLocked(live []*manage.Project) []int64 {
	var missing []int64
	for _, p := range live {
		if _, ok := c.days[p.ID]; !ok && !p.Archived {
			missing = append(missing, p.ID)
		}
	}
	return missing
}

// scanEventDays stores the day of the newest event (checks aside) of each
// live project with event files, from a scan of the newest file's month and,
// while it holds none, the months before it down to the oldest file (the
// file names say which month, and the newest may hold nothing but checks),
// chunked like usage's. Archived projects are not read, and a project whose
// scan fails, a timeout included, reads its newest file's day, the failure
// logged, and the answer stands only for activityFallbackTTL. A file name's day
// stops at today, a month file counting as its last day. A project with no
// event at all is absent. Files that cannot be listed leave the last answer as
// it is, for as long.
func (h *host) scanEventDays(live []*manage.Project) {
	c := &h.activity
	ctx := context.Background() // a reader that leaves does not end it for the others
	today := c.clock().UTC().Format(time.DateOnly)
	listed := func() (latest, oldest map[int64]string, err error) {
		if latest, err = archive.LatestDays(h.eventsDir); err != nil {
			return nil, nil, err
		}
		oldest, err = archive.OldestDays(h.eventsDir)
		return latest, oldest, err
	}
	latest, oldest, err := listed()
	if err != nil {
		h.logger.Error("project_activity: the event files cannot be listed", "error", err)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.err, c.fresh = err, c.clock().Add(activityFallbackTTL)
		return
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
		if err != nil { // with tolerate, none today: every project of the chunk failed
			failed = map[int64]error{}
			for _, id := range chunk {
				failed[id] = err
			}
		}
		for id, t := range ts {
			days[id] = t[:min(len(t), 10)]
		}
		for id, ferr := range failed {
			h.logger.Error("project_activity: last_event_day read from the file names, the events cannot be scanned", "project_id", id, "error", ferr)
			days[id] = min(latest[id], today)
			fellBack = true
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	ttl := activityTTL
	if fellBack {
		ttl = activityFallbackTTL
	}
	c.days, c.err, c.fresh, c.checked = days, nil, now.Add(ttl), now
}

// recheckMissing looks again at projects the answer has no day for: their
// newest file's month only, so a monitoring-only project does not pay for a
// step back to its oldest file at every look. A day found joins the answer; a
// failure leaves it as it was.
func (h *host) recheckMissing(missing []int64) {
	c := &h.activity
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
	found := map[int64]string{}
	for chunk := range slices.Chunk(ids, usageProjectsPerCall) {
		ts, _, _, err := h.newestRow(context.Background(), chunk, latest, nil, "ts", nil, newestOpts{onlyNewest: true, tolerate: true})
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
	c.mu.Lock()
	defer c.mu.Unlock()
	days := maps.Clone(c.days) // the map a reader may still hold is not written
	maps.Copy(days, found)
	c.days = days
}
