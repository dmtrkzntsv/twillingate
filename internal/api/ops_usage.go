package api

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// ---- usage ----

type usageIn struct {
	ProjectID int64 `json:"project_id,omitempty" jsonschema:"one project; absent answers every project"`
	usageRangeIn
}

type usageDay struct {
	Day      string `json:"day"`
	Views    int64  `json:"views"`
	Events   int64  `json:"events"`
	Measures int64  `json:"measures"`
	// TotalBytes is the project's event file bytes as the daily pass
	// measured them that day; null on a day it did not.
	TotalBytes *int64 `json:"total_bytes"`
}

type usageTotals struct {
	Views    int64 `json:"views"`
	Events   int64 `json:"events"`
	Measures int64 `json:"measures"`
}

type usageSize struct {
	TotalBytes int64 `json:"total_bytes"`
	// MeasuredAt is the UTC day the daily pass took the measurement on.
	MeasuredAt string `json:"measured_at"`
}

type projectUsage struct {
	ProjectID      int64       `json:"project_id"`
	Series         []usageDay  `json:"series" jsonschema:"every day of the range, zeros included"`
	Totals         usageTotals `json:"totals"`
	LastReceivedAt *string     `json:"last_received_at" jsonschema:"when the newest event in the project's files arrived, whatever the range; null with none, and for an archived project unless project_id names it"`
	FirstDay       *string     `json:"first_day" jsonschema:"the oldest day the project's event files hold (a month's file counts from its first day); null with none"`
	Size           *usageSize  `json:"size" jsonschema:"the project's event file bytes as the daily pass last measured them, with the day it was measured_at; null until the first measurement"`
}

// dbDay is the database file's size as the daily pass measured it on a
// day; null on a day it was not measured.
type dbDay struct {
	Day   string `json:"day"`
	Bytes *int64 `json:"bytes"`
}

type usageOut struct {
	From           string         `json:"from"`
	To             string         `json:"to"`
	DatabaseBytes  int64          `json:"database_bytes"`
	DatabaseSeries []dbDay        `json:"database_series" jsonschema:"the database file's size per day of the range as the daily pass measured it, null on days not measured"`
	DiskBytes      int64          `json:"disk_bytes" jsonschema:"what the server keeps on disk: the database file now plus every project's event files as the daily pass last measured them"`
	DiskSeries     []dbDay        `json:"disk_series" jsonschema:"disk_bytes per day of the range: the database and every project's event files as the daily pass measured them, null on days the database was not measured"`
	Projects       []projectUsage `json:"projects"`
}

