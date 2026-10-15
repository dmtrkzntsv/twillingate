package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type host struct {
	db *readsql.DB
	// subs reads the tables db refuses (CustomSQLRefused), for SQL the
	// server builds; never given user-written SQL.
	subs *readsql.DB
	reg  *manage.Registry
	ops  *manage.Ops
	rep  *reporting.Service
	// ingestURL is the collector's public base (INGEST_URL); snippets and
	// the integration guide are built from it. Empty means "unknown —
	// placeholder + tell the operator".
	ingestURL string
	logger    *slog.Logger
	// limits are the caps in force (limitsFrom); limits and cap_usage read them.
	limits []limitOut
	// rawDays is RETENTION_RAW_DAYS, the window limits' raw_events
	// count falls in.
	rawDays int
	// raw caches the raw events held (rawcount.go).
	raw *rawCount
	// ingestDisabled is INGEST_DISABLED, which limits reports.
	ingestDisabled bool
	// files runs SQL over the raw event files (archive.Reader); nil in a
	// process without an engine, where query's files source is refused.
	files Files
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

// filesTable runs one of the tools' own queries on the event files (DuckDB
// SQL over the tables of archive.Reader), scoped to in's project: q's
// :project, :from and :to are bound as BIGINT and DATEs, any other :name
// from vals. Every cell reads as SQLite's did (tw_cell: NULL as "", 3 not
// 3.0).
func (h *host) filesTable(ctx context.Context, in rangeIn, q string, vals map[string]enginesql.Value) (tableOut, error) {
	all := map[string]enginesql.Value{
		":project": {Type: "BIGINT", Text: strconv.FormatInt(in.ProjectID, 10)},
		":from":    {Type: "DATE", Text: in.From},
		":to":      {Type: "DATE", Text: in.To},
	}
	for k, v := range vals {
		all[k] = v
	}
	bound, args, err := enginesql.Bind(q, all)
	if err != nil {
		return tableOut{}, err
	}
	res, err := h.runFiles(ctx, archive.Scoped{SQL: enginesql.Text(bound), Args: args, MaxRows: h.db.MaxRows(), Project: in.ProjectID})
	if err != nil {
		return tableOut{}, err
	}
	out := tableOut{Columns: res.Columns, Rows: cellRows(res.Rows), Truncated: res.Truncated}
	if res.Truncated {
		out.Note = fmt.Sprintf("truncated to %d rows; results are PARTIAL — narrow the range or raise the limit", h.db.MaxRows())
	}
	return out, nil
}

// runFiles runs q on the event files under CONSOLE_QUERY_TIMEOUT, with the
// registry's rules, for the tools' own SQL: a timeout asks for a narrower
// range, any other failure reads as filesError words it.
func (h *host) runFiles(ctx context.Context, q archive.Scoped) (engine.Result, error) {
	if h.files == nil {
		return engine.Result{}, invalidf("the event files cannot be queried by this process")
	}
	timeout := h.db.Timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	events, attrs := h.reg.Snapshot(ctx).RuleLists()
	res, err := h.files.QueryScoped(ctx, q, archive.Rules{Events: events, Attributes: attrs})
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return engine.Result{}, invalidf("query exceeded %s; narrow the date range", timeout)
	case err != nil:
		return engine.Result{}, filesError(err, timeout)
	}
	return res, nil
}

// cellRows is an engine result's rows as text (cellText), never nil.
func cellRows(rows [][]any) [][]string {
	out := make([][]string, len(rows))
	for i, row := range rows {
		out[i] = make([]string, len(row))
		for j, v := range row {
			out[i][j] = cellText(v)
		}
	}
	return out
}

// ---- list_projects ----

type projectOut struct {
	ProjectID      int64    `json:"project_id"`
	Name           string   `json:"name"`
	Archived       bool     `json:"archived,omitempty"`
	ArchivedAt     string   `json:"archived_at,omitempty"`
	AllowedOrigins []string `json:"allowed_origins"`
	Attributes     []string `json:"attributes,omitempty"`
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
			AllowedOrigins: p.AllowedOrigins, Attributes: p.Attributes,
		})
	}
	return out, nil
}

