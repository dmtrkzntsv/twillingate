package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/dmtrkzntsv/twillingate/docs"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// schemaViews is the reference a model needs to write correct SQL. The
// three caveats at the top are the ones it cannot infer (endpoint spec §9).
const schemaViews = `# Query schema

Facts you cannot infer from the DDL:

1. day columns are TEXT 'YYYY-MM-DD' (UTC). Compare and BETWEEN as strings.
2. Every v_* view includes yesterday and today: each stitches aggregated
   history (agg_* tables) with a live half computed from raw rows.
3. EXCEPTION: v_retention has no live half. It refreshes at the 03:00 UTC
   daily pass; cohort days after that are ABSENT, not zero. It is populated
   only for projects with identity=identified (anonymous visitor ids rotate
   daily, so cohorts are undefined). Check list_projects for identity.
4. Every dimension is capped at 500 values per day; the rest sit in one
   '(other)' row per day whose visitors are distinct actors, not a sum.
   v_identity_daily keeps the busiest 500 ids per kind per day and drops
   the rest: there is no '(other)' row, so do not sum it for totals.

Views (all carry a 'project' column — always filter on it):

  v_views_daily(project, day, kind, visitors, views, sessions, bounces, duration_sec)  -- kind: 'web'|'app'|'cli'|…
  v_views_paths(project, day, path, visitors, views)
  v_views_hosts(project, day, host, visitors, views)
  v_views_referrers(project, day, source, visitors, views)
  v_views_utm(project, day, utm_source, utm_medium, utm_campaign, visitors, views)
  v_views_countries(project, day, country, visitors, views)
  v_views_os(project, day, os, os_version, visitors, views)
  v_views_browsers(project, day, browser, browser_version, visitors, views)
  v_views_app_versions(project, day, os, app_version, visitors, views)
  v_views_devices(project, day, device, device_model, visitors, views)
  v_views_displays(project, day, display, visitors, views)  -- display: 'WxH', e.g. '1920x1080'
  v_product_daily(project, day, event_name, count, unique_users)
  v_product_totals(project, day, total_events, active_users)
  v_identity_daily(project, day, kind, id, actors, users, views, events)  -- kind: 'user'|'group'
  v_retention(project, actor_kind, cohort_day, day_offset, actors, cohort_size)  -- actor_kind: 'user'|'install'
  v_product_attrs(project, day, event_name, attr_key, attr_value, count, unique_users)
  identities(project, kind, id, name)  -- display names, joinable to v_identity_daily

Cost note: the views' live halves sessionize raw rows with window
functions; a WHERE on day may not prune that work. Narrow ranges and the
agg_* tables are cheaper. Queries are row-capped and time-limited.`

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
		"Run twillingate on your own server: systemd and docker install, every environment variable, Evidence reporting, enabling and authenticating the API endpoint (MCP and REST), litestream replication for a two-server topology, backup drills and disaster recovery. Read when helping an operator install or configure the collector itself.",
		docs.Deployment)

	s.AddResource(&mcp.Resource{
		URI: "schema://views", Name: "views",
		Description: "Queryable views, their columns, and the caveats needed to write correct SQL. Read before using the query tool.",
		MIMEType:    "text/plain",
	}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: "schema://views", MIMEType: "text/plain", Text: schemaViews}}}, nil
	})
	s.AddResource(&mcp.Resource{
		URI: "schema://projects", Name: "projects",
		Description: "Current projects with identity modes and settings.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		type pj struct {
			Alias, Name, Identity string
			Archived              bool     `json:",omitempty"`
			AllowedOrigins        []string `json:"allowed_origins"`
		}
		var out []pj
		for _, p := range h.reg.Snapshot(ctx).Projects() {
			out = append(out, pj{p.Alias, p.Name, p.Identity, p.Archived, p.AllowedOrigins})
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("schema://projects: %w", err)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: "schema://projects", MIMEType: "application/json", Text: string(b)}}}, nil
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
