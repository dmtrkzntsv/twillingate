package api

import (
	"context"
	"strconv"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
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
	// TotalBytes is the project's size as the daily pass measured it that
	// day; null on a day it was not measured.
	TotalBytes *int64 `json:"total_bytes"`
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
	// MeasuredAt is the UTC day the daily pass took the measurement on.
	MeasuredAt string `json:"measured_at"`
}

type projectStats struct {
	ProjectID        int64       `json:"project_id"`
	Series           []statsDay  `json:"series" jsonschema:"every day of the range, zeros included"`
	Totals           statsTotals `json:"totals"`
	LastReceivedAt   *string     `json:"last_received_at" jsonschema:"when the newest raw row arrived; null with none"`
	FirstDay         *string     `json:"first_day" jsonschema:"the oldest day with data, raw or rolled up"`
	RawDays          int         `json:"raw_days"`
	RolledUpDays     int         `json:"rolled_up_days"`
	Size             *statsSize  `json:"size" jsonschema:"the latest estimate the daily pass measured, with the day it was measured_at; null until the first measurement"`
	UnusedAttributes *[]string   `json:"unused_attributes" jsonschema:"declared keys no event carried in the range; computed only when project_id is given, null otherwise"`
}

// dbDay is the database file's size as the daily pass measured it on a
// day; null on a day it was not measured.
type dbDay struct {
	Day   string `json:"day"`
	Bytes *int64 `json:"bytes"`
}

type statsOut struct {
	From           string         `json:"from"`
	To             string         `json:"to"`
	DatabaseBytes  int64          `json:"database_bytes"`
	DatabaseSeries []dbDay        `json:"database_series" jsonschema:"the database file's size per day of the range as the daily pass measured it, null on days not measured"`
	Projects       []projectStats `json:"projects"`
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
	if res, err := h.db.Run(ctx, `SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`); err == nil && len(res.Rows) == 1 {
		out.DatabaseBytes, _ = strconv.ParseInt(res.Rows[0][0], 10, 64)
	}
	sizes, err := h.readSizes(ctx)
	if err != nil {
		return statsOut{}, err
	}
	st, err := h.readStored(ctx, fromD, toD)
	if err != nil {
		return statsOut{}, err
	}
	for d := fromD; !toD.Before(d); d = d.AddDays(1) {
		day := dbDay{Day: d.String()}
		if n, ok := st.database[day.Day]; ok {
			day.Bytes = &n
		}
		out.DatabaseSeries = append(out.DatabaseSeries, day)
	}
	for _, p := range projects {
		ps, err := h.statsFor(ctx, p, fromD, toD, st.countedBefore, in.ProjectID != 0)
		if err != nil {
			return statsOut{}, err
		}
		ps.Size = sizes[p.ID]
		for i := range ps.Series {
			d := &ps.Series[i]
			stored := st.days[p.ID][d.Day]
			if stored == nil {
				continue
			}
			if d.Day < st.countedBefore {
				d.Views, d.Events, d.Measures = stored.views, stored.events, stored.measures
			}
			d.TotalBytes = stored.totalBytes
		}
		for _, d := range ps.Series {
			ps.Totals.Views += d.Views
			ps.Totals.Events += d.Events
			ps.Totals.Measures += d.Measures
		}
		out.Projects = append(out.Projects, ps)
	}
	return out, nil
}

