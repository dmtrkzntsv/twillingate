package api

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type host struct {
	db *readsql.DB
	// subs reads submissions, for SQL the server builds; never given
	// user-written SQL.
	subs *readsql.DB
	reg  *manage.Registry
	ops  *manage.Ops
	rep  *reporting.Service
	// ingestURL is the collector's public base (INGEST_URL); snippets and
	// the integration guide are built from it. Empty means "unknown —
	// placeholder + tell the operator".
	ingestURL string
	logger    *slog.Logger
	// limits are the limits in force (limitsFrom), which limits reports.
	limits []limitOut
	// events caches the last 30 days' events (eventcount.go), counted
	// natively; nil when native is, where limits leaves events_30d out.
	events *eventCount
	// ingestDisabled is INGEST_DISABLED, which limits reports.
	ingestDisabled bool
	// sql runs the query tool's SQL on the external SQL engine, its only
	// use; nil in a process without one, where that SQL is refused
	// (reporting.ErrSQLEngine).
	sql SQL
	// native answers the query tool's structured queries and the
	// analytics tools over the event files; nil in a process that cannot
	// read them, where those refuse.
	native Native
	// eventsDir is EVENTS_DIR, whose file names say each project's days.
	eventsDir string
	// queryTimeout and queryRows are CONSOLE_QUERY_TIMEOUT and
	// CONSOLE_QUERY_ROWS_MAX, which every query on the files runs under.
	queryTimeout time.Duration
	queryRows    int
	// keepalive is how often POST /api/widgets/stream sends a keepalive
	// comment; 0 is sseKeepalive. Tests shorten it.
	keepalive time.Duration
}

var dayRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// isDay reports a calendar date as YYYY-MM-DD: the tools cast from and to
// to DATE, which refuses a day like 2026-02-30.
func isDay(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil && dayRe.MatchString(s)
}

type rangeIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	From      string `json:"from" jsonschema:"start day inclusive, YYYY-MM-DD"`
	To        string `json:"to" jsonschema:"end day inclusive, YYYY-MM-DD"`
}

// checkRange validates the shared inputs. Error text is written for a
// model to recover from (endpoint spec §10): an unknown project lists
// the valid ids and names instead of just refusing.
func (h *host) checkRange(ctx context.Context, in rangeIn) error {
	if !isDay(in.From) || !isDay(in.To) {
		return invalidf("from and to must be YYYY-MM-DD, got %q and %q", in.From, in.To)
	}
	if h.reg.Snapshot(ctx).Project(in.ProjectID) == nil {
		return h.unknownProjectErr(ctx, in.ProjectID)
	}
	return nil
}

// unknownProjectErr lists the valid ids with their names so a model can
// recover (endpoint spec §10) without a second list_projects call. Shared
// by checkRange and by tools that re-fetch the project after checkRange
// has already validated it, in case the registry reloaded in between (a
// nil project would otherwise panic on field access).
func (h *host) unknownProjectErr(ctx context.Context, id int64) error {
	var choices []string
	for _, p := range h.reg.Snapshot(ctx).Projects() {
		choices = append(choices, fmt.Sprintf("%d (%s)", p.ID, p.Name))
	}
	return notFoundf("unknown project %d; valid projects: %s", id, strings.Join(choices, ", "))
}

type tableOut struct {
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated,omitempty"`
	Note      string     `json:"note,omitempty"`
}

// ---- list_projects ----

type projectOut struct {
	ProjectID      int64    `json:"project_id"`
	Name           string   `json:"name"`
	Archived       bool     `json:"archived,omitempty"`
	ArchivedAt     string   `json:"archived_at,omitempty"`
	AllowedOrigins []string `json:"allowed_origins"`
}

type listProjectsOut struct {
	Projects []projectOut `json:"projects"`
}

