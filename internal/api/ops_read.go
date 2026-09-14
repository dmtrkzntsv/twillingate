package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type host struct {
	db      *sql.DB
	reg     *manage.Registry
	ops     *manage.Ops
	timeout time.Duration
	maxRows int
	// publicURL is the collector's public base (PUBLIC_URL); snippets and
	// the integration guide are built from it. Empty means "unknown —
	// placeholder + tell the operator".
	publicURL string
	logger    *slog.Logger
}

var dayRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

type rangeIn struct {
	Project string `json:"project" jsonschema:"project alias; call list_projects first"`
	From    string `json:"from" jsonschema:"start day inclusive, YYYY-MM-DD"`
	To      string `json:"to" jsonschema:"end day inclusive, YYYY-MM-DD"`
}

// checkRange validates the shared inputs. Error text is written for a
// model to recover from (endpoint spec §10): an unknown project lists
// the valid aliases instead of just refusing.
func (h *host) checkRange(ctx context.Context, in rangeIn) error {
	if !dayRe.MatchString(in.From) || !dayRe.MatchString(in.To) {
		return invalidf("from and to must be YYYY-MM-DD, got %q and %q", in.From, in.To)
	}
	if h.reg.Snapshot(ctx).Project(in.Project) == nil {
		return h.unknownProjectErr(ctx, in.Project)
	}
	return nil
}

// unknownProjectErr lists the valid aliases so a model can recover
// (endpoint spec §10). Shared by checkRange and by tools that re-fetch
// the project after checkRange has already validated it, in case the
// registry reloaded in between (a nil project would otherwise panic on
// field access).
func (h *host) unknownProjectErr(ctx context.Context, alias string) error {
	s := h.reg.Snapshot(ctx)
	var aliases []string
	for _, p := range s.Projects() {
		aliases = append(aliases, p.Alias)
	}
	sort.Strings(aliases)
	return notFoundf("unknown project %q; valid aliases: %s", alias, strings.Join(aliases, ", "))
}

type tableOut struct {
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated,omitempty"`
	Note      string     `json:"note,omitempty"`
}

func (h *host) table(ctx context.Context, q string, args ...any) (tableOut, error) {
	cols, rows, truncated, err := queryRows(ctx, h.db, h.timeout, h.maxRows, q, args...)
	if err != nil {
		if ctx.Err() != nil || strings.Contains(err.Error(), "context deadline") {
			return tableOut{}, invalidf("query exceeded %s; narrow the date range", h.timeout)
		}
		return tableOut{}, err
	}
	out := tableOut{Columns: cols, Rows: rows, Truncated: truncated}
	if truncated {
		out.Note = fmt.Sprintf("truncated to %d rows; results are PARTIAL — narrow the range or raise the limit", h.maxRows)
	}
	return out, nil
}

// ---- list_projects ----

type projectOut struct {
	Alias          string                    `json:"alias"`
	Name           string                    `json:"name"`
	Identity       string                    `json:"identity"`
	Archived       bool                      `json:"archived,omitempty"`
	FirstWebDay    string                    `json:"first_web_day,omitempty"`
	LastWebDay     string                    `json:"last_web_day,omitempty"`
	FirstAppDay    string                    `json:"first_app_day,omitempty"`
	LastAppDay     string                    `json:"last_app_day,omitempty"`
	AllowedOrigins []string                  `json:"allowed_origins"`
	Retention      *config.RetentionOverride `json:"retention,omitempty"`
	Attributes     []string                  `json:"attributes,omitempty"`
}

type listProjectsOut struct {
	Projects []projectOut `json:"projects"`
}

func (h *host) listProjects(ctx context.Context, _ struct{}) (listProjectsOut, error) {
	var out listProjectsOut
	for _, p := range h.reg.Snapshot(ctx).Projects() {
		po := projectOut{
			Alias: p.Alias, Name: p.Name, Identity: p.Identity, Archived: p.Archived,
			AllowedOrigins: p.AllowedOrigins, Retention: p.Retention, Attributes: p.Attributes,
		}
		// coverage probe: cheap MIN/MAX over the stitch views
		for _, probe := range []struct {
			view  string
			first *string
			last  *string
		}{
			{"v_web_daily", &po.FirstWebDay, &po.LastWebDay},
			{"v_app_daily", &po.FirstAppDay, &po.LastAppDay},
		} {
			_, rows, _, err := queryRows(ctx, h.db, h.timeout, 1,
				`SELECT COALESCE(MIN(day),''), COALESCE(MAX(day),'') FROM `+probe.view+` WHERE project=?`, p.Alias)
			if err != nil {
				return out, err
			}
			if len(rows) == 1 {
				*probe.first, *probe.last = rows[0][0], rows[0][1]
			}
		}
		out.Projects = append(out.Projects, po)
	}
	return out, nil
}