// statsFor reads one project's usage. Its series counts live only the
// days from countedBefore on (the newest daily pass's day, "" before any):
// the days before it are stored, and projectStats fills them in. withUnused
// also computes the declared attributes no event carried: two view scans
// per project, so only the one-project answer pays for it.
func (h *host) statsFor(ctx context.Context, p *manage.Project, fromD, toD civil.Date, countedBefore string, withUnused bool) (projectStats, error) {
	ps := projectStats{ProjectID: p.ID}
	from, to := fromD.String(), toD.String()
	index := map[string]int{}
	for d := fromD; !toD.Before(d); d = d.AddDays(1) {
		index[d.String()] = len(ps.Series)
		ps.Series = append(ps.Series, statsDay{Day: d.String()})
	}
	liveFrom := from
	if countedBefore > liveFrom {
		liveFrom = countedBefore
	}
	for _, s := range []struct {
		q   string
		set func(*statsDay, int64)
	}{
		// Not v_views_daily: its live half sessionizes raw rows with window
		// functions only to yield a count, and this runs per project. A day
		// is rolled up or raw, never both, and each view counts once either
		// way, so this sums to the same (TestProjectStatsViewsMatchTheView).
		{`SELECT day, SUM(n) FROM (
			  SELECT day, SUM(views) AS n FROM agg_views_daily WHERE project_id = ?1 AND day BETWEEN ?2 AND ?3 GROUP BY day
			  UNION ALL
			  SELECT day, COUNT(*) FROM raw_views WHERE project_id = ?1 AND day BETWEEN ?2 AND ?3 GROUP BY day
			) GROUP BY day`,
			func(d *statsDay, n int64) { d.Views = n }},
		{`SELECT day, SUM(total_events) FROM v_product_totals WHERE project_id = ?1 AND day BETWEEN ?2 AND ?3 GROUP BY day`,
			func(d *statsDay, n int64) { d.Events = n }},
		{`SELECT day, SUM(samples) FROM v_measures_daily WHERE project_id = ?1 AND day BETWEEN ?2 AND ?3 GROUP BY day`,
			func(d *statsDay, n int64) { d.Measures = n }},
	} {
		if liveFrom > to {
			break
		}
		res, err := h.run(ctx, s.q, p.ID, liveFrom, to)
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
	// The newest row arrived on its family's newest day, so each family
	// scans one day, not every row of the project.
	res, err := h.run(ctx, `SELECT
		  (SELECT MAX(r) FROM (SELECT MAX(received_at) AS r FROM raw_views WHERE project_id = ?1
		                          AND day = (SELECT MAX(day) FROM raw_views WHERE project_id = ?1)
		                        UNION ALL SELECT MAX(received_at) FROM raw_product WHERE project_id = ?1
		                          AND day = (SELECT MAX(day) FROM raw_product WHERE project_id = ?1)
		                        UNION ALL SELECT MAX(received_at) FROM raw_measures WHERE project_id = ?1
		                          AND day = (SELECT MAX(day) FROM raw_measures WHERE project_id = ?1))),
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

	if withUnused {
		unused := []string{}
		ps.UnusedAttributes = &unused
	}
	if withUnused && len(p.Attributes) > 0 {
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
				*ps.UnusedAttributes = append(*ps.UnusedAttributes, k)
			}
		}
	}
	return ps, nil
}

// readSizes answers every project's latest size, by project id: the rows
// the daily pass stored in server_stats on the newest day it measured, read
// in one query however many projects there are. A project with no row that
// day had no data left to measure and is absent, even if an earlier day
// measured it.
func (h *host) readSizes(ctx context.Context) (map[int64]*statsSize, error) {
	res, err := h.run(ctx, `SELECT key, project_id, value, measured_at FROM server_stats
		WHERE key IN (?1, ?2) AND measured_at = (SELECT MAX(measured_at) FROM server_stats WHERE key IN (?1, ?2))`,
		store.StatRawBytes, store.StatAggregateBytes)
	if err != nil {
		return nil, err
	}
	out := map[int64]*statsSize{}
	for _, r := range res.Rows {
		id, _ := strconv.ParseInt(r[1], 10, 64)
		n, _ := strconv.ParseInt(r[2], 10, 64)
		sz := out[id]
		if sz == nil {
			sz = &statsSize{MeasuredAt: r[3]}
			out[id] = sz
		}
		switch r[0] {
		case store.StatRawBytes:
			sz.RawBytes = n
		case store.StatAggregateBytes:
			sz.AggregateBytes = n
		}
	}
	for _, sz := range out {
		sz.TotalBytes = sz.RawBytes + sz.AggregateBytes
	}
	return out, nil
}

// storedDay is what the daily pass stored for a project and day.
type storedDay struct {
	views, events, measures int64
	totalBytes              *int64
}

// stored is the daily pass's rows for a range, read in one query.
type stored struct {
	// countedBefore is the newest pass's day: it counted every day before
	// it, so those days' counts are stored (a day with nothing has no row
	// and counts 0). "" before any pass.
	countedBefore string
	days          map[int64]map[string]*storedDay
	database      map[string]int64
}

// readStored reads the counts, the project sizes (raw and aggregate summed)
// and the database's size stored for every day of the range.
func (h *host) readStored(ctx context.Context, fromD, toD civil.Date) (stored, error) {
	st := stored{days: map[int64]map[string]*storedDay{}, database: map[string]int64{}}
	res, err := h.run(ctx, `SELECT COALESCE(MAX(measured_at), '') FROM server_stats WHERE key = ? AND project_id = 0`, store.StatDatabaseBytes)
	if err != nil {
		return stored{}, err
	}
	st.countedBefore = res.Rows[0][0]
	res, err = h.run(ctx, `SELECT key, project_id, measured_at, value FROM server_stats
		WHERE key IN (?1, ?2, ?3, ?4, ?5, ?6) AND measured_at BETWEEN ?7 AND ?8`,
		store.StatViews, store.StatEvents, store.StatMeasures, store.StatRawBytes, store.StatAggregateBytes, store.StatDatabaseBytes,
		fromD.String(), toD.String())
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
		if st.days[id] == nil {
			st.days[id] = map[string]*storedDay{}
		}
		d := st.days[id][r[2]]
		if d == nil {
			d = &storedDay{}
			st.days[id][r[2]] = d
		}
		switch r[0] {
		case store.StatViews:
			d.views = n
		case store.StatEvents:
			d.events = n
		case store.StatMeasures:
			d.measures = n
		case store.StatRawBytes, store.StatAggregateBytes:
			if d.totalBytes == nil {
				d.totalBytes = new(int64)
			}
			*d.totalBytes += n
		}
	}
	return st, nil
}
