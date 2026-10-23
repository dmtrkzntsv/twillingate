package api

import (
	"context"
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
	for d := fromD; !toD.Before(d); d = d.AddDays(1) {
		day := dbDay{Day: d.String()}
		if n, ok := st.database[day.Day]; ok {
			day.Bytes = &n
		}
		out.DatabaseSeries = append(out.DatabaseSeries, day)
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
// (newestArrival), for every project when asked, else for the live ones
// only: an archived project's files only wait for the purge.
func (h *host) usageCounts(ctx context.Context, projects []*manage.Project, asked bool, fromD, toD civil.Date) (fileCounts, error) {
	latest, err := archive.LatestDays(h.eventsDir)
	if err != nil {
		return fileCounts{}, err
	}
	out := fileCounts{days: map[int64]map[string]*usageTotals{}, received: map[int64]string{}}
	for _, p := range projects {
		days := map[string]*usageTotals{}
		for lo := fromD; !toD.Before(lo); lo = lo.AddDays(usageChunkDays) {
			hi := lo.AddDays(usageChunkDays - 1)
			if toD.Before(hi) {
				hi = toD
			}
			res, err := h.answer(ctx, native.Query{Kind: "breakdown", Table: "events", Dimensions: []string{"day", "family"},
				Metrics: []native.Metric{{Op: "count"}}, Order: "day", Limit: maxBreakdownLimit},
				native.Params{Project: p.ID, From: lo, To: hi})
			if err != nil {
				return fileCounts{}, err
			}
			if res.Truncated {
				return fileCounts{}, invalidf("usage: too many rows; narrow the date range")
			}
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
		out.days[p.ID] = days
		if !asked && p.Archived {
			continue
		}
		r, err := h.newestArrival(ctx, p.ID, latest[p.ID])
		if err != nil {
			return fileCounts{}, err
		}
		if r != "" {
			out.received[p.ID] = r
		}
	}
	return out, nil
}

// newestArrival is when the newest row of a project arrived, "" when it
// has none: the newest received_at from the first day of the month of its
// newest file name (latest, archive.LatestDays: a month's file counts as
// its last day, its rows may end earlier) to that day, so it is found
// whatever the range and scans at most a month.
func (h *host) newestArrival(ctx context.Context, project int64, latest string) (string, error) {
	last, err := civil.Parse(latest)
	if err != nil {
		return "", nil // no files
	}
	first, err := civil.Parse(latest[:8] + "01")
	if err != nil {
		return "", nil
	}
	res, err := h.answer(ctx, native.Query{Kind: "stat", Table: "events", Metrics: []native.Metric{{Op: "max", Column: "received_at"}}},
		native.Params{Project: project, From: first, To: last})
	if err != nil || len(res.Rows) != 1 {
		return "", err
	}
	return res.Rows[0][0], nil
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

// stored is what the daily pass measured over a range, read in one
// query: each project's file bytes and the database's size, by day.
type stored struct {
	files    map[int64]map[string]int64
	database map[string]int64
}

func (h *host) readStored(ctx context.Context, fromD, toD civil.Date) (stored, error) {
	st := stored{files: map[int64]map[string]int64{}, database: map[string]int64{}}
	res, err := h.run(ctx, `SELECT key, project_id, measured_at, value FROM usage_history
		WHERE key IN (?1, ?2) AND measured_at BETWEEN ?3 AND ?4`,
		store.StatFileBytes, store.StatDatabaseBytes, fromD.String(), toD.String())
	if err != nil {
		return stored{}, err
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
	return st, nil
}