// ---- web_overview ----

func (h *host) webOverview(ctx context.Context, in rangeIn) (tableOut, error) {
	if err := h.checkRange(ctx, in); err != nil {
		return tableOut{}, err
	}
	out, err := h.table(ctx, `SELECT day, visitors, pageviews, sessions, bounces, duration_sec,
		ROUND(CAST(bounces AS REAL)/MAX(sessions,1), 3) AS bounce_rate,
		CAST(duration_sec/MAX(sessions,1) AS INTEGER) AS avg_session_sec
		FROM v_web_daily WHERE project=? AND day BETWEEN ? AND ? ORDER BY day`,
		in.Project, in.From, in.To)
	return out, err
}

// ---- web_breakdown ----

// webDimensions maps the dimension enum to view + value column. The
// enum in the input schema is generated from this map, so tool text and
// behaviour cannot drift.
var webDimensions = map[string]struct{ view, col string }{
	"pages":     {"v_web_pages", "path"},
	"hosts":     {"v_web_hosts", "host"},
	"referrers": {"v_web_referrers", "source"},
	"countries": {"v_web_countries", "country"},
	"devices":   {"v_web_devices", "device"},
	"browsers":  {"v_web_browsers", "browser"},
	"os":        {"v_web_os", "os"},
	"utm":       {"v_web_utm", "utm_source"},
}

// breakdownIn embeds rangeIn. Verified during implementation (a
// tools/list call against web_breakdown) that the SDK's jsonschema
// inference promotes embedded struct fields exactly like encoding/json:
// project/from/to appear as top-level properties, not nested under a
// "rangeIn" key. See task-16-report.md.
type breakdownIn struct {
	rangeIn
	Dimension string `json:"dimension" jsonschema:"one of: pages, hosts, referrers, countries, devices, browsers, os, utm"`
	Limit     int    `json:"limit,omitempty" jsonschema:"top-N rows, default 20"`
}

func dimensionKeys(m map[string]struct{ view, col string }) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func (h *host) webBreakdown(ctx context.Context, in breakdownIn) (tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return tableOut{}, err
	}
	dim, ok := webDimensions[in.Dimension]
	if !ok {
		return tableOut{}, invalidf("unknown dimension %q; valid: %s",
			in.Dimension, dimensionKeys(webDimensions))
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	if in.Dimension == "utm" {
		out, err := h.table(ctx, `SELECT utm_source, utm_medium, utm_campaign,
			SUM(visitors) AS visitors, SUM(pageviews) AS pageviews
			FROM v_web_utm WHERE project=? AND day BETWEEN ? AND ?
			GROUP BY utm_source, utm_medium, utm_campaign
			ORDER BY visitors DESC LIMIT ?`, in.Project, in.From, in.To, limit)
		return out, err
	}
	out, err := h.table(ctx, `SELECT `+dim.col+` AS value,
		SUM(visitors) AS visitors, SUM(pageviews) AS pageviews
		FROM `+dim.view+` WHERE project=? AND day BETWEEN ? AND ?
		GROUP BY `+dim.col+` ORDER BY visitors DESC LIMIT ?`,
		in.Project, in.From, in.To, limit)
	return out, err
}

// ---- app_overview / app_breakdown ----

var appDimensions = map[string]struct{ view, col string }{
	"screens":   {"v_app_screens", "screen"},
	"versions":  {"v_app_versions", "app_version"},
	"os":        {"v_app_os", "os_version"},
	"devices":   {"v_app_devices", "device_model"},
	"countries": {"v_app_countries", "country"},
}

func (h *host) appOverview(ctx context.Context, in rangeIn) (tableOut, error) {
	if err := h.checkRange(ctx, in); err != nil {
		return tableOut{}, err
	}
	out, err := h.table(ctx, `SELECT day, actives, views, sessions, duration_sec
		FROM v_app_daily WHERE project=? AND day BETWEEN ? AND ? ORDER BY day`,
		in.Project, in.From, in.To)
	return out, err
}

func (h *host) appBreakdown(ctx context.Context, in breakdownIn) (tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return tableOut{}, err
	}
	dim, ok := appDimensions[in.Dimension]
	if !ok {
		return tableOut{}, invalidf("unknown dimension %q; valid: %s",
			in.Dimension, dimensionKeys(appDimensions))
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	out, err := h.table(ctx, `SELECT `+dim.col+` AS value,
		SUM(actives) AS actives, SUM(views) AS views
		FROM `+dim.view+` WHERE project=? AND day BETWEEN ? AND ?
		GROUP BY `+dim.col+` ORDER BY actives DESC LIMIT ?`,
		in.Project, in.From, in.To, limit)
	return out, err
}