func (h *host) usage(ctx context.Context, in usageIn) (usageOut, error) {
	snap := h.reg.Snapshot(ctx)
	var projects []*manage.Project
	if in.ProjectID != 0 {
		p := snap.Project(in.ProjectID)
		if p == nil {
			return usageOut{}, h.unknownProjectErr(ctx, in.ProjectID)
		}
		projects = append(projects, p)
	} else {
		projects = snap.Projects()
	}
	fromD, toD, err := usageRange(in.usageRangeIn, time.Now())
	if err != nil {
		return usageOut{}, err
	}
	// Each query has a CONSOLE_QUERY_TIMEOUT of its own (the database's
	// through h.db, the event files' through answerEach), so many projects
	// do not share one.
	out := usageOut{From: fromD.String(), To: toD.String(), Projects: []projectUsage{}}
	if res, err := h.db.Run(ctx, `SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`); err == nil && len(res.Rows) == 1 {
		out.DatabaseBytes, _ = strconv.ParseInt(res.Rows[0][0], 10, 64)
	}
	sizes, err := h.readSizes(ctx)
	if err != nil {
		return usageOut{}, err
	}
	st, err := h.readStored(ctx, fromD, toD)
	if err != nil {
		return usageOut{}, err
	}
	// The disk is every project's files, whichever were asked for.
	out.DiskBytes = out.DatabaseBytes
	for _, sz := range sizes {
		out.DiskBytes += sz.TotalBytes
	}
	for d := fromD; !toD.Before(d); d = d.AddDays(1) {
		day, disk := dbDay{Day: d.String()}, dbDay{Day: d.String()}
		if n, ok := st.database[day.Day]; ok {
			day.Bytes = &n
			total := n
			for _, files := range st.files {
				total += files[day.Day]
			}
			disk.Bytes = &total
		}
		out.DatabaseSeries = append(out.DatabaseSeries, day)
		out.DiskSeries = append(out.DiskSeries, disk)
	}
	oldest, err := archive.OldestDays(h.eventsDir)
	if err != nil {
		return usageOut{}, err
	}
	counts, err := h.usageCounts(ctx, projects, in.ProjectID != 0, fromD, toD)
	if err != nil {
		return usageOut{}, err
	}
	for _, p := range projects {
		ps := projectUsage{ProjectID: p.ID, Size: sizes[p.ID]}
		for d := fromD; !toD.Before(d); d = d.AddDays(1) {
			day := usageDay{Day: d.String()}
			if c := counts.days[p.ID][day.Day]; c != nil {
				day.Views, day.Events, day.Measures = c.Views, c.Events, c.Measures
			}
			if n, ok := st.files[p.ID][day.Day]; ok {
				day.TotalBytes = &n
			}
			ps.Totals.Views += day.Views
			ps.Totals.Events += day.Events
			ps.Totals.Measures += day.Measures
			ps.Series = append(ps.Series, day)
		}
		if r, ok := counts.received[p.ID]; ok {
			ps.LastReceivedAt = &r
		}
		if d, ok := oldest[p.ID]; ok {
			ps.FirstDay = &d
		}
		out.Projects = append(out.Projects, ps)
	}
	return out, nil
}

// fileCounts is usageCounts' answer: each project's counts by day, and
// when its newest row arrived.
type fileCounts struct {
	days     map[int64]map[string]*usageTotals
	received map[int64]string
}

// usageChunkDays is the most days one usage breakdown counts: a group per
// day and family, three families, within the engine's 1,000 groups.
const usageChunkDays = maxBreakdownLimit / 3

// usageCounts counts the views, product events and measures of every day
// of the range in the event files of each project, natively, rules
// applied: a breakdown by day and family per project, one per 333 days of
// the range. It also reads when each project's newest row arrived
// (newestRow), for every project when asked, else for the live ones
// only: an archived project's files only wait for the purge. The
// projects' queries go in calls (answerAll) of usageProjectsPerCall
// projects, a project's sharing one scan, read in order: the first error
// is returned and later calls never run. Each query has a
// CONSOLE_QUERY_TIMEOUT of its own (answerEach).
//
// Every query reads the events table, which leaves the checks family out
// (the availability monitor's rows are not the site's traffic), so checks
// are in neither the counts nor last_received_at, which newestRow finds
// even when the newest file holds only checks (TestUsageIgnoresChecks).
func (h *host) usageCounts(ctx context.Context, projects []*manage.Project, asked bool, fromD, toD civil.Date) (fileCounts, error) {
	latest, err := archive.LatestDays(h.eventsDir)
	if err != nil {
		return fileCounts{}, err
	}
	oldest, err := archive.OldestDays(h.eventsDir)
	if err != nil {
		return fileCounts{}, err
	}
	out := fileCounts{days: map[int64]map[string]*usageTotals{}, received: map[int64]string{}}
	for _, p := range projects {
		out.days[p.ID] = map[string]*usageTotals{}
	}
	for group := range slices.Chunk(projects, usageProjectsPerCall) {
		if err := h.usageGroup(ctx, group, asked, fromD, toD, latest, oldest, out); err != nil {
			return fileCounts{}, err
		}
	}
	return out, nil
}

// usageProjectsPerCall is the most projects usage asks for in one call:
// four per engine turn, enough to keep the turns busy without queueing
// every project's scans at once.
const usageProjectsPerCall = 4 * native.DefaultMaxScans

