package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/dmtrkzntsv/twillingate/docs"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// schemaFiles is the reference to the event files' seven tables, which
// query reads by default and widget SQL reads: the facts a model cannot
// infer from the column names first, then one indented
// "name(col, col, …)  -- note" line per table, which the console's SQL
// editor reads for completion (web/src/lib/schema-views.ts).
// TestSchemaViewsMatchesTheFiles binds each line to the reader's columns.
const schemaFiles = `# Query schema

The event files: DuckDB SQL over the raw events twillingate keeps as files
beside its database. query and widget SQL read nothing else. The files hold the raw history: every event
since the upgrade that began writing them, plus the earlier raw days
twillingate events import copied, today's included, kept RETENTION_DAYS.

Facts you cannot infer from the column names:

1. day is a DATE, ts's UTC day; ts, received_at, started_at, ended_at,
   first_seen and last_seen are TIMESTAMPs (UTC). In widget SQL :project
   is a BIGINT and :from and :to are DATEs: day BETWEEN :from AND :to.
2. A field an event did not send is NULL, not '' (sample_rate is 1 unless
   the event was sampled). Test IS NULL; query shows NULL as an empty cell.
3. Every attribute key any event carried is a VARCHAR column attr_<key>,
   declared or not, NULL on events without it. Quote a key with other
   characters than letters, digits and _: "attr_plan type". Event and
   attribute rules (renames, merges, blocks) apply as you read.
4. measures adds bucket, the log-scale bucket of value (base 1.04, -1000
   for a value of 1e-17 or less), read as 2 * pow(1.04, bucket) / 2.04
   (0 for -1000). A sample weighs 1 / sample_rate. A percentile is the
   first bucket whose running weight reaches q × the total.
5. Nothing is capped or folded: there are no (other) rows. The dashboards
   count visitors as count(DISTINCT actor_id) per day, then add the days.
6. Filter on project_id (ids from list_projects) and day: DuckDB skips the
   files a filter excludes.

Tables:

  events(id, project_id, family, event_name, ts, day, received_at, kind, actor_id, actor_kind, user_id, group_id, session_id, install_id, user_name, group_name, host, path, referrer_source, utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser, browser_version, browser_locale, app_version, app_locale, device, device_model, display_width, display_height, country, consent, value, measure, sample_rate)  -- every event; family 'views'|'product'|'measures'; plus one attr_<key> column per attribute key
  views(id, project_id, family, event_name, ts, day, received_at, kind, actor_id, actor_kind, user_id, group_id, session_id, install_id, user_name, group_name, host, path, referrer_source, utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser, browser_version, browser_locale, app_version, app_locale, device, device_model, display_width, display_height, country, consent, value, measure, sample_rate)  -- the events of family 'views': page and screen views
  product(id, project_id, family, event_name, ts, day, received_at, kind, actor_id, actor_kind, user_id, group_id, session_id, install_id, user_name, group_name, host, path, referrer_source, utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser, browser_version, browser_locale, app_version, app_locale, device, device_model, display_width, display_height, country, consent, value, measure, sample_rate)  -- the events of family 'product'
  measures(id, project_id, family, event_name, ts, day, received_at, kind, actor_id, actor_kind, user_id, group_id, session_id, install_id, user_name, group_name, host, path, referrer_source, utm_source, utm_medium, utm_campaign, platform, os, os_version, os_name, browser, browser_version, browser_locale, app_version, app_locale, device, device_model, display_width, display_height, country, consent, value, measure, sample_rate, bucket)  -- the events of family 'measures'; measure: 'time' (ms)|'size' (bytes)|'number'
  sessions(project_id, day, kind, actor_id, actor_kind, session_id, started_at, ended_at, duration_sec, views, entry_host, entry_path, exit_path, referrer_source, utm_source, utm_medium, utm_campaign)  -- one row per session of views, cut at midnight UTC; a session of 1 view is a bounce
  identities(project_id, kind, id, name, first_seen, last_seen, views, events)  -- kind: 'user' (from user_id)|'group' (from group_id); name: sent with the latest event that carried one
  aliases(project_id, user_id, install_id, first_seen, last_seen, events)  -- each user_id and install_id sent on the same event

Cost note: a query without a day filter reads the project's whole history;
COUNT(DISTINCT …) over a long range keeps every value in memory
(ENGINE_MEMORY_LIMIT). Queries are row-capped and time-limited.`

