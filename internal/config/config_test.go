package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// load runs FromEnv against a var map. DATABASE_DSN defaults to an unused
// sqlite DSN so callers that only care about other fields can omit it.
func load(t *testing.T, vars map[string]string) (*Config, error) {
	t.Helper()
	m := map[string]string{"DATABASE_DSN": "sqlite:///tmp/a.db"}
	for k, v := range vars {
		m[k] = v
	}
	return FromEnv(func(k string) (string, bool) { v, ok := m[k]; return v, ok })
}

func TestDefaultsApplied(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.IngestAddr != "127.0.0.1:8080" {
		t.Errorf("IngestAddr = %q", c.IngestAddr)
	}
	if c.Geo != "cloudflare://" {
		t.Errorf("Geo = %q", c.Geo)
	}
	if c.Log.Level != "info" || c.Log.Format != "json" {
		t.Errorf("Log = %+v", c.Log)
	}
	if c.Buffer.FlushMaxEvents != 1000 || c.Buffer.FlushInterval != 5*time.Second || c.Buffer.Capacity != 10000 {
		t.Errorf("Buffer = %+v", c.Buffer)
	}
	if c.Retention.Web.RawDays != 7 || c.Retention.Product.RawDays != 30 ||
		c.Retention.Web.AggregateDays != 365 || c.Retention.Product.AggregateDays != 365 {
		t.Errorf("Retention = %+v", c.Retention)
	}
	if c.ProductAttributesTopN != 50 {
		t.Errorf("ProductAttributesTopN = %d, want 50", c.ProductAttributesTopN)
	}
	if c.Dashboards.Addr != "0.0.0.0:3000" || c.Dashboards.Interval != 15*time.Minute {
		t.Errorf("Dashboards = %+v", c.Dashboards)
	}
	if c.Dashboards.ProjectDir != "/opt/evidence" || c.Dashboards.WorkDir != "/var/lib/dashboards" {
		t.Errorf("Dashboards dirs = %+v", c.Dashboards)
	}
}