// listProjects reads the registry snapshot and nothing else: the web
// app waits on it before loading any widget.
func (h *host) listProjects(ctx context.Context, _ struct{}) (listProjectsOut, error) {
	// An empty registry lists as [], never null: the console reads .projects.filter.
	out := listProjectsOut{Projects: []projectOut{}}
	for _, p := range h.reg.Snapshot(ctx).Projects() {
		out.Projects = append(out.Projects, projectOut{
			ProjectID: p.ID, Name: p.Name, Archived: p.Archived, ArchivedAt: p.ArchivedAt,
			AllowedOrigins: p.AllowedOrigins,
		})
	}
	return out, nil
}

// ---- views_overview ----

type overviewIn struct {
	rangeIn
	Kind string `json:"kind,omitempty" jsonschema:"optional: only this kind (web, app, cli, …); absent sums every kind per day"`
}

// viewsOverview answers two native breakdowns by day, joined here: the
// views' visitors (distinct per day) and views, and the sessions'
// sessions, bounces and seconds, as the Views dashboard reads them. A day
// with views but no session reads 0 sessions, bounces and seconds, a
// bounce_rate of 0; avg_session_sec is the seconds over the sessions,
// rounded down.
func (h *host) viewsOverview(ctx context.Context, in overviewIn) (tableOut, error) {
	// One deadline for every query the answer runs.
	ctx, cancel := context.WithTimeout(ctx, h.queryTimeout)
	defer cancel()
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return tableOut{}, err
	}
	p, ok := rangeParams(in.rangeIn)
	cols := []string{"day", "visitors", "views", "sessions", "bounces", "duration_sec", "bounce_rate", "avg_session_sec"}
	if !ok {
		return tableOut{Columns: cols, Rows: [][]string{}}, nil
	}
	var where []native.Filter
	if in.Kind != "" {
		where = []native.Filter{{Column: "kind", Op: "eq", Values: []string{in.Kind}}}
	}
	limit := h.rowCap()
	// One call: the views and the sessions are two scans, side by side.
	ans := h.answerAll(ctx, []native.Request{
		{Query: native.Query{Kind: "breakdown", Table: "views", Dimensions: []string{"day"}, Order: "day",
			Limit: min(limit+1, maxBreakdownLimit), Where: where, Metrics: []native.Metric{
				{Op: "daily_sum", Of: "actors", As: "visitors"}, {Op: "count", As: "views"}}}, Params: p},
		{Query: native.Query{Kind: "breakdown", Table: "sessions", Dimensions: []string{"day"}, Order: "day",
			Limit: limit, Where: where, Metrics: []native.Metric{
				{Op: "count", As: "sessions"}, {Op: "bounces"}, {Op: "sum", Column: "duration_sec", As: "duration_sec"}}}, Params: p},
	})
	for _, a := range ans {
		if a.Err != nil {
			return tableOut{}, a.Err
		}
	}
	views, sessions := ans[0].Result, ans[1].Result
	cut := len(views.Rows) > limit
	if cut {
		views.Rows = views.Rows[:limit]
	}
	byDay := make(map[string][]string, len(sessions.Rows))
	for _, r := range sessions.Rows {
		byDay[r[0]] = r[1:]
	}
	out := tableOut{Columns: cols, Rows: make([][]string, 0, len(views.Rows))}
	for _, r := range views.Rows {
		n, bounces, secs := int64(0), int64(0), int64(0)
		if s, ok := byDay[r[0]]; ok {
			n, bounces, secs = cellInt(s[0]), cellInt(s[1]), cellInt(s[2])
		}
		out.Rows = append(out.Rows, []string{r[0], r[1], r[2], strconv.FormatInt(n, 10), strconv.FormatInt(bounces, 10),
			strconv.FormatInt(secs, 10), roundRate(bounces, max(n, 1)), strconv.FormatInt(secs/max(n, 1), 10)})
	}
	// At the engine's 1,000 there is no asking for one more day.
	h.markPartial(&out, views.Truncated || sessions.Truncated || cut || len(views.Rows) == maxBreakdownLimit && limit < rangeDays(p),
		append(views.Approximate, sessions.Approximate...))
	return out, nil
}

// maxBreakdownLimit is a native breakdown's most groups.
const maxBreakdownLimit = 1000