// usageGroup is usageCounts over one group of projects, in one call,
// into out.
func (h *host) usageGroup(ctx context.Context, projects []*manage.Project, asked bool, fromD, toD civil.Date, latest, oldest map[int64]string, out fileCounts) error {
	// asks is each request's project and whether it reads the newest
	// arrival (else it is a chunk's breakdown), in the order the answers
	// are read.
	type ask struct {
		project int64
	}
	var reqs []native.Request
	var asks []ask
	for _, p := range projects {
		for lo := fromD; !toD.Before(lo); lo = lo.AddDays(usageChunkDays) {
			hi := lo.AddDays(usageChunkDays - 1)
			if toD.Before(hi) {
				hi = toD
			}
			reqs = append(reqs, native.Request{Query: native.Query{Kind: "breakdown", Table: "events", Dimensions: []string{"day", "family"},
				Metrics: []native.Metric{{Op: "count"}}, Order: "day", Limit: maxBreakdownLimit},
				Params: native.Params{Project: p.ID, From: lo, To: hi}})
			asks = append(asks, ask{project: p.ID})
		}
	}
	var want []int64
	for _, p := range projects {
		if asked || !p.Archived {
			want = append(want, p.ID)
		}
	}
	// The breakdowns and the newest arrivals go in one call.
	got, _, answers, err := h.newestRow(ctx, want, latest, oldest, "received_at", reqs, newestOpts{})
	if err != nil {
		return err
	}
	maps.Copy(out.received, got)
	for i, a := range answers {
		if a.Err != nil {
			return a.Err
		}
		res, project := a.Result, asks[i].project
		if res.Truncated {
			return invalidf("usage: too many rows; narrow the date range")
		}
		days := out.days[project]
		for _, row := range res.Rows {
			c := days[row[0]]
			if c == nil {
				c = &usageTotals{}
				days[row[0]] = c
			}
			n := cellInt(row[2])
			switch store.Family(row[1]) {
			case store.FamilyViews:
				c.Views = n
			case store.FamilyProduct:
				c.Events = n
			case store.FamilyMeasures:
				c.Measures = n
			}
		}
	}
	return nil
}

// newestOpts bounds newestRow's search and says how it takes a failure.
type newestOpts struct {
	maxBack  int
	tolerate bool
}

// newestRow is, for each of projects with event files, the newest value
// of column (received_at or ts) among its rows of the events table, which
// leaves the checks family out. The file names only bound the search
// (latest, oldest: archive.LatestDays and OldestDays, a month file
// counting as its last or first day), and the newest file may hold nothing
// but checks, so it scans the month of the newest file name and, while a
// project has no row there, the month before, down to its oldest file:
// one month's scan for a project that has traffic, more only for a
// silent one. A project with no row at all is absent. batch goes in the
// first call with the first scans, and its answers are returned in order.
//
// opt.maxBack bounds the search to that many months before the newest
// file's (0: down to the oldest file); a project with no row by then is
// absent. With opt.tolerate a project whose scan fails is left out of the
// answer and named, with its error, in the third result, where otherwise
// the first failure is returned.
func (h *host) newestRow(ctx context.Context, projects []int64, latest, oldest map[int64]string, column string, batch []native.Request, opt newestOpts) (map[int64]string, map[int64]error, []native.Answered, error) {
	type span struct {
		first, last civil.Date
		back        int // months stepped back from the newest file's
	}
	failed := map[int64]error{}
	pending := map[int64]span{}
	for _, id := range projects {
		last, err := civil.Parse(latest[id])
		if err != nil {
			continue // no files
		}
		first, err := civil.Parse(latest[id][:8] + "01")
		if err != nil {
			continue
		}
		pending[id] = span{first: first, last: last}
	}
	out := map[int64]string{}
	batchAnswers := make([]native.Answered, len(batch))
	first := true
	for first || len(pending) > 0 {
		// Requests of one project stay together, in the order given: the
		// engine takes a project's scans as one pass.
		type ask struct {
			req   native.Request
			batch int // index in batch, or -1 for a scan of pending
		}
		var asks []ask
		if first {
			for i, r := range batch {
				asks = append(asks, ask{r, i})
			}
		}
		for id, sp := range pending {
			asks = append(asks, ask{native.Request{Query: native.Query{Kind: "stat", Table: "events", Metrics: []native.Metric{{Op: "max", Column: column}}},
				Params: native.Params{Project: id, From: sp.first, To: sp.last}}, -1})
		}
		slices.SortStableFunc(asks, func(x, y ask) int { return cmp.Compare(x.req.Params.Project, y.req.Params.Project) })
		reqs := make([]native.Request, len(asks))
		for i, a := range asks {
			reqs[i] = a.req
		}
		first = false
		for i, a := range h.answerEach(ctx, reqs) {
			if asks[i].batch >= 0 {
				batchAnswers[asks[i].batch] = a
				continue
			}
			id := asks[i].req.Params.Project
			if a.Err != nil {
				if !opt.tolerate {
					return nil, nil, nil, a.Err
				}
				failed[id] = a.Err
				delete(pending, id)
				continue
			}
			if rows := a.Result.Rows; len(rows) == 1 && rows[0][0] != "" {
				out[id] = rows[0][0]
				delete(pending, id)
				continue
			}
			sp := pending[id]
			prev := sp.first.AddDays(-1)
			if o, err := civil.Parse(oldest[id]); err != nil || prev.Before(o) || (opt.maxBack > 0 && sp.back >= opt.maxBack) {
				delete(pending, id)
				continue
			}
			m, _ := civil.Parse(prev.String()[:8] + "01")
			pending[id] = span{m, prev, sp.back + 1}
		}
	}
	return out, failed, batchAnswers, nil
}