// ---- views_overview ----

type overviewIn struct {
	rangeIn
	Kind string `json:"kind,omitempty" jsonschema:"optional: only this kind (web, app, cli, …); absent sums every kind per day"`
}

func (h *host) viewsOverview(ctx context.Context, in overviewIn) (tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return tableOut{}, err
	}
	kind, vals := "", map[string]enginesql.Value(nil)
	if in.Kind != "" {
		kind, vals = ` AND kind = :kind`, map[string]enginesql.Value{":kind": {Type: "VARCHAR", Text: in.Kind}}
	}
	// Visitors are distinct per day and kind, added up over a day's kinds;
	// sessions, bounces and durations are the sessions table's, as the
	// Views dashboard reads them.
	q := `WITH v AS (
  SELECT day, count(DISTINCT actor_id) AS visitors, count(*) AS views
  FROM views WHERE project_id = :project AND day BETWEEN :from AND :to` + kind + `
  GROUP BY day, kind
), vd AS (
  SELECT day, SUM(visitors) AS visitors, SUM(views) AS views FROM v GROUP BY day
), sd AS (
  SELECT day, count(*) AS sessions, count(*) FILTER (WHERE views = 1) AS bounces,
         COALESCE(SUM(duration_sec), 0) AS duration_sec
  FROM sessions WHERE project_id = :project AND day BETWEEN :from AND :to` + kind + `
  GROUP BY day
)
SELECT vd.day, vd.visitors, vd.views, COALESCE(sd.sessions, 0) AS sessions,
       COALESCE(sd.bounces, 0) AS bounces, COALESCE(sd.duration_sec, 0) AS duration_sec,
       ROUND(COALESCE(sd.bounces, 0)::DOUBLE / greatest(COALESCE(sd.sessions, 0), 1), 3) AS bounce_rate,
       CAST(COALESCE(sd.duration_sec, 0) // greatest(COALESCE(sd.sessions, 0), 1) AS BIGINT) AS avg_session_sec
FROM vd LEFT JOIN sd ON sd.day = vd.day
ORDER BY vd.day`
	return h.filesTable(ctx, in.rangeIn, q, vals)
}

// ---- views_breakdown ----

// viewsDimension is one breakdown of the views table: the columns it
// returns, each with its key expression (an absent value reads as the empty
// string, as SQLite's views stored it), and the views it leaves out, if any.
type viewsDimension struct {
	cols  []string
	exprs []string
	where string
}

// orEmpty is a nullable column as a key: NULL (not sent) reads as empty.
func orEmpty(col string) string { return "COALESCE(" + col + ", '')" }

var viewsDimensions = map[string]viewsDimension{
	"kinds":     {cols: []string{"kind"}, exprs: []string{orEmpty("kind")}},
	"paths":     {cols: []string{"path"}, exprs: []string{orEmpty("path")}},
	"hosts":     {cols: []string{"host"}, exprs: []string{orEmpty("host")}},
	"referrers": {cols: []string{"source"}, exprs: []string{orEmpty("referrer_source")}},
	"utm": {cols: []string{"utm_source", "utm_medium", "utm_campaign"},
		exprs: []string{orEmpty("utm_source"), orEmpty("utm_medium"), orEmpty("utm_campaign")},
		where: "(utm_source <> '' OR utm_medium <> '' OR utm_campaign <> '')"},
	"countries":    {cols: []string{"country"}, exprs: []string{orEmpty("country")}},
	"platforms":    {cols: []string{"platform"}, exprs: []string{orEmpty("platform")}},
	"os":           {cols: []string{"os", "os_version"}, exprs: []string{orEmpty("os"), orEmpty("os_version")}},
	"browsers":     {cols: []string{"browser", "browser_version"}, exprs: []string{orEmpty("browser"), orEmpty("browser_version")}},
	"app_versions": {cols: []string{"platform", "app_version"}, exprs: []string{orEmpty("platform"), "app_version"}, where: "app_version <> ''"},
	"devices":      {cols: []string{"device", "device_model"}, exprs: []string{orEmpty("device"), orEmpty("device_model")}},
	"displays": {cols: []string{"display"}, exprs: []string{"concat(display_width, 'x', display_height)"},
		where: "display_width > 0 AND display_height > 0"},
	"consent": {cols: []string{"consent"},
		exprs: []string{"CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END"}},
	"locales": {cols: []string{"browser_locale", "app_locale"}, exprs: []string{orEmpty("browser_locale"), orEmpty("app_locale")},
		where: "(browser_locale <> '' OR app_locale <> '')"},
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
	Limit     int    `json:"limit,omitempty" jsonschema:"top-N rows, default 20"`
}