// schemaViews is schema://views and GET /api/schema/views: the event
// files' tables, the only ones query reads.
const schemaViews = schemaFiles

// textResource registers a static text resource.
func textResource(s *mcp.Server, uri, name, desc, text string) {
	s.AddResource(&mcp.Resource{URI: uri, Name: name, Description: desc,
		MIMEType: "text/markdown"},
		func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
				URI: uri, MIMEType: "text/markdown", Text: text}}}, nil
		})
}

func (h *host) registerResources(s *mcp.Server) {
	textResource(s, "docs://twillingate", "twillingate",
		"Set up a project, instrument a site or app, and answer questions from the data: project and ingest-key management, the JS SDK, the normative POST /ingest/events wire format and event model, the HTTP API, and the queryable views. Read before instrumenting or integrating anything.",
		docs.Twillingate)
	textResource(s, "docs://deployment", "deployment",
		"Run twillingate on your own server: systemd and docker install, every environment variable, enabling and authenticating the console (MCP, REST and the dashboards at /app/), backup and restore. Read when helping an operator install or configure the collector itself.",
		docs.Deployment)
	textResource(s, "docs://reporting", "reporting",
		"Build dashboards for the /app/ page: dashboards, widgets and components, the component table with the columns each one reads, the SQL parameters and range presets, the grid, and archiving. Read before creating or changing a dashboard; reporting_guide returns the live parts of it in one call.",
		docs.Reporting)
	h.jsonResource(s, "schema://components", "components",
		"The registered source types and components: what list_components returns.",
		func(ctx context.Context) (any, error) { return h.listComponents(ctx, struct{}{}) })
	h.jsonResource(s, "schema://dashboards", "dashboards",
		"The instance timezone and every dashboard in sidebar order: what list_dashboards returns.",
		func(ctx context.Context) (any, error) { return h.listDashboards(ctx, struct{}{}) })
	h.jsonResource(s, "schema://widgets", "widgets",
		"Every widget, archived ones included, with its dashboard and place: what list_widgets returns unfiltered.",
		func(ctx context.Context) (any, error) { return h.listWidgets(ctx, listWidgetsIn{}) })

	s.AddResource(&mcp.Resource{
		URI: "schema://views", Name: "views",
		Description: "The event files' seven tables, their columns, and the caveats needed to write correct SQL. Read before using the query tool.",
		MIMEType:    "text/plain",
	}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: "schema://views", MIMEType: "text/plain", Text: schemaViews}}}, nil
	})
	s.AddResource(&mcp.Resource{
		URI: "schema://projects", Name: "projects",
		Description: "Current projects and their settings.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		type pj struct {
			ProjectID      int64 `json:"project_id"`
			Name           string
			Archived       bool     `json:",omitempty"`
			AllowedOrigins []string `json:"allowed_origins"`
		}
		var out []pj
		for _, p := range h.reg.Snapshot(ctx).Projects() {
			out = append(out, pj{p.ID, p.Name, p.Archived, p.AllowedOrigins})
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("schema://projects: %w", err)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: "schema://projects", MIMEType: "application/json", Text: string(b)}}}, nil
	})
}

// jsonResource registers a live JSON resource: load runs on every read,
// through the same code as the matching tool, so the two cannot differ.
func (h *host) jsonResource(s *mcp.Server, uri, name, desc string, load func(context.Context) (any, error)) {
	s.AddResource(&mcp.Resource{URI: uri, Name: name, Description: desc, MIMEType: "application/json"},
		func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			v, err := load(ctx)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", uri, err)
			}
			b, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				return nil, fmt.Errorf("%s: %w", uri, err)
			}
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
				URI: uri, MIMEType: "application/json", Text: string(b)}}}, nil
		})
}

// registerSchemaRoute serves schema://views over REST: POST /api/query
// callers need the same column reference MCP clients read.
func registerSchemaRoute(r *registrar) {
	if r.rest == nil {
		return
	}
	r.rest.HandleFunc("GET /api/schema/views", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, schemaViews)
	})
}