// register exposes every operation once, for MCP and (where a route is
// declared) REST. Read operations carry ReadOnlyHint; management
// operations (ops_manage.go) do not.
func (h *host) register(r *registrar) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	no := false // DestructiveHint is *bool in the SDK; nothing here destroys
	write := &mcp.ToolAnnotations{DestructiveHint: &no}
	idem := &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}
	const p = "/api/projects/{project}"

	expose(r, spec{Name: "list_projects", Annotations: ro, Method: "GET", Path: "/api/projects",
		Description: "List projects with identity mode and data coverage. Call this first: every other tool takes a project alias from here. Projects with identity=identified support retention and identities; anonymous ones cannot (their visitor ids rotate daily)."},
		h.listProjects)
	expose(r, spec{Name: "web_overview", Annotations: ro, Method: "GET", Path: p + "/web/overview",
		Description: "Daily web traffic for one project: visitors, pageviews, sessions, bounces, duration, with derived bounce_rate and avg_session_sec. Data includes yesterday and today (live)."},
		h.webOverview)
	expose(r, spec{Name: "web_breakdown", Annotations: ro, Method: "GET", Path: p + "/web/breakdown",
		Description: "Top pages, hosts, referrers, countries, devices, browsers, os or utm for one project over a date range."},
		h.webBreakdown)
	expose(r, spec{Name: "app_overview", Annotations: ro, Method: "GET", Path: p + "/app/overview",
		Description: "Daily app usage for one project: active users, screen views, sessions, duration."},
		h.appOverview)
	expose(r, spec{Name: "app_breakdown", Annotations: ro, Method: "GET", Path: p + "/app/breakdown",
		Description: "Top screens, versions, os, devices or countries for one project's app traffic."},
		h.appBreakdown)
	expose(r, spec{Name: "product_events", Annotations: ro, Method: "GET", Path: p + "/product/events",
		Description: "Product events per day: count and unique users per event name, plus daily totals. Unconditional — no attribute declaration is required to see it."},
		h.productEvents)
	expose(r, spec{Name: "product_attributes", Annotations: ro, Method: "GET", Path: p + "/product/attributes",
		Description: "Attribute breakdowns for product events. The system dimensions $platform and $app_version are always included; a custom key only appears once the project declares it in attributes (see update_project)."},
		h.productAttributes)
	expose(r, spec{Name: "retention", Annotations: ro, Method: "GET", Path: p + "/retention",
		Description: "D1/D7/D30-style cohort curves for identified projects. Returns aggregated_through: cohorts after it are absent (refreshed 03:00 UTC), not zero. Anonymous projects have no retention by design."},
		h.retention)
	expose(r, spec{Name: "identities", Annotations: ro, Method: "GET", Path: p + "/identities",
		Description: "Per-user or per-group activity with display names. This surfaces personal data on identified projects."},
		h.identities)
	expose(r, spec{Name: "query", Annotations: ro, Method: "POST", Path: "/api/query",
		Description: "Escape hatch: run one read-only SELECT/WITH against the v_* views and agg_* tables. Read schema://views first for columns and caveats. Row-capped and time-limited; the connection is read-only at the driver level."},
		h.runQuery)

	expose(r, spec{Name: "create_project", Annotations: write, Method: "POST", Path: "/api/projects", Status: http.StatusCreated,
		Description: "Create a project and (by default) its first ingest key; returns a paste-ready embed snippet (confirm the collector hostname with the user). Set skip_key to suppress the key."},
		h.createProject)
	expose(r, spec{Name: "update_project", Annotations: write, Method: "PATCH", Path: "/api/projects/{alias}",
		Description: "Update a project's name, identity mode, allowed origins and/or declared product-event attributes (breakdown keys for flat-view columns and attribute rollups). Fields you omit are left unchanged (this is a merge, not a replace) — except allowed_origins, which if provided non-empty replaces the whole list; origins cannot be cleared to empty via this tool (clear origins via `twillingate config import`, an explicit empty allowed_origins list in the document). Switching to identity=identified starts storing user ids and names as given — privacy-significant, say so to the user before doing it."},
		h.updateProject)
	expose(r, spec{Name: "archive_project", Annotations: idem, Method: "POST", Path: "/api/projects/{alias}/archive",
		Description: "Archive a project: ingestion stops, data and dashboards keep working, fully reversible with restore_project. There is no delete over the API — deletion requires the CLI."},
		h.archiveProject)
	expose(r, spec{Name: "restore_project", Annotations: idem, Method: "POST", Path: "/api/projects/{alias}/restore",
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

	expose(r, spec{Name: "integration_guide", Annotations: ro, // MCP only
		Description: "Tailored integration instructions for one project and platform (web, spa, server, mobile), with the project's real ingest key, collector URL, identity-mode guidance and event examples baked in. Confirm the collector hostname with the user. Call after create_project; read docs://twillingate for depth."},
		h.integrationGuide)

	registerSchemaRoute(r)
}