func (h *host) viewsBreakdown(ctx context.Context, in breakdownIn) (tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return tableOut{}, err
	}
	dim, ok := viewsDimensions[in.Dimension]
	if !ok {
		return tableOut{}, invalidf("unknown dimension %q; valid: %s", in.Dimension, dimensionNames())
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	// Visitors are distinct per day and key, added up over the range, as
	// the views' breakdowns kept them.
	keys := make([]string, len(dim.cols))
	pos := make([]string, len(dim.cols))
	for i, c := range dim.cols {
		keys[i] = dim.exprs[i] + " AS " + c
		pos[i] = strconv.Itoa(i + 1)
	}
	where := ""
	if dim.where != "" {
		where = " AND " + dim.where
	}
	cols := strings.Join(dim.cols, ", ")
	q := `WITH d AS (
  SELECT ` + strings.Join(keys, ", ") + `, count(DISTINCT actor_id) AS visitors, count(*) AS views
  FROM views WHERE project_id = :project AND day BETWEEN :from AND :to` + where + `
  GROUP BY day, ` + strings.Join(pos, ", ") + `
)
SELECT ` + cols + `, SUM(visitors) AS visitors, SUM(views) AS views
FROM d GROUP BY ` + cols + `
ORDER BY SUM(visitors) DESC, SUM(views) DESC, ` + cols + `
LIMIT :limit`
	return h.filesTable(ctx, in.rangeIn, q, map[string]enginesql.Value{":limit": {Type: "BIGINT", Text: strconv.Itoa(limit)}})
}

