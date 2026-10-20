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
beside its database. query reads them by default (source files), and
widget SQL reads nothing else. The files hold the raw history: every event
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

// schemaSQLite is the reference to SQLite's views, which query reads with
// source sqlite: deprecated, removed in a later release. The three caveats
// at the top are the ones a model cannot infer (endpoint spec §9).
const schemaSQLite = `Deprecated: query with source sqlite

SQLite's views, removed in a later release: query the event files above
instead. Facts you cannot infer from the DDL:

1. day columns are TEXT 'YYYY-MM-DD' (UTC). Compare and BETWEEN as strings.
2. Every v_* view includes yesterday and today: each stitches aggregated
   history (agg_* tables) with a live half computed from raw rows.
3. EXCEPTION: v_retention has no live half. It refreshes at the 03:00 UTC
   daily pass; cohort days after that are ABSENT, not zero. It holds cohorts
   only for actors identified by $user_id or $install_id; a project whose
   clients send neither has none.
4. Every dimension is capped per day (ATTRIBUTE_VALUES_TOP_N values per
   views breakdown and per attribute key, default 100; 0 is no cap); the
   rest sit in one '(other)' row per day whose visitors are distinct
   actors, not a sum. v_identity_daily keeps the busiest
   IDENTITIES_TOP_N ids per kind per day (default 500) and drops the rest:
   there is no '(other)' row, so do not sum it for totals.

Views (all carry a 'project_id' column — always filter on it; ids come from list_projects):

  v_views_daily(project_id, day, kind, visitors, views, sessions, bounces, duration_sec)  -- kind: 'web'|'app'|'cli'|…
  v_views_paths(project_id, day, path, visitors, views)
  v_views_hosts(project_id, day, host, visitors, views)
  v_views_referrers(project_id, day, source, visitors, views)
  v_views_utm(project_id, day, utm_source, utm_medium, utm_campaign, visitors, views)
  v_views_countries(project_id, day, country, visitors, views)
  v_views_platforms(project_id, day, platform, visitors, views)  -- platform: 'web'|'ios'|'android'|'electron'|…|'unknown', never empty
  v_views_os(project_id, day, os, os_version, visitors, views)  -- os: lower-case closed vocabulary ('windows','macos','ios',…) plus 'other' (outside the list) and 'unknown' (not declared)
  v_views_browsers(project_id, day, browser, browser_version, visitors, views)  -- browser: 'chrome'|'safari'|'firefox'|'edge'|'opera'|'samsung_internet'|'brave'|'vivaldi'|'duckduckgo'|'yandex'|'other'|'unknown'
  v_views_app_versions(project_id, day, platform, app_version, visitors, views)  -- keyed by platform: 2.4.1 means different things per build
  v_views_devices(project_id, day, device, device_model, visitors, views)  -- device: 'desktop'|'mobile'|'tablet'|'wearable'|'xr'|'other'|'unknown'
  v_views_displays(project_id, day, display, visitors, views)  -- display: 'WxH', e.g. '1920x1080'
  v_views_consent(project_id, day, consent, visitors, views)  -- consent: 'given'|'none'|'unknown'; unknown is every row stored before migration 018 or sent without $consent
  v_views_locales(project_id, day, browser_locale, app_locale, visitors, views)  -- '' = not sent; views sending neither are left out; nothing before migration 019 was rolled up
  v_product_daily(project_id, day, event_name, count, unique_users)
  v_product_totals(project_id, day, total_events, active_users)
  v_identity_daily(project_id, day, kind, id, actors, users, views, events)  -- kind: 'user'|'group'
  v_retention(project_id, actor_kind, cohort_day, day_offset, actors, cohort_size)  -- actor_kind: 'user'|'install'
  v_product_attrs(project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)  -- attr_key '$platform', '$os', '$app_version', '$app_locale', '$kind', '$browser', '$device', '$browser_locale', '$country' always present; unique_groups is NULL for days rolled up before it was measured (0 = measured, none)
  v_measures_daily(project_id, day, event_name, measure, bucket, approx_value, samples, weight, sum)  -- measure: 'time' (ms)|'size' (bytes)|'number'; weight = estimated count; percentile: order by bucket, first bucket whose running weight reaches q × total
  v_measures_attrs(project_id, day, event_name, measure, attr_key, attr_value, bucket, approx_value, samples, weight, sum)  -- same keys and top-N as v_product_attrs
  identities(project_id, kind, id, name)  -- display names, joinable to v_identity_daily; ingest no longer writes it: names after the upgrade to the event files are in the files only

Cost note: the views' live halves sessionize raw rows with window
functions; a WHERE on day may not prune that work. Narrow ranges and the
agg_* tables are cheaper. Queries are row-capped and time-limited.`

// schemaViews is schema://views and GET /api/schema/views: the event
// files first, then SQLite's views under their deprecation heading. The
// editor's parser reads the table lines before that heading.
const schemaViews = schemaFiles + "\n\n" + schemaSQLite

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
		Description: "The event files' seven tables, their columns, and the caveats needed to write correct SQL, then SQLite's deprecated views (source sqlite). Read before using the query tool.",
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
