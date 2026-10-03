package api

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
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
	UnusedAttributes *[]string   `json:"unused_attributes" jsonschema:"declared keys no event carried in the range; computed only when project_id is given, null otherwise"`
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
	if res, err := h.db.Run(ctx, `SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`); err == nil && len(res.Rows) == 1 {
		out.DatabaseBytes, _ = strconv.ParseInt(res.Rows[0][0], 10, 64)
	}
	sizes, _ := h.sizes.get(ctx, h.db, h.logger)
	for _, p := range projects {
		ps, err := h.statsFor(ctx, p, fromD, toD, in.ProjectID != 0)
		if err != nil {
			return statsOut{}, err
		}
		if sizes != nil {
			ps.Size = sizes.size(p.ID)
		}
		out.Projects = append(out.Projects, ps)
	}
	return out, nil
}

// statsFor reads one project's usage. withUnused also computes the
// declared attributes no event carried: two view scans per project, so
// only the one-project answer pays for it.
func (h *host) statsFor(ctx context.Context, p *manage.Project, fromD, toD civil.Date, withUnused bool) (projectStats, error) {
	ps := projectStats{ProjectID: p.ID}
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

// tableBytesSQL reads each table's bytes, its indexes included. dbstat
// walks every page, so sizeCache keeps the answer. A var so a test can
// make it fail.
var tableBytesSQL = `SELECT s.tbl_name, SUM(d.pgsize)
	FROM (SELECT name, pgsize FROM dbstat WHERE aggregate = TRUE) d
	JOIN sqlite_schema s ON s.name = d.name
	GROUP BY s.tbl_name`

// tableSizes is one reading: every table holding project rows, with its
// bytes and its rows per project.
type tableSizes struct {
	tables []tableSize
}

type tableSize struct {
	name  string
	raw   bool // events: raw rows; everything else is an aggregate
	bytes int64
	rows  int64
	per   map[int64]int64
}

// size is a project's share of each table's bytes by its share of rows.
func (s *tableSizes) size(projectID int64) *statsSize {
	out := &statsSize{}
	for _, t := range s.tables {
		if t.rows == 0 {
			continue
		}
		b := t.bytes * t.per[projectID] / t.rows
		if t.raw {
			out.RawBytes += b
		} else {
			out.AggregateBytes += b
		}
	}
	out.TotalBytes = out.RawBytes + out.AggregateBytes
	return out
}

// failTTL is how long a failed reading is remembered: a database that
// cannot read dbstat would otherwise be asked on every call.
const failTTL = time.Minute

type sizeCache struct {
	ttl    time.Duration
	mu     sync.Mutex
	at     time.Time
	val    *tableSizes
	failAt time.Time // when the last reading failed; zero after a success
	fail   error
	loads  int // readings taken; tests check the cache is used
}

func newSizeCache(ttl time.Duration) *sizeCache { return &sizeCache{ttl: ttl} }

// get answers the cached reading, or takes a new one when it is older than
// ttl. A failed reading is remembered for failTTL: the error is answered
// at once in that window, and logged once, when it happens.
func (c *sizeCache) get(ctx context.Context, db *readsql.DB, logger *slog.Logger) (*tableSizes, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.val != nil && time.Since(c.at) < c.ttl {
		return c.val, nil
	}
	if c.fail != nil && time.Since(c.failAt) < failTTL {
		return nil, c.fail
	}
	c.loads++
	val, err := readTableSizes(ctx, db)
	if err != nil {
		logger.Warn("project sizes unavailable", "error", err)
		if ctx.Err() == nil { // a caller that gave up says nothing about the database
			c.fail, c.failAt = err, time.Now()
		}
		return nil, err
	}
	c.val, c.at, c.fail = val, time.Now(), nil
	return val, nil
}

func readTableSizes(ctx context.Context, db *readsql.DB) (*tableSizes, error) {
	res, err := db.Run(ctx, tableBytesSQL)
	if err != nil {
		return nil, err
	}
	bytes := map[string]int64{}
	for _, r := range res.Rows {
		bytes[r[0]], _ = strconv.ParseInt(r[1], 10, 64)
	}
	// A project's data: its raw rows (events, read through v_events_flat,
	// the raw read path for every family) and every rollup keyed by
	// project_id. Dashboards and the registry are not data.
	res, err = db.Run(ctx, `SELECT m.name FROM sqlite_schema m, pragma_table_info(m.name) c
		WHERE m.type = 'table' AND c.name = 'project_id'
		  AND (m.name = 'events' OR m.name LIKE 'agg\_%' ESCAPE '\' OR m.name IN ('actors', 'identities'))
		ORDER BY m.name`)
	if err != nil {
		return nil, err
	}
	out := &tableSizes{}
	for _, r := range res.Rows {
		name := r[0]
		src := name
		if name == "events" {
			src = "v_events_flat"
		}
		if strings.ContainsAny(src, "\"'` ") {
			continue // never quote-splice an odd name
		}
		counts, err := db.Run(ctx, `SELECT project_id, COUNT(*) FROM "`+src+`" GROUP BY project_id`)
		if err != nil {
			return nil, err
		}
		t := tableSize{name: name, raw: name == "events", bytes: bytes[name], per: map[int64]int64{}}
		for _, c := range counts.Rows {
			id, _ := strconv.ParseInt(c[0], 10, 64)
			n, _ := strconv.ParseInt(c[1], 10, 64)
			t.per[id] = n
			t.rows += n
		}
		out.tables = append(out.tables, t)
	}
	return out, nil
}