// register exposes every operation once, for MCP and (where a route is
// declared) REST. Read operations carry ReadOnlyHint; management
// operations (ops_manage.go) do not.
func (h *host) register(r *registrar) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	// DestructiveHint is *bool in the SDK. Nothing here is destructive but
	// merge_event and merge_attribute (a merge cannot be split again) and
	// block_event and block_attribute (the next daily pass deletes the
	// aggregates), which ask the operator.
	no, yes := false, true
	write := &mcp.ToolAnnotations{DestructiveHint: &no}
	idem := &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}
	destroy := &mcp.ToolAnnotations{DestructiveHint: &yes}
	destroyIdem := &mcp.ToolAnnotations{DestructiveHint: &yes, IdempotentHint: true}
	const p = "/api/projects/{project_id}"

	expose(r, spec{Name: "list_projects", Annotations: ro, Method: "GET", Path: "/api/projects",
		Description: "List projects with id, name, allowed origins and declared attributes, in the order move_project sets. Call this first: every other tool takes a project_id from here."},
		h.listProjects)
	restOnly(r, spec{Name: "project_activity", Method: "GET", Path: "/api/project-activity",
		Description: "Per live project: last_event_day (the newest day with views, product events or measures, raw or rolled up; null with none) new_submissions (submissions to live forms the console hasn't read), forms (live forms) and draft_forms (live forms still drafts, waiting to be approved). For the console's badges; not an MCP tool."},
		h.projectActivity)
	expose(r, spec{Name: "limits", Annotations: ro, Method: "GET", Path: "/api/limits",
		Description: "The limits in force, grouped: retention (days raw events, aggregates, event files and archived items are kept), caps (ATTRIBUTE_VALUES_TOP_N, the values kept per views breakdown and day and per attribute key, event and day; users and groups per day; and ATTRIBUTE_BREAKDOWNS_MAX, the attributes all active projects declare together; 0 means no cap) and ingest (INGEST_LATE_DAYS, how far back a client timestamp may lie before it is clamped, then the wire format's fixed limits: body, batch, attributes, timestamps, measures). Each has a name, value, unit and description; settings also carry the environment variable and its default. Set in the server's environment, not here. Also raw_events: held, the raw events stored across every project and family (what the server's disk and dashboard scans follow), counted at most every 5 minutes (never more than 10 minutes old), and window_days, the raw window they fall in (RETENTION_RAW_DAYS); absent when the last count failed. And ingest_disabled: true when INGEST_DISABLED is set and every ingest route refuses events and form submissions."},
		h.listLimits)
	expose(r, spec{Name: "cap_usage", Annotations: ro, Method: "GET", Path: p + "/cap-usage",
		Description: "How a project's data meets the caps over a range (default the last 30 days, at most 400): per views breakdown, attribute key, and users/groups, the busiest day's values against the cap, days with data, days folded into (other) (users and groups: days that reached the cap), and the share of views, counts or samples folded. Days already rolled up keep only the kept values and their (other) rows, so values per day stay near the cap there (cap + 1 for single-key breakdowns and attributes, cap plus one per leading key for two-key breakdowns)."},
		h.capUsage)
	expose(r, spec{Name: "list_attributes", Annotations: ro, Method: "GET", Path: p + "/attributes",
		Description: "A project's attributes: the keys its product events and measures carried over a range (default the last 30 days, at most 400; only the raw window is kept, RETENTION_RAW_DAYS), with its breakdowns and attribute rules. Per key the events that carried it and max_values (the most distinct values one event (or measure) had on one day, which ATTRIBUTE_VALUES_TOP_N caps), both as the daily pass counted them, so today's arrive the night after (a key first received today has events 0 and max_values null); whether any event carried it (received), whether the project breaks it down (breakdown), whether block_attribute strips it (blocked), and its aliases, the keys renamed or merged into it. Lists the 500 busiest keys plus every breakdown key, every blocked key and every key others are renamed to (received false when none carried it); a key renamed away is listed only in its target's aliases. keys_total counts the distinct keys received. Also values_cap (ATTRIBUTE_VALUES_TOP_N), breakdowns_used (breakdown keys across active projects) and breakdowns_max (ATTRIBUTE_BREAKDOWNS_MAX, 0 = no limit). Switch a breakdown with update_project's attributes."},
		h.listAttributes)
	expose(r, spec{Name: "usage", Annotations: ro, Method: "GET", Path: "/api/usage",
		Description: "Usage per project over a range (default the last 30 days, at most 400): views, product events and measure samples per day (every day, zeros included) and in total, when the newest raw row arrived, the oldest day with data, raw and rolled-up day counts, an estimate of the disk the project's rows take (measured daily by the daily pass, which also runs at start: size is the latest, with the day it was measured_at, null until the first measurement; each series day carries that day's total_bytes, null on days not measured), and, with project_id only (null otherwise), declared attributes no event carried. Days before the newest daily pass read the counts it stored, which outlive the aggregates' retention; later days are counted live. Without project_id, every project. Each series day also carries the project's declared_attributes and, counted the night after while the day's rows are raw and kept once rolled up, the distinct attribute_keys and attribute_values received and the attribute_values_folded into (other); null on days not stored. Also the database's size now and per day (database_series, null on days not measured)."},
		h.usage)
	expose(r, spec{Name: "views_overview", Annotations: ro, Method: "GET", Path: p + "/views/overview",
		Description: "Daily views for one project: visitors (distinct per day and kind, added up), views, sessions, bounces, duration, with derived bounce_rate and avg_session_sec. Sums every kind (web, app, cli, …) unless kind is given. Read from the event files, today included."},
		h.viewsOverview)
	expose(r, spec{Name: "views_breakdown", Annotations: ro, Method: "GET", Path: p + "/views/breakdown",
		Description: "Top values for one dimension of a project's views over a date range, from the event files; visitors are distinct per day and value, added up: kinds, paths, hosts, referrers, utm, countries, platforms, os, browsers, app_versions, devices, displays, consent or locales. Two-key dimensions (os, browsers, app_versions, devices, locales) return both columns. os, browser and device values are lower-case closed vocabularies with other (outside the list) and unknown (not declared) as distinct floors; platform is the surface (web, ios, electron, …) and never empty. consent is given, none or unknown (sent without $consent, or stored before it existed); none also includes views sent before the visitor answered a consent prompt. locales pairs the browser's language ($browser_locale) with the product's ($app_locale), either empty when not sent; views that sent neither are left out. A value not sent reads as empty."},
		h.viewsBreakdown)
	expose(r, spec{Name: "product_events", Annotations: ro, Method: "GET", Path: p + "/product/events",
		Description: "Product events per day, from the event files: count and unique users per event name, plus daily totals. Unconditional — no attribute declaration is required to see it."},
		h.productEvents)
	expose(r, spec{Name: "product_attributes", Annotations: ro, Method: "GET", Path: p + "/product/attributes",
		Description: "Attribute breakdowns for product events, from the event files: count, unique users and unique groups (0 when no group was involved) per value, per event, per day. Every key the events carried breaks down, with no declaration needed: each custom attribute key, and the reserved keys $platform, $os, $app_version, $app_locale, $kind, $browser, $device, $browser_locale, $country (derived from the connection's IP), $host, $path, $referrer, $utm_source, $utm_medium, $utm_campaign, $os_version, $browser_version and $device_model."},
		h.productAttributes)
	expose(r, spec{Name: "measures", Annotations: ro, Method: "GET", Path: p + "/measures",
		Description: "Performance measures and Web Vitals, from the event files: per metric, samples, estimated count, mean and p50/p75/p95 from log-scale histograms (about ±2%). time is milliseconds, size bytes, number unitless. attr_key breaks each metric down by any key the measures carried, with no declaration needed: a custom attribute key or one of the reserved keys product_attributes lists ($browser, $path, $utm_source, …); a key none carried gives no rows."},
		h.measures)
	expose(r, spec{Name: "retention", Annotations: ro, Method: "GET", Path: p + "/retention",
		Description: "D1/D7/D30-style cohort curves from the event files, cohorted by how the actor was identified: actor=user or actor=install. Days before today (UTC) only: today is partial. Returns aggregated_through, the newest day counted: cohorts after it are absent, not zero. Empty for a project whose clients send neither $user_id nor $install_id."},
		h.retention)
	expose(r, spec{Name: "identities", Annotations: ro, Method: "GET", Path: p + "/identities",
		Description: "Per-user or per-group activity over the range, from the event files: per id, its actors and users (distinct per day, added up over the days), views and product events, busiest first, with the display name sent with its latest event. This surfaces personal data on projects whose clients send ids."},
		h.identities)
	expose(r, spec{Name: "query", Annotations: ro, Method: "POST", Path: "/api/query",
		Description: "Escape hatch: run one read-only SELECT/WITH. source \"files\" (the default) reads the raw events through DuckDB: the tables events, views, product, measures, sessions, identities and aliases, one attr_<key> column per attribute key; read schema://views first (docs://twillingate, \"Writing SQL against the event files\"). source \"sqlite\" reads the v_* views and agg_* tables: deprecated, removed in a later release. Row-capped and time-limited; read-only either way."},
		h.runQuery)

	expose(r, spec{Name: "create_project", Annotations: write, Method: "POST", Path: "/api/projects", Status: http.StatusCreated,
		Description: "Create a project and (by default) its first ingest key; returns a paste-ready embed snippet (confirm the collector hostname with the user). Set skip_key to suppress the key. Each declared attribute is a breakdown, counted against ATTRIBUTE_BREAKDOWNS_MAX across active projects; pick keys from list_attributes."},
		h.createProject)
	expose(r, spec{Name: "update_project", Annotations: write, Method: "PATCH", Path: "/api/projects/{project_id}",
		Description: "Update a project's name, allowed origins and/or declared product-event attributes (breakdown keys for flat-view columns and attribute rollups). Fields you omit are left unchanged; allowed_origins and attributes replace the whole list when given, and an explicit empty allowed_origins clears it. Each declared attribute is a breakdown, counted against ATTRIBUTE_BREAKDOWNS_MAX across active projects; pick keys from list_attributes."},
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
		Description: "Rename a product or measures event to a name not in use: rewrites its stored events and aggregates, and stores later events sent under the old name under the new one. Refused onto a name with events (merge_event folds one event into another) or a blocked name. Widget SQL naming the old event is not rewritten."},
		h.renameEvent)
	expose(r, spec{Name: "merge_event", Annotations: destroy, Method: "POST", Path: p + "/event-rules/merge",
		Description: "Merge a product or measures event into another of the same family: rewrites its stored events and aggregates under the target, and stores later events sent under the old name under the target. Cannot be undone; merged days' unique counts become an upper bound. The old name need not have events (an alias for an app version still in the field). Refused onto a blocked name. Widget SQL naming the old event is not rewritten."},
		h.mergeEvent)
	expose(r, spec{Name: "block_event", Annotations: destroyIdem, Method: "POST", Path: p + "/event-rules/block",
		Description: "Block a product or measures event: ingest accepts and drops it (with a warning), the next daily pass deletes its aggregates, and its stored raw events expire with the raw window. Nothing else is deleted. remove_event_rule unblocks."},
		h.blockEvent)
	expose(r, spec{Name: "remove_event_rule", Annotations: idem, Method: "POST", Path: p + "/event-rules/remove",
		Description: "Remove a project's rule for an event name: a rename stops mapping (history stays renamed), a block lets the event be stored again."},
		h.removeEventRule)
	expose(r, spec{Name: "rename_attribute", Annotations: write, Method: "POST", Path: p + "/attribute-rules/rename",
		Description: "Rename a custom attribute key of a project's product and measures events to a key not in use: rewrites its stored events, aggregates and usage, keeps its breakdown, and stores later events sent with the old key under the new one (the new key's value wins when an event carries both). Refused onto a key with data (merge_attribute folds one key into another) or a blocked key, and for $ keys. Widget SQL naming the old key is not rewritten."},
		h.renameAttribute)
	expose(r, spec{Name: "merge_attribute", Annotations: destroy, Method: "POST", Path: p + "/attribute-rules/merge",
		Description: "Merge a custom attribute key into another: rewrites its stored events (the target's value wins where an event carries both), aggregates and usage under the target, and stores later events sent with the old key under the target. Cannot be undone; merged days' unique counts become an upper bound. The old key need not have data (an alias for an app version still in the field). Refused onto a blocked key, and for $ keys. Widget SQL naming the old key is not rewritten."},
		h.mergeAttribute)
	expose(r, spec{Name: "block_attribute", Annotations: destroyIdem, Method: "POST", Path: p + "/attribute-rules/block",
		Description: "Block a custom attribute key: ingest strips it from events (with a warning) and stores the events without it, it stops being a breakdown, and the next daily pass deletes its aggregates; stored raw events keep it until their day leaves the raw window. Nothing else is deleted. remove_attribute_rule unblocks."},
		h.blockAttribute)
	expose(r, spec{Name: "remove_attribute_rule", Annotations: idem, Method: "POST", Path: p + "/attribute-rules/remove",
		Description: "Remove a project's rule for an attribute key: a rename stops mapping (history stays renamed), a block lets the key be stored again."},
		h.removeAttributeRule)
	restOnly(r, spec{Name: "event_names", Method: "GET", Path: p + "/event-names",
		Description: "A project's product and measures event names in a range with their event counts, the busiest 200, busiest first. For the console's Settings page; not an MCP tool (product_events and measures answer agents)."},
		h.eventNames)

	expose(r, spec{Name: "integration_guide", Annotations: ro, // MCP only
		Description: "Tailored integration instructions for one project and platform (web, spa, server, mobile), with the project's real ingest key, collector URL and event examples baked in. Confirm the collector hostname with the user. Call after create_project; read docs://twillingate for depth."},
		h.integrationGuide)

	h.registerForms(r)
	h.registerReporting(r)
	registerSchemaRoute(r)
}
