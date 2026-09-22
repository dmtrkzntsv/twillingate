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
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	From      string `json:"from" jsonschema:"start day inclusive, YYYY-MM-DD"`
	To        string `json:"to" jsonschema:"end day inclusive, YYYY-MM-DD"`
}

// checkRange validates the shared inputs. Error text is written for a
// model to recover from (endpoint spec §10): an unknown project lists
// the valid ids and names instead of just refusing.
func (h *host) checkRange(ctx context.Context, in rangeIn) error {
	if !dayRe.MatchString(in.From) || !dayRe.MatchString(in.To) {
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
	ProjectID      int64    `json:"project_id"`
	Name           string   `json:"name"`
	Identity       string   `json:"identity"`
	Archived       bool     `json:"archived,omitempty"`
	FirstViewDay   string   `json:"first_view_day,omitempty"`
	LastViewDay    string   `json:"last_view_day,omitempty"`
	AllowedOrigins []string `json:"allowed_origins"`
	Attributes     []string `json:"attributes,omitempty"`
}

type listProjectsOut struct {
	Projects []projectOut `json:"projects"`
}

func (h *host) listProjects(ctx context.Context, _ struct{}) (listProjectsOut, error) {
	var out listProjectsOut
	for _, p := range h.reg.Snapshot(ctx).Projects() {
		po := projectOut{
			ProjectID: p.ID, Name: p.Name, Identity: p.Identity, Archived: p.Archived,
			AllowedOrigins: p.AllowedOrigins, Attributes: p.Attributes,
		}
		// coverage probe: cheap MIN/MAX over the stitch view
		_, rows, _, err := queryRows(ctx, h.db, h.timeout, 1,
			`SELECT COALESCE(MIN(day),''), COALESCE(MAX(day),'') FROM v_views_daily WHERE project_id=?`, p.ID)
		if err != nil {
			return out, err
		}
		if len(rows) == 1 {
			po.FirstViewDay, po.LastViewDay = rows[0][0], rows[0][1]
		}
		out.Projects = append(out.Projects, po)
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
	q := `SELECT day, SUM(visitors) AS visitors, SUM(views) AS views, SUM(sessions) AS sessions,
		SUM(bounces) AS bounces, SUM(duration_sec) AS duration_sec,
		ROUND(CAST(SUM(bounces) AS REAL)/MAX(SUM(sessions),1), 3) AS bounce_rate,
		CAST(SUM(duration_sec)/MAX(SUM(sessions),1) AS INTEGER) AS avg_session_sec
		FROM v_views_daily WHERE project_id=? AND day BETWEEN ? AND ?`
	args := []any{in.ProjectID, in.From, in.To}
	if in.Kind != "" {
		q += ` AND kind=?`
		args = append(args, in.Kind)
	}
	out, err := h.table(ctx, q+` GROUP BY day ORDER BY day`, args...)
	return out, err
}

// ---- views_breakdown ----

// viewsDimension is one breakdown: the view it reads and the key columns
// it groups by (one or two). kinds has no view of its own; it reads the
// daily table.
type viewsDimension struct {
	view string
	cols []string
}

var viewsDimensions = map[string]viewsDimension{
	"kinds":        {"v_views_daily", []string{"kind"}},
	"paths":        {"v_views_paths", []string{"path"}},
	"hosts":        {"v_views_hosts", []string{"host"}},
	"referrers":    {"v_views_referrers", []string{"source"}},
	"utm":          {"v_views_utm", []string{"utm_source", "utm_medium", "utm_campaign"}},
	"countries":    {"v_views_countries", []string{"country"}},
	"os":           {"v_views_os", []string{"os", "os_version"}},
	"browsers":     {"v_views_browsers", []string{"browser", "browser_version"}},
	"app_versions": {"v_views_app_versions", []string{"os", "app_version"}},
	"devices":      {"v_views_devices", []string{"device", "device_model"}},
	"displays":     {"v_views_displays", []string{"display"}},
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
	Dimension string `json:"dimension" jsonschema:"one of: app_versions, browsers, countries, devices, displays, hosts, kinds, os, paths, referrers, utm"`
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
	cols := strings.Join(dim.cols, ", ")
	out, err := h.table(ctx, `SELECT `+cols+`,
		SUM(visitors) AS visitors, SUM(views) AS views
		FROM `+dim.view+` WHERE project_id=? AND day BETWEEN ? AND ?
		GROUP BY `+cols+` ORDER BY visitors DESC LIMIT ?`,
		in.ProjectID, in.From, in.To, limit)
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
	const p = "/api/projects/{project_id}"

	expose(r, spec{Name: "list_projects", Annotations: ro, Method: "GET", Path: "/api/projects",
		Description: "List projects with id, name, identity mode and data coverage. Call this first: every other tool takes a project_id from here. Projects with identity=identified support retention and identities; anonymous ones cannot (their visitor ids rotate daily)."},
		h.listProjects)
	expose(r, spec{Name: "views_overview", Annotations: ro, Method: "GET", Path: p + "/views/overview",
		Description: "Daily views for one project: visitors, views, sessions, bounces, duration, with derived bounce_rate and avg_session_sec. Sums every kind (web, app, cli, …) unless kind is given. Includes yesterday and today (live)."},
		h.viewsOverview)
	expose(r, spec{Name: "views_breakdown", Annotations: ro, Method: "GET", Path: p + "/views/breakdown",
		Description: "Top values for one dimension of a project's views over a date range: kinds, paths, hosts, referrers, utm, countries, os, browsers, app_versions, devices or displays. Two-key dimensions (os, browsers, app_versions, devices) return both columns."},
		h.viewsBreakdown)
	expose(r, spec{Name: "product_events", Annotations: ro, Method: "GET", Path: p + "/product/events",
		Description: "Product events per day: count and unique users per event name, plus daily totals. Unconditional — no attribute declaration is required to see it."},
		h.productEvents)
	expose(r, spec{Name: "product_attributes", Annotations: ro, Method: "GET", Path: p + "/product/attributes",
		Description: "Attribute breakdowns for product events. The system dimensions $os and $app_version are always included; a custom key only appears once the project declares it in attributes (see update_project)."},
		h.productAttributes)
	expose(r, spec{Name: "retention", Annotations: ro, Method: "GET", Path: p + "/retention",
		Description: "D1/D7/D30-style cohort curves for identified projects. Cohorted by how the actor was identified: actor=user or actor=install. Returns aggregated_through: cohorts after it are absent (refreshed 03:00 UTC), not zero. Anonymous projects have no retention by design."},
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
	expose(r, spec{Name: "update_project", Annotations: write, Method: "PATCH", Path: "/api/projects/{project_id}",
		Description: "Update a project's name, identity mode, allowed origins and/or declared product-event attributes (breakdown keys for flat-view columns and attribute rollups). Fields you omit are left unchanged; allowed_origins and attributes replace the whole list when given, and an explicit empty allowed_origins clears it. Switching to identity=identified starts storing user ids and names as given — privacy-significant, say so to the user before doing it."},
		h.updateProject)
	expose(r, spec{Name: "archive_project", Annotations: idem, Method: "POST", Path: "/api/projects/{project_id}/archive",
		Description: "Archive a project: ingestion stops, data and dashboards keep working, fully reversible with restore_project. There is no delete over the API — deletion requires the CLI."},
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

	expose(r, spec{Name: "integration_guide", Annotations: ro, // MCP only
		Description: "Tailored integration instructions for one project and platform (web, spa, server, mobile), with the project's real ingest key, collector URL, identity-mode guidance and event examples baked in. Confirm the collector hostname with the user. Call after create_project; read docs://twillingate for depth."},
		h.integrationGuide)

	registerSchemaRoute(r)
}