// readSizes answers every project's latest size, by project id: its
// file_bytes row on the newest day the daily pass measured any, read in
// one query however many projects there are. A project with no row that
// day had no files left to measure and is absent, even if an earlier day
// measured it.
func (h *host) readSizes(ctx context.Context) (map[int64]*usageSize, error) {
	res, err := h.run(ctx, `SELECT project_id, value, measured_at FROM usage_history
		WHERE key = ?1 AND measured_at = (SELECT MAX(measured_at) FROM usage_history WHERE key = ?1)`,
		store.StatFileBytes)
	if err != nil {
		return nil, err
	}
	out := map[int64]*usageSize{}
	for _, r := range res.Rows {
		id, _ := strconv.ParseInt(r[0], 10, 64)
		n, _ := strconv.ParseInt(r[1], 10, 64)
		out[id] = &usageSize{TotalBytes: n, MeasuredAt: r[2]}
	}
	return out, nil
}

// stored is what the daily pass measured over a range: each project's
// file bytes and the database's size, by day.
type stored struct {
	files    map[int64]map[string]int64
	database map[string]int64
}

// readStored reads the range a page at a time, in the order of the
// table's key, each page after the last row of the one before: a row per
// project and day outgrows CONSOLE_QUERY_ROWS_MAX over a few months.
func (h *host) readStored(ctx context.Context, fromD, toD civil.Date) (stored, error) {
	st := stored{files: map[int64]map[string]int64{}, database: map[string]int64{}}
	after := []string{"", "-1", ""}
	for {
		res, err := h.db.Run(ctx, `SELECT key, project_id, measured_at, value FROM usage_history
			WHERE key IN (?1, ?2) AND measured_at BETWEEN ?3 AND ?4 AND (key, project_id, measured_at) > (?5, ?6, ?7)
			ORDER BY key, project_id, measured_at`,
			store.StatFileBytes, store.StatDatabaseBytes, fromD.String(), toD.String(), after[0], after[1], after[2])
		if err != nil {
			return stored{}, runErr(h.db, err)
		}
		for _, r := range res.Rows {
			id, _ := strconv.ParseInt(r[1], 10, 64)
			n, _ := strconv.ParseInt(r[3], 10, 64)
			if r[0] == store.StatDatabaseBytes {
				st.database[r[2]] = n
				continue
			}
			if st.files[id] == nil {
				st.files[id] = map[string]int64{}
			}
			st.files[id][r[2]] = n
		}
		if !res.Truncated || len(res.Rows) == 0 {
			return st, nil
		}
		after = res.Rows[len(res.Rows)-1][:3]
	}
}