func TestEnvOverrides(t *testing.T) {
	c, err := load(t, map[string]string{
		"DATABASE_DSN":                     "sqlite:///tmp/a.db",
		"INGEST_ADDR":                      "0.0.0.0:9999",
		"GEO_DSN":                          "none://",
		"LOG_LEVEL":                        "debug",
		"LOG_FORMAT":                       "text",
		"LOG_FILE":                         "/tmp/a.log",
		"BUFFER_FLUSH_MAX_EVENTS":          "5",
		"BUFFER_FLUSH_INTERVAL":            "250ms",
		"BUFFER_CAPACITY":                  "42",
		"RETENTION_WEB_RAW_DAYS":           "3",
		"RETENTION_WEB_AGGREGATE_DAYS":     "30",
		"RETENTION_PRODUCT_RAW_DAYS":       "10",
		"RETENTION_PRODUCT_AGGREGATE_DAYS": "60",
		"DASHBOARDS_ADDR":                  "127.0.0.1:4000",
		"DASHBOARDS_INTERVAL":              "1m",
		"DASHBOARDS_PROJECT_DIR":           "/tmp/evidence",
		"DASHBOARDS_WORK_DIR":              "/tmp/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.IngestAddr != "0.0.0.0:9999" || c.Geo != "none://" {
		t.Errorf("IngestAddr/Geo = %q/%q", c.IngestAddr, c.Geo)
	}
	if c.Log.Level != "debug" || c.Log.Format != "text" || c.Log.File != "/tmp/a.log" {
		t.Errorf("Log = %+v", c.Log)
	}
	if c.Buffer.FlushMaxEvents != 5 || c.Buffer.FlushInterval != 250*time.Millisecond || c.Buffer.Capacity != 42 {
		t.Errorf("Buffer = %+v", c.Buffer)
	}
	if c.Retention.Web.RawDays != 3 || c.Retention.Web.AggregateDays != 30 ||
		c.Retention.Product.RawDays != 10 || c.Retention.Product.AggregateDays != 60 {
		t.Errorf("Retention = %+v", c.Retention)
	}
	if c.Dashboards.Addr != "127.0.0.1:4000" || c.Dashboards.Interval != time.Minute ||
		c.Dashboards.ProjectDir != "/tmp/evidence" || c.Dashboards.WorkDir != "/tmp/work" {
		t.Errorf("Dashboards = %+v", c.Dashboards)
	}
}

func TestValidationErrors(t *testing.T) {
	base := func(over map[string]string) map[string]string {
		vars := map[string]string{"DATABASE_DSN": "sqlite:///tmp/a.db"}
		for k, v := range over {
			vars[k] = v
		}
		return vars
	}
	cases := map[string]map[string]string{
		"no database":       {"DATABASE_DSN": ""},
		"bad geo scheme":    base(map[string]string{"GEO_DSN": "???"}),
		"negative raw_days": base(map[string]string{"RETENTION_WEB_RAW_DAYS": "-1"}),
		"bad integer":       base(map[string]string{"BUFFER_CAPACITY": "many"}),
		"invalid duration":  base(map[string]string{"BUFFER_FLUSH_INTERVAL": "fast"}),
	}
	for name, vars := range cases {
		if _, err := FromEnv(func(k string) (string, bool) { v, ok := vars[k]; return v, ok }); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// Load reads the real process environment; cover the wrapper itself.
func TestLoadFromProcessEnv(t *testing.T) {
	t.Setenv("DATABASE_DSN", "sqlite:///tmp/a.db")
	t.Setenv("LOG_LEVEL", "warn")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Log.Level != "warn" {
		t.Errorf("Load() = %+v", c)
	}
}

// mapLookup is the FromEnv* seam: a lookup backed by a plain map.
func mapLookup(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
}

func TestDashboardsDefaults(t *testing.T) {
	c, err := FromEnvDashboards(mapLookup(map[string]string{
		"DATABASE_DSN": "sqlite:///var/lib/twillingate/twillingate.db",
	}))
	if err != nil {
		t.Fatalf("FromEnvDashboards: %v", err)
	}
	if c.Dashboards.DBPath != "/var/lib/twillingate/twillingate.db" {
		t.Errorf("DBPath = %q, want the DATABASE_DSN path", c.Dashboards.DBPath)
	}
	if c.Dashboards.Addr != "0.0.0.0:3000" || c.Dashboards.Interval != 15*time.Minute {
		t.Errorf("defaults = %+v", c.Dashboards)
	}
	if c.Dashboards.ProjectDir != "/opt/evidence" || c.Dashboards.WorkDir != "/var/lib/dashboards" {
		t.Errorf("dir defaults = %+v", c.Dashboards)
	}
}

func TestDashboardsDBPathWins(t *testing.T) {
	c, err := FromEnvDashboards(mapLookup(map[string]string{
		"DATABASE_DSN":       "sqlite:///var/lib/twillingate/twillingate.db",
		"DASHBOARDS_DB_PATH": "/data/replica.db",
	}))
	if err != nil {
		t.Fatalf("FromEnvDashboards: %v", err)
	}
	if c.Dashboards.DBPath != "/data/replica.db" {
		t.Errorf("DBPath = %q, want the explicit override", c.Dashboards.DBPath)
	}
}

func TestDashboardsNeedsADatabase(t *testing.T) {
	if _, err := FromEnvDashboards(mapLookup(map[string]string{})); err == nil {
		t.Fatal("want an error with neither DASHBOARDS_DB_PATH nor DATABASE_DSN")
	}
}

func TestDashboardsReportsBadDurations(t *testing.T) {
	if _, err := FromEnvDashboards(mapLookup(map[string]string{
		"DASHBOARDS_DB_PATH":  "/data/replica.db",
		"DASHBOARDS_INTERVAL": "soon",
	})); err == nil {
		t.Fatal("want an error for an unparseable DASHBOARDS_INTERVAL")
	}
}

// --- legacy projects.json format (used only by `twillingate config import`) ---

func TestParseProjectsIngestKeys(t *testing.T) {
	ps, err := ParseProjects(strings.NewReader(`[
	  {"alias":"a","name":"A","identity":"identified",
	   "ingest_keys":[{"key":"ak_1","label":"web"},{"key":"ak_2","label":"ios","disabled":true}]}
	]`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ps) != 1 || len(ps[0].IngestKeys) != 2 {
		t.Fatalf("got %+v", ps)
	}
	if ps[0].Identity != "identified" {
		t.Errorf("identity = %q", ps[0].Identity)
	}
	if !ps[0].IngestKeys[1].Disabled {
		t.Error("second key should be disabled")
	}
}

func TestParseProjectsRejectsUnknownFields(t *testing.T) {
	if _, err := ParseProjects(strings.NewReader(`[{"alias":"a","name":"A","allowed_origin":["https://a.com"]}]`)); err == nil {
		t.Fatal("want error for unknown field allowed_origin")
	}
}

func TestParseProjectsRejectsNonArray(t *testing.T) {
	if _, err := ParseProjects(strings.NewReader(`{"projects":[]}`)); err == nil {
		t.Fatal("want error when the top level is not an array")
	}
}

// TestParseProjectsAcceptsLegacyProductAggregation asserts an unmodified
// pre-upgrade projects.json — including the enabled and top_n fields the
// new shape drops — still parses under DisallowUnknownFields.
func TestParseProjectsAcceptsLegacyProductAggregation(t *testing.T) {
	ps, err := ParseProjects(strings.NewReader(`[
	  {"alias":"a","name":"A","identity":"anonymous",
	   "product_aggregation":{"enabled":true,"attributes":{"*":["plan"],"subscribed":["tier","plan"]},"top_n":25}}
	]`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ps) != 1 || ps[0].LegacyAggregation == nil {
		t.Fatalf("got %+v", ps)
	}
	if got := ps[0].DeclaredAttributes(); len(got) != 2 || got[0] != "plan" || got[1] != "tier" {
		t.Fatalf("DeclaredAttributes() = %v, want [plan tier] (sorted DISTINCT union)", got)
	}
}

// TestDeclaredAttributesPrefersExplicitOverLegacy: when both the new
// attributes field and the legacy product_aggregation block are present,
// attributes wins.
func TestDeclaredAttributesPrefersExplicitOverLegacy(t *testing.T) {
	p := Project{
		Attributes:        []string{"source"},
		LegacyAggregation: &LegacyAggregation{Attributes: map[string][]string{"*": {"plan"}}},
	}
	if got := p.DeclaredAttributes(); len(got) != 1 || got[0] != "source" {
		t.Fatalf("DeclaredAttributes() = %v, want [source]", got)
	}
}

// TestDeclaredAttributesNilWhenNeitherPresent: no attributes, no legacy
// block -> nil, not a spurious empty slice.
func TestDeclaredAttributesNilWhenNeitherPresent(t *testing.T) {
	p := Project{}
	if got := p.DeclaredAttributes(); len(got) != 0 {
		t.Fatalf("DeclaredAttributes() = %v, want empty", got)
	}
}

func TestAppRetentionDefaultsAndMaxEventAge(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Retention.App.RawDays != 30 || c.Retention.App.AggregateDays != 365 {
		t.Fatalf("app retention = %+v", c.Retention.App)
	}
	if want := 30 * 24 * time.Hour; c.MaxEventAge() != want {
		t.Errorf("MaxEventAge() = %v, want %v", c.MaxEventAge(), want)
	}
}

func TestAppRetentionFromEnv(t *testing.T) {
	c, err := load(t, map[string]string{
		"RETENTION_APP_RAW_DAYS":       "14",
		"RETENTION_APP_AGGREGATE_DAYS": "90",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Retention.App.RawDays != 14 || c.Retention.App.AggregateDays != 90 {
		t.Errorf("app retention = %+v", c.Retention.App)
	}
}

func TestRejectsNegativeAppRetention(t *testing.T) {
	if _, err := load(t, map[string]string{"RETENTION_APP_RAW_DAYS": "-1"}); err == nil {
		t.Fatal("want error for negative app retention")
	}
}

func TestLoadDoesNotRequireProjectsFile(t *testing.T) {
	cfg, err := FromEnv(func(k string) (string, bool) {
		if k == "DATABASE_DSN" {
			return "sqlite:///tmp/x.db", true
		}
		return "", false // PROJECTS_FILE unset, no file anywhere
	})
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.Database != "sqlite:///tmp/x.db" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func mcpEnv(over map[string]string) func(string) (string, bool) {
	base := map[string]string{
		"DATABASE_DSN": "sqlite:///tmp/x.db",
		"API_AUTH_DSN": "token://ar_x",
	}
	for k, v := range over {
		if v == "" {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	return func(k string) (string, bool) { v, ok := base[k]; return v, ok }
}

func TestValidateAPI(t *testing.T) {
	cases := []struct {
		name string
		over map[string]string
		ok   bool
	}{
		{"token ok", nil, true},
		{"no dsn", map[string]string{"API_AUTH_DSN": ""}, false},
		{"not a dsn", map[string]string{"API_AUTH_DSN": "token"}, false},
		{"unknown scheme", map[string]string{"API_AUTH_DSN": "basic://x"}, false},
		{"empty token", map[string]string{"API_AUTH_DSN": "token://"}, false},
		{"oauth ok", map[string]string{
			"API_AUTH_DSN": "oauth://idp.example.com?resource=https://twillingate.example.com"}, true},
		{"oauth resource from PUBLIC_URL", map[string]string{
			"API_AUTH_DSN": "oauth://idp.example.com",
			"PUBLIC_URL":   "https://twillingate.example.com"}, true},
		{"oauth no resource and no PUBLIC_URL", map[string]string{
			"API_AUTH_DSN": "oauth://idp.example.com"}, false},
		{"oauth empty issuer", map[string]string{
			"API_AUTH_DSN": "oauth://?resource=https://twillingate.example.com"}, false},
		{"cloudflare removed", map[string]string{
			"API_AUTH_DSN": "cloudflare://team.cloudflareaccess.com?aud=aud123"}, false},
		{"token login ok", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/api/mcp/auth_callback&resource=https://mcp.example.com"}, true},
		{"token login resource from PUBLIC_URL", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/api/mcp/auth_callback",
			"PUBLIC_URL":   "https://mcp.example.com"}, true},
		{"token login one-character password", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=a&redirect=http://localhost/callback&resource=https://mcp.example.com"}, true},
		{"token login http loopback redirect", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirect=http://127.0.0.1/callback&resource=https://mcp.example.com"}, true},
		{"token login no resource and no PUBLIC_URL", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/cb"}, false},
		{"token login no password", map[string]string{
			"API_AUTH_DSN": "token://ar_x?redirect=https://claude.ai/cb&resource=https://mcp.example.com"}, false},
		{"token login empty password", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=&redirect=https://claude.ai/cb&resource=https://mcp.example.com"}, false},
		{"token login password only", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&resource=https://mcp.example.com"}, true},
		{"token resource without password", map[string]string{
			"API_AUTH_DSN": "token://ar_x?resource=https://mcp.example.com"}, false},
		{"token unknown parameter", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirects=https://claude.ai/cb&resource=https://mcp.example.com"}, false},
		{"token malformed query", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=%zz"}, false},
		{"token empty token with query", map[string]string{
			"API_AUTH_DSN": "token://?password=pw&redirect=https://claude.ai/cb"}, false},
		{"token redirect http on public host", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirect=http://claude.ai/cb&resource=https://mcp.example.com"}, false},
		{"token redirect relative", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirect=/callback&resource=https://mcp.example.com"}, false},
		{"token redirect custom scheme", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirect=myapp://callback&resource=https://mcp.example.com"}, false},
		{"token redirect with fragment", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/cb%23frag&resource=https://mcp.example.com"}, false},
		{"token redirect unparseable", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirect=https://%25zz/cb&resource=https://mcp.example.com"}, false},
		{"token redirect host", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirect=app.example.com&resource=https://mcp.example.com"}, true},
		{"token redirect IPv6 loopback host", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirect=[::1]&resource=https://mcp.example.com"}, true},
		{"token redirect host with path", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirect=app.example.com/cb&resource=https://mcp.example.com"}, false},
		{"token redirect host with port", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirect=app.example.com:8443&resource=https://mcp.example.com"}, false},
		{"token redirect empty", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirect=&resource=https://mcp.example.com"}, false},
		{"token resource http on public host", map[string]string{
			"API_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/cb&resource=http://mcp.example.com"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := FromEnv(mcpEnv(tc.over))
			if err != nil {
				t.Fatal(err)
			}
			err = cfg.ValidateAPI()
			if (err == nil) != tc.ok {
				t.Fatalf("ValidateAPI = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestMCPAuthDSNParsing(t *testing.T) {
	t.Run("token", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(nil))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.API.AuthMode != "token" || cfg.API.Token != "ar_x" {
			t.Errorf("mode = %q token = %q", cfg.API.AuthMode, cfg.API.Token)
		}
	})
	t.Run("oauth issuer keeps path, resource explicit", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(map[string]string{
			"API_AUTH_DSN": "oauth://idp.example.com/tenant1?resource=https://t.example.com&audience=aud9"}))
		if err != nil {
			t.Fatal(err)
		}
		m := cfg.API
		if m.AuthMode != "oauth" || m.Issuer != "https://idp.example.com/tenant1" {
			t.Errorf("mode = %q issuer = %q", m.AuthMode, m.Issuer)
		}
		if m.ResourceURL != "https://t.example.com" || m.Audience != "aud9" {
			t.Errorf("resource = %q audience = %q", m.ResourceURL, m.Audience)
		}
	})
	t.Run("oauth defaults resource to PUBLIC_URL and audience to resource", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(map[string]string{
			"API_AUTH_DSN": "oauth://idp.example.com",
			"PUBLIC_URL":   "https://twillingate.example.com"}))
		if err != nil {
			t.Fatal(err)
		}
		m := cfg.API
		if m.ResourceURL != "https://twillingate.example.com" {
			t.Errorf("resource = %q", m.ResourceURL)
		}
		if m.Audience != m.ResourceURL {
			t.Errorf("audience = %q, want the resource URL", m.Audience)
		}
	})
	t.Run("oauth+insecure issuer is http for local IdPs", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(map[string]string{
			"API_AUTH_DSN": "oauth+insecure://127.0.0.1:9999?resource=https://t.example.com"}))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.API.AuthMode != "oauth" || cfg.API.Issuer != "http://127.0.0.1:9999" {
			t.Errorf("mode = %q issuer = %q", cfg.API.AuthMode, cfg.API.Issuer)
		}
	})
	t.Run("malformed DSN does not fail FromEnv, only ValidateAPI", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(map[string]string{"API_AUTH_DSN": "basic://x"}))
		if err != nil {
			t.Fatalf("FromEnv must stay lenient for bare `serve`: %v", err)
		}
		if err := cfg.ValidateAPI(); err == nil {
			t.Error("ValidateAPI accepted an unknown scheme")
		}
	})
}

func TestMCPDefaults(t *testing.T) {
	cfg, err := FromEnv(mcpEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.API.Addr != cfg.IngestAddr {
		t.Errorf("Addr = %q, want Listen %q", cfg.API.Addr, cfg.IngestAddr)
	}
	if cfg.API.DBPath != "/tmp/x.db" {
		t.Errorf("DBPath = %q", cfg.API.DBPath)
	}
	if cfg.API.QueryTimeout != 10*time.Second || cfg.API.QueryMaxRows != 1000 {
		t.Errorf("guards = %v %d", cfg.API.QueryTimeout, cfg.API.QueryMaxRows)
	}
}

func TestMCPAudienceDefaultsToResource(t *testing.T) {
	cfg, err := FromEnv(mcpEnv(map[string]string{
		"API_AUTH_DSN": "oauth://idp.example.com?resource=https://twillingate.example.com"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.API.Audience != "https://twillingate.example.com" {
		t.Errorf("Audience = %q", cfg.API.Audience)
	}
}

func TestTokenLoginDSNParsing(t *testing.T) {
	cfg, err := FromEnv(mcpEnv(map[string]string{
		"API_AUTH_DSN": "token://ar_x?redirect=https://claude.ai/api/mcp/auth_callback&password=a%2Bb%26c&redirect=App.Example.com",
		"PUBLIC_URL":   "https://mcp.example.com"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatal(err)
	}
	m := cfg.API
	if m.AuthMode != "token" || m.Token != "ar_x" {
		t.Errorf("mode = %q token = %q; the query must not leak into the token", m.AuthMode, m.Token)
	}
	if m.Password != "a+b&c" {
		t.Errorf("password = %q, want percent-decoded %q", m.Password, "a+b&c")
	}
	// A full URL counts as its host, so DSNs written for exact callbacks keep working.
	want := []string{"claude.ai", "app.example.com"}
	if !slices.Equal(m.RedirectHosts, want) {
		t.Errorf("redirect hosts = %v, want %v in DSN order", m.RedirectHosts, want)
	}
	if m.ResourceURL != "https://mcp.example.com" {
		t.Errorf("resource = %q, want PUBLIC_URL", m.ResourceURL)
	}

	plain, err := FromEnv(mcpEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.API.RedirectHosts) != 0 || plain.API.Password != "" || plain.API.ResourceURL != "" {
		t.Errorf("plain token:// grew login settings: %+v", plain.API)
	}
	if !m.LoginEnabled() || plain.API.LoginEnabled() {
		t.Errorf("LoginEnabled: with password %v, plain %v; want true, false", m.LoginEnabled(), plain.API.LoginEnabled())
	}
	oauth := APIConfig{AuthMode: "oauth", Password: "ignored"}
	if oauth.LoginEnabled() {
		t.Error("LoginEnabled true outside token mode")
	}
}

func TestRenamedVariablesRefuse(t *testing.T) {
	for old, repl := range map[string]string{
		"LISTEN_ADDR":        "INGEST_ADDR",
		"MCP_ADDR":           "API_ADDR",
		"MCP_AUTH_DSN":       "API_AUTH_DSN",
		"MCP_DB_PATH":        "API_DB_PATH",
		"MCP_QUERY_TIMEOUT":  "API_QUERY_TIMEOUT",
		"MCP_QUERY_MAX_ROWS": "API_QUERY_MAX_ROWS",
	} {
		env := map[string]string{"DATABASE_DSN": "sqlite:///tmp/x.db", old: "x"}
		_, err := FromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		want := "config: " + old + " was renamed to " + repl
		if err == nil || err.Error() != want {
			t.Errorf("%s set: err = %v, want %q", old, err, want)
		}
	}
}

func TestAPIDefaults(t *testing.T) {
	env := map[string]string{"DATABASE_DSN": "sqlite:///tmp/x.db", "INGEST_ADDR": "127.0.0.1:9"}
	c, err := FromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if c.IngestAddr != "127.0.0.1:9" || c.API.Addr != "127.0.0.1:9" || c.API.DBPath != "/tmp/x.db" ||
		c.API.QueryTimeout != 10*time.Second || c.API.QueryMaxRows != 1000 {
		t.Errorf("defaults = %+v / %+v", c.IngestAddr, c.API)
	}
	if err := c.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "API_AUTH_DSN") {
		t.Errorf("ValidateAPI without DSN = %v", err)
	}
}

func TestResourceIsTheAPIOrigin(t *testing.T) {
	load := func(env map[string]string) (*Config, error) {
		env["DATABASE_DSN"] = "sqlite:///tmp/x.db"
		c, err := FromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		if err != nil {
			return nil, err
		}
		return c, c.ValidateAPI()
	}
	c, err := load(map[string]string{"PUBLIC_URL": "https://t.example.com/", "API_AUTH_DSN": "token://ar_x?password=pw"})
	if err != nil || c.API.ResourceURL != "https://t.example.com" {
		t.Errorf("token login default resource = %q, %v", c.API.ResourceURL, err)
	}
	c, err = load(map[string]string{"PUBLIC_URL": "https://t.example.com", "API_AUTH_DSN": "oauth://idp.example.com"})
	if err != nil || c.API.ResourceURL != "https://t.example.com" || c.API.Audience != "https://t.example.com" {
		t.Errorf("oauth default resource/audience = %q/%q, %v", c.API.ResourceURL, c.API.Audience, err)
	}
	for _, dsn := range []string{
		"token://ar_x?password=pw&resource=https://api.example.com/mcp",
		"oauth://idp.example.com?resource=https://api.example.com/api",
		"token://ar_x?password=pw&resource=https://api.example.com?x=1",
		"token://ar_x?password=pw&resource=https://user@api.example.com",
		"oauth://idp.example.com?resource=api.example.com",
	} {
		if _, err := load(map[string]string{"API_AUTH_DSN": dsn}); err == nil || !strings.Contains(err.Error(), "origin") {
			t.Errorf("%s: err = %v, want an origin-only refusal", dsn, err)
		}
	}
	for _, dsn := range []string{"token://ar_x?password=pw", "oauth://idp.example.com"} {
		if _, err := load(map[string]string{"PUBLIC_URL": "https://t.example.com/analytics", "API_AUTH_DSN": dsn}); err == nil || !strings.Contains(err.Error(), "origin") {
			t.Errorf("%s with a PUBLIC_URL path: err = %v, want an origin-only refusal", dsn, err)
		}
	}
	if c, err := load(map[string]string{"API_AUTH_DSN": "token://ar_x?password=pw&resource=https://api.example.com/"}); err != nil || c.API.ResourceURL != "https://api.example.com" {
		t.Errorf("trailing slash origin = %q, %v", c.API.ResourceURL, err)
	}
}