// rowCap is the most rows a tool answering natively returns:
// CONSOLE_QUERY_ROWS_MAX (0 or below for none) within the engine's
// 1,000 groups.
func (h *host) rowCap() int {
	if h.queryRows <= 0 {
		return maxBreakdownLimit
	}
	return min(h.queryRows, maxBreakdownLimit)
}

// rangeParams is in's project and days as a native query's, ok false
// when to is before from: a range of no day, which answers no row.
func rangeParams(in rangeIn) (native.Params, bool) {
	from, err1 := civil.Parse(in.From)
	to, err2 := civil.Parse(in.To)
	if err1 != nil || err2 != nil || to.Before(from) {
		return native.Params{}, false
	}
	return native.Params{Project: in.ProjectID, From: from, To: to}, true
}

// rangeDays is the days of p's range, from and to included.
func rangeDays(p native.Params) int {
	return int(p.To.Time().Sub(p.From.Time()).Hours()/24) + 1
}

// cellInt is a native integer cell; an empty one (NULL) is 0.
func cellInt(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// roundRate is num/den rounded to 3 places, half away from zero, as the
// tools' SQL printed ROUND(… ::DOUBLE, 3): the shortest decimal (1, 0.25).
func roundRate(num, den int64) string {
	return strconv.FormatFloat(math.Round(float64(num)/float64(den)*1000)/1000, 'f', -1, 64)
}

// markPartial notes on out what the tools' SQL noted: a table cut short,
// and (new with the native engine) the columns holding an estimate.
func (h *host) markPartial(out *tableOut, truncated bool, approximate []string) {
	var notes []string
	if truncated {
		out.Truncated = true
		notes = append(notes, fmt.Sprintf("truncated to %d rows; results are PARTIAL — narrow the range or raise the limit", len(out.Rows)))
	}
	if len(approximate) > 0 {
		approximate = slices.Clone(approximate)
		slices.Sort(approximate)
		notes = append(notes, fmt.Sprintf("%s past 1,024 distinct ids are estimated (about ±2%%)", strings.Join(slices.Compact(approximate), ", ")))
	}
	out.Note = strings.Join(notes, "; ")
}

// ---- views_breakdown ----

// viewsDimension is one breakdown of the views table: the native
// dimensions it groups by, the columns it returns them as, and the filter
// that leaves views out, if any. A value not sent (NULL) reads as the
// empty string, as the old SQL's COALESCE read it: the two agree
// because the files never hold an empty string (archive's log leaves
// empty fields out), so NULL and the empty string never split one value
// in two.
type viewsDimension struct {
	dims  []string
	cols  []string
	where []native.Filter
}

// isSet keeps the views whose column is set.
func isSet(col string) []native.Filter { return []native.Filter{{Column: col, Op: "is_set"}} }

// anySet keeps the views with any of cols set.
func anySet(cols ...string) []native.Filter {
	return []native.Filter{{Op: "any_set", Columns: cols}}
}

// same is a dimension returned under its own name.
func same(dims ...string) viewsDimension { return viewsDimension{dims: dims, cols: dims} }

var viewsDimensions = map[string]viewsDimension{
	"kinds":        same("kind"),
	"paths":        same("path"),
	"hosts":        same("host"),
	"referrers":    {dims: []string{"referrer_source"}, cols: []string{"source"}},
	"utm":          {dims: []string{"utm_source", "utm_medium", "utm_campaign"}, cols: []string{"utm_source", "utm_medium", "utm_campaign"}, where: anySet("utm_source", "utm_medium", "utm_campaign")},
	"countries":    same("country"),
	"platforms":    same("platform"),
	"os":           same("os", "os_version"),
	"browsers":     same("browser", "browser_version"),
	"app_versions": {dims: []string{"platform", "app_version"}, cols: []string{"platform", "app_version"}, where: isSet("app_version")},
	"devices":      same("device", "device_model"),
	"displays":     {dims: []string{"resolution"}, cols: []string{"display"}, where: isSet("resolution")},
	"consent":      {dims: []string{"consent_state"}, cols: []string{"consent"}},
	"locales":      {dims: []string{"browser_locale", "app_locale"}, cols: []string{"browser_locale", "app_locale"}, where: anySet("browser_locale", "app_locale")},
}

// dimensionNames lists the enum, sorted, for the schema text and errors.
func dimensionNames() string {
	keys := make([]string, 0, len(viewsDimensions))
	for k := range viewsDimensions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

type breakdownIn struct {
	rangeIn
	Dimension string `json:"dimension" jsonschema:"one of: app_versions, browsers, consent, countries, devices, displays, hosts, kinds, locales, os, paths, platforms, referrers, utm"`
	Limit     int    `json:"limit,omitempty" jsonschema:"top-N rows, default 20, at most 1000"`
}

// viewsBreakdown answers a native breakdown of the views by the
// dimension's columns: visitors (distinct per day and value, added up)
// and views, the busiest by visitors, then views, then the values.
func (h *host) viewsBreakdown(ctx context.Context, in breakdownIn) (tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return tableOut{}, err
	}
	dim, ok := viewsDimensions[in.Dimension]
	if !ok {
		return tableOut{}, invalidf("unknown dimension %q; valid: %s", in.Dimension, dimensionNames())
	}
	cols := append(slices.Clone(dim.cols), "visitors", "views")
	p, ok := rangeParams(in.rangeIn)
	if !ok {
		return tableOut{Columns: cols, Rows: [][]string{}}, nil
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	n := min(limit, h.rowCap())
	keys := len(dim.dims)
	res, more, tied, err := h.topRows(ctx, native.Query{Kind: "breakdown", Table: "views", Dimensions: dim.dims, Order: "visitors",
		Where: dim.where, Metrics: []native.Metric{{Op: "daily_sum", Of: "actors", As: "visitors"}, {Op: "count", As: "views"}}},
		p, n, func(a, b []string) int {
			if c := cmp.Compare(cellInt(b[keys]), cellInt(a[keys])); c != 0 {
				return c
			}
			if c := cmp.Compare(cellInt(b[keys+1]), cellInt(a[keys+1])); c != 0 {
				return c
			}
			return slices.Compare(a[:keys], b[:keys])
		})
	if err != nil {
		return tableOut{}, err
	}
	out := tableOut{Columns: cols, Rows: res.Rows}
	h.markPartial(&out, res.Truncated || more && n < limit || tied, res.Approximate)
	return out, nil
}

// topRows answers q, a breakdown ranked by its order metric, with its n
// first rows in the order less sorts them: the tool's own, which breaks
// the ties on the metric otherwise than the engine (by the dimensions).
// It asks for the engine's most groups (1,000) at once, which costs no
// more than fewer (a breakdown aggregates every group anyway), sorts them
// and keeps n. more says whether groups past the n were left out; tied
// that the groups tied with the n-th ran past the 1,000, so the n kept
// may not be the ones the tool's order puts first.
func (h *host) topRows(ctx context.Context, q native.Query, p native.Params, n int,
	less func(a, b []string) int) (res native.Result, more, tied bool, err error) {
	q.Limit = maxBreakdownLimit
	if res, err = h.answer(ctx, q, p); err != nil {
		return native.Result{}, false, false, err
	}
	if rows, rank := res.Rows, slices.Index(res.Columns, q.Order); len(rows) == maxBreakdownLimit && n < len(rows) {
		tied = rows[n-1][rank] == rows[len(rows)-1][rank]
	}
	slices.SortStableFunc(res.Rows, less)
	if res.Rows == nil {
		res.Rows = [][]string{}
	}
	if len(res.Rows) > n {
		res.Rows, more = res.Rows[:n], true
	}
	return res, more, tied, nil
}

// register exposes every operation once, for MCP and (where a route is
// declared) REST. Read operations carry ReadOnlyHint; management
// operations (ops_manage.go) do not.
func (h *host) register(r *registrar) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	// DestructiveHint is *bool in the SDK. Nothing here is destructive but
	// merge_event and merge_attribute (a merge cannot be split again) and
	// block_event and block_attribute, which ask the operator.
	no, yes := false, true
	write := &mcp.ToolAnnotations{DestructiveHint: &no}
	idem := &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}
	destroy := &mcp.ToolAnnotations{DestructiveHint: &yes}
	destroyIdem := &mcp.ToolAnnotations{DestructiveHint: &yes, IdempotentHint: true}
	const p = "/api/projects/{project_id}"

	expose(r, spec{Name: "list_projects", Annotations: ro, Method: "GET", Path: "/api/projects",
		Description: "List projects with id, name and allowed origins, in the order move_project sets. Call this first: every other tool takes a project_id from here."},
		h.listProjects)
	restOnly(r, spec{Name: "project_activity", Method: "GET", Path: "/api/project-activity",
		Description: "Per live project: last_event_day (the newest day file in the event files, a month file counting as its last day; null with none), new_submissions (submissions to live forms the console hasn't read), forms (live forms) and draft_forms (live forms still drafts, waiting to be approved). For the console's badges; not an MCP tool."},
		h.projectActivity)
	expose(r, spec{Name: "limits", Annotations: ro, Method: "GET", Path: "/api/limits",
		Description: "The limits in force, grouped: retention (days the event files, archived items and form drafts are kept) and ingest (INGEST_LATE_DAYS, how far back a client timestamp may lie before it is clamped, then the wire format's fixed limits: body, batch, attributes, timestamps, measures). Each has a name, value, unit and description; settings also carry the environment variable and its default. Set in the server's environment, not here. Also events_30d: count, the events in the event files from from through to (the 30 days ending today, UTC), of every family and project, counted at most every 5 minutes (never more than 10 minutes old); absent when the last count failed. And ingest_disabled: true when INGEST_DISABLED is set and every ingest route refuses events and form submissions."},
		h.listLimits)
	expose(r, spec{Name: "list_attributes", Annotations: ro, Method: "GET", Path: p + "/attributes",
		Description: "A project's attributes, from the event files with the attribute rules applied: the keys its product events and measures carried over a range (default the last 30 days, at most 400). Per key the events that carried it and its distinct values over the range, whether any event carried it (received), whether block_attribute hides it (blocked), and its aliases, the keys renamed or merged into it. Lists the 500 busiest keys plus every key a rule names (received false when none carried it; a blocked key always); a key renamed away reads as its target and is listed only in its aliases. keys_total counts the distinct keys received. A key's values past 1,024 are estimated (about ±2%); a range with more keys than one pass counts is refused."},
		h.listAttributes)
	expose(r, spec{Name: "usage", Annotations: ro, Method: "GET", Path: "/api/usage",
		Description: "Usage per project over a range (default the last 30 days, at most 400), from the event files: views, product events and measure samples per day (every day, zeros included) and in total, when the newest event arrived (whatever the range), the oldest day the files hold, and the bytes the project's event files take (measured by the daily pass of the process that keeps them, which also runs at start: size is the latest, with the day it was measured_at, null until the first measurement; each series day carries that day's total_bytes, null on days not measured). Without project_id, every project. Also the database's size now and per day (database_series, null on days not measured)."},
		h.usage)
	expose(r, spec{Name: "views_overview", Annotations: ro, Method: "GET", Path: p + "/views/overview",
		Description: "Daily views for one project: visitors (distinct per day), views, sessions, bounces, duration, with derived bounce_rate and avg_session_sec. Counts every kind (web, app, cli, …) unless kind is given. Read from the event files, today included; a note names the columns holding an estimate (distinct counts past 1,024 ids a day, about ±2%)."},
		h.viewsOverview)
	expose(r, spec{Name: "views_breakdown", Annotations: ro, Method: "GET", Path: p + "/views/breakdown",
		Description: "Top values for one dimension of a project's views over a date range, from the event files, busiest by visitors, then views; visitors are distinct per day and value, added up: kinds, paths, hosts, referrers, utm, countries, platforms, os, browsers, app_versions, devices, displays, consent or locales. Two-key dimensions (os, browsers, app_versions, devices, locales) return both columns. os, browser and device values are lower-case closed vocabularies with other (outside the list) and unknown (not declared) as distinct floors; platform is the surface (web, ios, electron, …) and never empty. consent is given, none or unknown (sent without $consent, or stored before it existed); none also includes views sent before the visitor answered a consent prompt. locales pairs the browser's language ($browser_locale) with the product's ($app_locale), either empty when not sent; views that sent neither are left out. A value not sent reads as empty."},
		h.viewsBreakdown)
	expose(r, spec{Name: "product_events", Annotations: ro, Method: "GET", Path: p + "/product/events",
		Description: "Product events per day, from the event files: count and unique users per event name (the most events first within a day), plus daily totals. Unconditional — no attribute declaration is required to see it."},
		h.productEvents)
	expose(r, spec{Name: "product_attributes", Annotations: ro, Method: "GET", Path: p + "/product/attributes",
		Description: "Attribute breakdowns for product events, from the event files: count, unique users and unique groups (0 when no group was involved) per value, per event, per day, by day, the most events first, at most 1,000 rows, the earliest days' (a range the engine cannot hold is refused: narrow it or filter by event). Every key the events carried breaks down, with no declaration needed: each custom attribute key, and the reserved keys $platform, $os, $app_version, $app_locale, $kind, $browser, $device, $browser_locale, $country (derived from the connection's IP), $host, $path, $referrer, $utm_source, $utm_medium, $utm_campaign, $os_version, $browser_version and $device_model."},
		h.productAttributes)
	expose(r, spec{Name: "measures", Annotations: ro, Method: "GET", Path: p + "/measures",
		Description: "Performance measures and Web Vitals, from the event files: per metric, samples, estimated count, mean and p50/p75/p95 from log-scale histograms (about ±2%). time is milliseconds, size bytes, number unitless. attr_key breaks each metric down by any key the measures carried, with no declaration needed: a custom attribute key or one of the reserved keys product_attributes lists ($browser, $path, $utm_source, …); a key none carried gives no rows. At most 1,000 rows."},
		h.measures)
	expose(r, spec{Name: "retention", Annotations: ro, Method: "GET", Path: p + "/retention",
		Description: "D1/D7/D30-style cohort curves from the event files, cohorted by how the actor was identified: actor=user or actor=install. Per cohort day of the range, the actors back on each day up to 90 days on, by cohort, then day back. Days before today (UTC) only: today is partial. Returns aggregated_through, the newest day counted: cohorts after it are absent, not zero. Empty for a project whose clients send neither $user_id nor $install_id."},
		h.retention)
	expose(r, spec{Name: "identities", Annotations: ro, Method: "GET", Path: p + "/identities",
		Description: "Per-user or per-group activity over the range, from the event files: per id, its actors and users (distinct per day, added up over the days), views and product events, busiest first, with the display name sent with its latest event (empty when none was sent or it is the id itself). This surfaces personal data on projects whose clients send ids."},
		h.identities)
	expose(r, spec{Name: "query", Annotations: ro, Method: "POST", Path: "/api/query", constrain: h.constrainQuery,
		Description: h.queryDescription()},
		h.runQuery)

	expose(r, spec{Name: "create_project", Annotations: write, Method: "POST", Path: "/api/projects", Status: http.StatusCreated,
		Description: "Create a project and (by default) its first ingest key; returns a paste-ready embed snippet (confirm the collector hostname with the user). Set skip_key to suppress the key. Attributes need no declaring: every key the events carry breaks down in the event files."},
		h.createProject)
	expose(r, spec{Name: "update_project", Annotations: write, Method: "PATCH", Path: "/api/projects/{project_id}",
		Description: "Update a project's name and/or allowed origins. Fields you omit are left unchanged; allowed_origins replaces the whole list when given, and an explicit empty allowed_origins clears it."},
		h.updateProject)
	expose(r, spec{Name: "move_project", Annotations: idem, Method: "POST", Path: "/api/projects/{project_id}/move",
		Description: "Put a project right after another (after: a project id, archived ones included; 0 first) in the order list_projects, the console and the CLI show. A new project comes last."},
		h.moveProject)
	expose(r, spec{Name: "archive_project", Annotations: idem, Method: "POST", Path: "/api/projects/{project_id}/archive",
		Description: "Archive a project: ingestion stops, data and dashboards keep working. Reversible with restore_project — data kept, purged after RETENTION_ARCHIVED_DAYS (default 30) unless restored. There is no delete over the API — deletion requires the CLI."},
		h.archiveProject)
	expose(r, spec{Name: "restore_project", Annotations: idem, Method: "POST", Path: "/api/projects/{project_id}/restore",
		Description: "Restore an archived project."},
		h.restoreProject)
	expose(r, spec{Name: "list_ingest_keys", Annotations: ro, Method: "GET", Path: "/api/keys",
		Description: "List ingest keys with their state, including disabled ones."},
		h.listKeys)
	expose(r, spec{Name: "issue_ingest_key", Annotations: write, Method: "POST", Path: p + "/keys", Status: http.StatusCreated,
		Description: "Issue a new ingest key for a project. Ingest keys are public identifiers (they ship in page source); retirement is disable, not secrecy. Confirm the snippet's collector hostname with the user."},
		h.issueKey)
	expose(r, spec{Name: "disable_ingest_key", Annotations: idem, Method: "POST", Path: p + "/keys/{label}/disable",
		Description: "Disable an ingest key by project and label; events with it are rejected within a second. Reversible."},
		h.disableKey)
	expose(r, spec{Name: "enable_ingest_key", Annotations: idem, Method: "POST", Path: p + "/keys/{label}/enable",
		Description: "Re-enable a disabled ingest key."},
		h.enableKey)
	expose(r, spec{Name: "list_event_rules", Annotations: ro, Method: "GET", Path: p + "/event-rules",
		Description: "List a project's event rules: renames (name → to) and blocks, product and measures families."},
		h.listEventRules)
	expose(r, spec{Name: "rename_event", Annotations: write, Method: "POST", Path: p + "/event-rules/rename",
		Description: "Rename a product or measures event to a name not in use: every read of the event files shows its events under the new name, history included, and ingest stores later events sent under the old name under the new one. Refused onto a name the files already have events of, rules applied (merge_event folds one event into another), or a blocked name. Widget SQL naming the old event is not rewritten."},
		h.renameEvent)
	expose(r, spec{Name: "merge_event", Annotations: destroy, Method: "POST", Path: p + "/event-rules/merge",
		Description: "Merge a product or measures event into another of the same family: every read of the event files counts its events under the target, history included, and ingest stores later events sent under the old name under the target, which removing the rule does not split again. The old name need not have events (an alias for an app version still in the field). Refused onto a blocked name. Widget SQL naming the old event is not rewritten."},
		h.mergeEvent)
	expose(r, spec{Name: "block_event", Annotations: destroyIdem, Method: "POST", Path: p + "/event-rules/block",
		Description: "Block a product or measures event: ingest accepts and drops it (with a warning), and every read of the event files leaves its stored events out, history included. Nothing is deleted. remove_event_rule unblocks."},
		h.blockEvent)
	expose(r, spec{Name: "remove_event_rule", Annotations: idem, Method: "POST", Path: p + "/event-rules/remove",
		Description: "Remove a project's rule for an event name: reads of the event files stop applying it, so events stored before the rule show as sent again (ingest stored the later ones as the rule said), and a block lets the event be stored again."},
		h.removeEventRule)
	expose(r, spec{Name: "rename_attribute", Annotations: write, Method: "POST", Path: p + "/attribute-rules/rename",
		Description: "Rename a custom attribute key of a project's product and measures events to a key not in use: every read of the event files shows its values under the new key, history included, and ingest stores later events sent with the old key under the new one (the new key's value wins when an event carries both). Refused onto a key the files already have values of, rules applied (merge_attribute folds one key into another), or a blocked key, and for $ keys. Widget SQL naming the old key is not rewritten."},
		h.renameAttribute)
	expose(r, spec{Name: "merge_attribute", Annotations: destroy, Method: "POST", Path: p + "/attribute-rules/merge",
		Description: "Merge a custom attribute key into another: every read of the event files shows its values under the target, history included (the target's value wins where an event carries both), and ingest stores later events sent with the old key under the target, which removing the rule does not split again. The old key need not have data (an alias for an app version still in the field). Refused onto a blocked key, and for $ keys. Widget SQL naming the old key is not rewritten."},
		h.mergeAttribute)
	expose(r, spec{Name: "block_attribute", Annotations: destroyIdem, Method: "POST", Path: p + "/attribute-rules/block",
		Description: "Block a custom attribute key: ingest strips it from events (with a warning) and stores the events without it, and every read of the event files leaves its stored values out, history included. Nothing is deleted. remove_attribute_rule unblocks."},
		h.blockAttribute)
	expose(r, spec{Name: "remove_attribute_rule", Annotations: idem, Method: "POST", Path: p + "/attribute-rules/remove",
		Description: "Remove a project's rule for an attribute key: reads of the event files stop applying it, so events stored before the rule show the key as sent again (ingest stored the later ones as the rule said), and a block lets the key be stored again."},
		h.removeAttributeRule)
	restOnly(r, spec{Name: "event_names", Method: "GET", Path: p + "/event-names",
		Description: "A project's product and measures event names in a range with their event counts, the busiest 200, busiest first, ties by name. For the console's Settings page; not an MCP tool (product_events and measures answer agents)."},
		h.eventNames)

	expose(r, spec{Name: "integration_guide", Annotations: ro, // MCP only
		Description: "Tailored integration instructions for one project and platform (web, spa, server, mobile), with the project's real ingest key, collector URL and event examples baked in. Confirm the collector hostname with the user. Call after create_project; read docs://twillingate for depth."},
		h.integrationGuide)

	h.registerForms(r)
	h.registerReporting(r)
	registerSchemaRoute(r)
}

// queryDescription is the query tool's description: with an external SQL
// engine its sql mode too, without one only the structured query, so an
// agent is never offered SQL it would be refused.
func (h *host) queryDescription() string {
	if h.sql == nil {
		return "Ask the event files a question. query is the native structured language over one project (project_id, from, to: default the 30 days ending today): a stat (one row of metrics, optionally against the previous period), a trend (one metric per day, optionally split by a series) or a breakdown (metrics grouped by 1-3 dimensions), over events, views, product or measures, with filters; or a retention (output grid, curve or milestones: the cohorts of users and installs first active in the range and the days they came back, counted over the days before today); distinct counts past 1,024 ids per group are estimated (about ±2%) and named in approximate (docs://twillingate, \"Structured queries\"). Row-capped and time-limited; read-only."
	}
	return "Ask the event files a question, one of two ways. query is the native structured language over one project (project_id, from, to: default the 30 days ending today): a stat (one row of metrics, optionally against the previous period), a trend (one metric per day, optionally split by a series) or a breakdown (metrics grouped by 1-3 dimensions), over events, views, product or measures, with filters; or a retention (output grid, curve or milestones: the cohorts of users and installs first active in the range and the days they came back, counted over the days before today); distinct counts past 1,024 ids per group are estimated (about ±2%) and named in approximate (docs://twillingate, \"Structured queries\"). sql is the escape hatch, DuckDB SQL answered by the external SQL engine: one read-only SELECT/WITH over the tables events, views, product, measures, sessions, identities and aliases and nothing else (no table function but range, generate_series and unnest, no schema-qualified name such as main.events), one attr_<key> column per attribute key, every project's rows; read schema://views first (\"Writing SQL against the event files\"). Give sql or query, not both. Row-capped and time-limited; read-only."
}

// constrainQuery is queryEnums, and without an external SQL engine the
// input schema without sql (queryDescription).
func (h *host) constrainQuery(in *jsonschema.Schema) {
	queryEnums(in)
	if h.sql == nil {
		delete(in.Properties, "sql")
	}
}
