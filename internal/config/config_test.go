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
	if c.Retention.Events.RawDays != 30 || c.Retention.Events.AggregateDays != 365 {
		t.Errorf("Retention = %+v", c.Retention)
	}
	if c.Retention.ArchivedDays != 30 {
		t.Errorf("Retention.ArchivedDays = %d, want 30", c.Retention.ArchivedDays)
	}
	if c.AttributesTopN != 100 || c.ViewsDimensionsTopN != 1000 || c.IdentitiesTopN != 1000 {
		t.Errorf("caps = %d/%d/%d, want 100/1000/1000", c.AttributesTopN, c.ViewsDimensionsTopN, c.IdentitiesTopN)
	}
	if c.Reporting.CacheAge != 900*time.Second || c.Reporting.RefreshAge != 60*time.Second {
		t.Errorf("Reporting = %+v", c.Reporting)
	}
}

func TestEnvOverrides(t *testing.T) {
	c, err := load(t, map[string]string{
		"DATABASE_DSN":                    "sqlite:///tmp/a.db",
		"INGEST_ADDR":                     "0.0.0.0:9999",
		"GEO_DSN":                         "none://",
		"LOG_LEVEL":                       "debug",
		"LOG_FORMAT":                      "text",
		"LOG_FILE":                        "/tmp/a.log",
		"BUFFER_FLUSH_MAX_EVENTS":         "5",
		"BUFFER_FLUSH_INTERVAL":           "250ms",
		"BUFFER_CAPACITY":                 "42",
		"RETENTION_EVENTS_RAW_DAYS":       "3",
		"RETENTION_EVENTS_AGGREGATE_DAYS": "60",
		"RETENTION_ARCHIVED_DAYS":         "7",
		"REPORTING_CACHE_SECONDS":         "120",
		"REPORTING_REFRESH_SECONDS":       "30",
		"ATTRIBUTES_TOP_N":                "200",
		"VIEWS_DIMENSIONS_TOP_N":          "0",
		"IDENTITIES_TOP_N":                "2000",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.AttributesTopN != 200 || c.ViewsDimensionsTopN != 0 || c.IdentitiesTopN != 2000 {
		t.Errorf("caps = %d/%d/%d, want 200/0/2000", c.AttributesTopN, c.ViewsDimensionsTopN, c.IdentitiesTopN)
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
	if c.Retention.Events.RawDays != 3 || c.Retention.Events.AggregateDays != 60 {
		t.Errorf("Retention = %+v", c.Retention)
	}
	if c.Retention.ArchivedDays != 7 {
		t.Errorf("Retention.ArchivedDays = %d, want 7", c.Retention.ArchivedDays)
	}
	if c.Reporting.CacheAge != 120*time.Second || c.Reporting.RefreshAge != 30*time.Second {
		t.Errorf("Reporting = %+v", c.Reporting)
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
		"no database":              {"DATABASE_DSN": ""},
		"bad geo scheme":           base(map[string]string{"GEO_DSN": "???"}),
		"negative raw_days":        base(map[string]string{"RETENTION_EVENTS_RAW_DAYS": "-1"}),
		"negative archived_days":   base(map[string]string{"RETENTION_ARCHIVED_DAYS": "-1"}),
		"negative attributes cap":  base(map[string]string{"ATTRIBUTES_TOP_N": "-1"}),
		"negative dimensions cap":  base(map[string]string{"VIEWS_DIMENSIONS_TOP_N": "-1"}),
		"negative identities cap":  base(map[string]string{"IDENTITIES_TOP_N": "-1"}),
		"bad integer":              base(map[string]string{"BUFFER_CAPACITY": "many"}),
		"invalid duration":         base(map[string]string{"BUFFER_FLUSH_INTERVAL": "fast"}),
		"negative cache seconds":   base(map[string]string{"REPORTING_CACHE_SECONDS": "-1"}),
		"negative refresh seconds": base(map[string]string{"REPORTING_REFRESH_SECONDS": "-1"}),
		"refresh exceeds cache": base(map[string]string{
			"REPORTING_CACHE_SECONDS": "60", "REPORTING_REFRESH_SECONDS": "120"}),
	}
	for name, vars := range cases {
		if _, err := FromEnv(func(k string) (string, bool) { v, ok := vars[k]; return v, ok }); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// TestRefreshExceedsCacheMessage pins the refusal's exact text, which
// names both variables and their values so a person can fix the .env
// without hunting for which one to change.
func TestRefreshExceedsCacheMessage(t *testing.T) {
	_, err := load(t, map[string]string{"REPORTING_CACHE_SECONDS": "60", "REPORTING_REFRESH_SECONDS": "120"})
	if err == nil || err.Error() != "config: REPORTING_REFRESH_SECONDS (120) exceeds REPORTING_CACHE_SECONDS (60)" {
		t.Errorf("err = %v", err)
	}
}

// TestReportingCacheZeroAllowsAnyRefresh: a cache age of 0 disables the
// cache outright (every request recomputes), so a refresh age longer
// than it is meaningless to refuse.
func TestReportingCacheZeroAllowsAnyRefresh(t *testing.T) {
	c, err := load(t, map[string]string{"REPORTING_CACHE_SECONDS": "0", "REPORTING_REFRESH_SECONDS": "120"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Reporting.CacheAge != 0 || c.Reporting.RefreshAge != 120*time.Second {
		t.Errorf("Reporting = %+v", c.Reporting)
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

// load is package config's own FromEnv wrapper (above); configtest cannot be
// imported here without an import cycle (it imports this package).
func TestEventsRetentionDefaultsAndMaxEventAge(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Retention.Events.RawDays != 30 || c.Retention.Events.AggregateDays != 365 {
		t.Fatalf("events retention = %+v, want 30/365", c.Retention.Events)
	}
	if got := c.MaxEventAge(); got != 30*24*time.Hour {
		t.Fatalf("MaxEventAge = %v", got)
	}
}

func TestEventsRetentionFromEnv(t *testing.T) {
	c, err := load(t, map[string]string{
		"RETENTION_EVENTS_RAW_DAYS": "14", "RETENTION_EVENTS_AGGREGATE_DAYS": "90"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Retention.Events.RawDays != 14 || c.Retention.Events.AggregateDays != 90 {
		t.Fatalf("events retention = %+v", c.Retention.Events)
	}
	if got := c.MaxEventAge(); got != 14*24*time.Hour {
		t.Fatalf("MaxEventAge = %v, want 14 days", got)
	}
}

func TestRejectsNegativeEventsRetention(t *testing.T) {
	for _, k := range []string{"RETENTION_EVENTS_RAW_DAYS", "RETENTION_EVENTS_AGGREGATE_DAYS"} {
		if _, err := load(t, map[string]string{k: "-1"}); err == nil {
			t.Errorf("%s=-1 accepted", k)
		}
	}
}

// The per-family names are gone, with no refusal: a leftover one is ignored.
func TestOldRetentionNamesHaveNoEffect(t *testing.T) {
	c, err := load(t, map[string]string{
		"RETENTION_VIEWS_RAW_DAYS": "3", "RETENTION_VIEWS_AGGREGATE_DAYS": "4",
		"RETENTION_PRODUCT_RAW_DAYS": "5", "RETENTION_PRODUCT_AGGREGATE_DAYS": "6",
		"RETENTION_WEB_RAW_DAYS": "7", "RETENTION_APP_AGGREGATE_DAYS": "8"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Retention.Events.RawDays != 30 || c.Retention.Events.AggregateDays != 365 {
		t.Fatalf("old names changed retention: %+v", c.Retention.Events)
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
		"DATABASE_DSN":     "sqlite:///tmp/x.db",
		"CONSOLE_AUTH_DSN": "token://ar_x",
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

func TestValidateConsole(t *testing.T) {
	cases := []struct {
		name string
		over map[string]string
		ok   bool
	}{
		{"token ok", nil, true},
		{"no dsn", map[string]string{"CONSOLE_AUTH_DSN": ""}, false},
		{"not a dsn", map[string]string{"CONSOLE_AUTH_DSN": "token"}, false},
		{"unknown scheme", map[string]string{"CONSOLE_AUTH_DSN": "basic://x"}, false},
		{"empty token", map[string]string{"CONSOLE_AUTH_DSN": "token://"}, false},
		{"oauth ok", map[string]string{
			"CONSOLE_AUTH_DSN": "oauth://idp.example.com?resource=https://twillingate.example.com"}, true},
		{"oauth resource from PUBLIC_URL", map[string]string{
			"CONSOLE_AUTH_DSN": "oauth://idp.example.com",
			"PUBLIC_URL":       "https://twillingate.example.com"}, true},
		{"oauth no resource and no PUBLIC_URL", map[string]string{
			"CONSOLE_AUTH_DSN": "oauth://idp.example.com"}, false},
		{"oauth empty issuer", map[string]string{
			"CONSOLE_AUTH_DSN": "oauth://?resource=https://twillingate.example.com"}, false},
		{"cloudflare removed", map[string]string{
			"CONSOLE_AUTH_DSN": "cloudflare://team.cloudflareaccess.com?aud=aud123"}, false},
		{"token login ok", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/api/mcp/auth_callback&resource=https://mcp.example.com"}, true},
		{"token login resource from PUBLIC_URL", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/api/mcp/auth_callback",
			"PUBLIC_URL":       "https://mcp.example.com"}, true},
		{"token login one-character password", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=a&redirect=http://localhost/callback&resource=https://mcp.example.com"}, true},
		{"token login http loopback redirect", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=http://127.0.0.1/callback&resource=https://mcp.example.com"}, true},
		{"token login no resource and no PUBLIC_URL", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/cb"}, false},
		{"token login no password", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?redirect=https://claude.ai/cb&resource=https://mcp.example.com"}, false},
		{"token login empty password", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=&redirect=https://claude.ai/cb&resource=https://mcp.example.com"}, false},
		{"token login password only", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&resource=https://mcp.example.com"}, true},
		{"token resource without password", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?resource=https://mcp.example.com"}, false},
		{"token unknown parameter", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirects=https://claude.ai/cb&resource=https://mcp.example.com"}, false},
		{"token malformed query", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=%zz"}, false},
		{"token empty token with query", map[string]string{
			"CONSOLE_AUTH_DSN": "token://?password=pw&redirect=https://claude.ai/cb"}, false},
		{"token redirect http on public host", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=http://claude.ai/cb&resource=https://mcp.example.com"}, false},
		{"token redirect relative", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=/callback&resource=https://mcp.example.com"}, false},
		{"token redirect custom scheme", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=myapp://callback&resource=https://mcp.example.com"}, false},
		{"token redirect with fragment", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/cb%23frag&resource=https://mcp.example.com"}, false},
		{"token redirect unparseable", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=https://%25zz/cb&resource=https://mcp.example.com"}, false},
		{"token redirect host", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=app.example.com&resource=https://mcp.example.com"}, true},
		{"token redirect IPv6 loopback host", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=[::1]&resource=https://mcp.example.com"}, true},
		{"token redirect host with path", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=app.example.com/cb&resource=https://mcp.example.com"}, false},
		{"token redirect host with port", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=app.example.com:8443&resource=https://mcp.example.com"}, false},
		{"token redirect empty", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=&resource=https://mcp.example.com"}, false},
		{"token resource http on public host", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/cb&resource=http://mcp.example.com"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := FromEnv(mcpEnv(tc.over))
			if err != nil {
				t.Fatal(err)
			}
			err = cfg.ValidateConsole()
			if (err == nil) != tc.ok {
				t.Fatalf("ValidateConsole = %v, want ok=%v", err, tc.ok)
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
		if cfg.Console.AuthMode != "token" || cfg.Console.Token != "ar_x" {
			t.Errorf("mode = %q token = %q", cfg.Console.AuthMode, cfg.Console.Token)
		}
	})
	t.Run("oauth issuer keeps path, resource explicit", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(map[string]string{
			"CONSOLE_AUTH_DSN": "oauth://idp.example.com/tenant1?resource=https://t.example.com&audience=aud9"}))
		if err != nil {
			t.Fatal(err)
		}
		m := cfg.Console
		if m.AuthMode != "oauth" || m.Issuer != "https://idp.example.com/tenant1" {
			t.Errorf("mode = %q issuer = %q", m.AuthMode, m.Issuer)
		}
		if m.ResourceURL != "https://t.example.com" || m.Audience != "aud9" {
			t.Errorf("resource = %q audience = %q", m.ResourceURL, m.Audience)
		}
	})
	t.Run("oauth defaults resource to PUBLIC_URL and audience to resource", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(map[string]string{
			"CONSOLE_AUTH_DSN": "oauth://idp.example.com",
			"PUBLIC_URL":       "https://twillingate.example.com"}))
		if err != nil {
			t.Fatal(err)
		}
		m := cfg.Console
		if m.ResourceURL != "https://twillingate.example.com" {
			t.Errorf("resource = %q", m.ResourceURL)
		}
		if m.Audience != m.ResourceURL {
			t.Errorf("audience = %q, want the resource URL", m.Audience)
		}
	})
	t.Run("oauth+insecure issuer is http for local IdPs", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(map[string]string{
			"CONSOLE_AUTH_DSN": "oauth+insecure://127.0.0.1:9999?resource=https://t.example.com"}))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Console.AuthMode != "oauth" || cfg.Console.Issuer != "http://127.0.0.1:9999" {
			t.Errorf("mode = %q issuer = %q", cfg.Console.AuthMode, cfg.Console.Issuer)
		}
	})
	t.Run("malformed DSN does not fail FromEnv, only ValidateConsole", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(map[string]string{"CONSOLE_AUTH_DSN": "basic://x"}))
		if err != nil {
			t.Fatalf("FromEnv must stay lenient for bare `serve`: %v", err)
		}
		if err := cfg.ValidateConsole(); err == nil {
			t.Error("ValidateConsole accepted an unknown scheme")
		}
	})
}

func TestMCPDefaults(t *testing.T) {
	cfg, err := FromEnv(mcpEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Console.Addr != cfg.IngestAddr {
		t.Errorf("Addr = %q, want Listen %q", cfg.Console.Addr, cfg.IngestAddr)
	}
	if cfg.Console.DBPath != "/tmp/x.db" {
		t.Errorf("DBPath = %q", cfg.Console.DBPath)
	}
	if cfg.Console.QueryTimeout != 10*time.Second || cfg.Console.QueryMaxRows != 1000 {
		t.Errorf("guards = %v %d", cfg.Console.QueryTimeout, cfg.Console.QueryMaxRows)
	}
}

func TestMCPAudienceDefaultsToResource(t *testing.T) {
	cfg, err := FromEnv(mcpEnv(map[string]string{
		"CONSOLE_AUTH_DSN": "oauth://idp.example.com?resource=https://twillingate.example.com"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Console.Audience != "https://twillingate.example.com" {
		t.Errorf("Audience = %q", cfg.Console.Audience)
	}
}

func TestTokenLoginDSNParsing(t *testing.T) {
	cfg, err := FromEnv(mcpEnv(map[string]string{
		"CONSOLE_AUTH_DSN": "token://ar_x?redirect=https://claude.ai/api/mcp/auth_callback&password=a%2Bb%26c&redirect=App.Example.com",
		"PUBLIC_URL":       "https://mcp.example.com"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateConsole(); err != nil {
		t.Fatal(err)
	}
	m := cfg.Console
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
	if len(plain.Console.RedirectHosts) != 0 || plain.Console.Password != "" || plain.Console.ResourceURL != "" {
		t.Errorf("plain token:// grew login settings: %+v", plain.Console)
	}
	if !m.LoginEnabled() || plain.Console.LoginEnabled() {
		t.Errorf("LoginEnabled: with password %v, plain %v; want true, false", m.LoginEnabled(), plain.Console.LoginEnabled())
	}
	oauth := ConsoleConfig{AuthMode: "oauth", Password: "ignored"}
	if oauth.LoginEnabled() {
		t.Error("LoginEnabled true outside token mode")
	}
}

// insecure=1 lets the console origin be plain http on any host (a tailnet
// encrypts the link itself); without it only loopback may. It widens the
// origin only: a redirect= URL stays https-only, and the value must be 1.
func TestTokenLoginInsecure(t *testing.T) {
	load := func(dsn, publicURL string) (ConsoleConfig, error) {
		cfg, err := FromEnv(mcpEnv(map[string]string{"CONSOLE_AUTH_DSN": dsn, "PUBLIC_URL": publicURL}))
		if err != nil {
			return ConsoleConfig{}, err
		}
		return cfg.Console, cfg.ValidateConsole()
	}
	for _, tc := range []struct {
		name, dsn, publicURL string
		wantErr              string // substring; "" accepts
		wantResource         string
	}{
		{"http without insecure", "token://ar_x?password=p&resource=http://homelab:8290", "", "insecure=1", ""},
		{"http PUBLIC_URL without insecure", "token://ar_x?password=p", "http://homelab:8290", "insecure=1", ""},
		{"http with insecure", "token://ar_x?password=p&resource=http://homelab:8290&insecure=1", "", "", "http://homelab:8290"},
		{"http PUBLIC_URL with insecure", "token://ar_x?password=p&insecure=1", "http://Homelab:8290", "", "http://homelab:8290"},
		{"https with insecure", "token://ar_x?password=p&insecure=1", "https://console.example.com", "", "https://console.example.com"},
		{"insecure=0", "token://ar_x?password=p&resource=http://homelab:8290&insecure=0", "", `insecure="0" must be 1`, ""},
		{"insecure=true", "token://ar_x?password=p&resource=http://homelab:8290&insecure=true", "", `insecure="true" must be 1`, ""},
		{"insecure twice", "token://ar_x?password=p&insecure=1&insecure=1", "http://homelab:8290", "must be 1", ""},
		{"http redirect stays refused", "token://ar_x?password=p&insecure=1&redirect=http://app.example.com/cb", "http://homelab:8290", "may only use http", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := load(tc.dsn, tc.publicURL)
			switch {
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
			case err != nil:
				t.Fatal(err)
			case !m.Insecure || m.ResourceURL != tc.wantResource:
				t.Errorf("insecure = %v resource = %q, want true and %q", m.Insecure, m.ResourceURL, tc.wantResource)
			}
		})
	}
	if m, err := load("token://ar_x?password=p", "https://console.example.com"); err != nil || m.Insecure {
		t.Errorf("without insecure=: insecure = %v, err = %v", m.Insecure, err)
	}
}

func TestRenamedVariablesRefuse(t *testing.T) {
	for old, repl := range map[string]string{
		"LISTEN_ADDR":              "INGEST_ADDR",
		"API_ADDR":                 "CONSOLE_ADDR",
		"API_URL":                  "CONSOLE_URL",
		"API_AUTH_DSN":             "CONSOLE_AUTH_DSN",
		"API_DB_PATH":              "CONSOLE_DB_PATH",
		"API_QUERY_TIMEOUT":        "CONSOLE_QUERY_TIMEOUT",
		"API_QUERY_MAX_ROWS":       "CONSOLE_QUERY_MAX_ROWS",
		"PRODUCT_ATTRIBUTES_TOP_N": "ATTRIBUTES_TOP_N",
	} {
		env := map[string]string{"DATABASE_DSN": "sqlite:///tmp/x.db", old: "x"}
		_, err := FromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		want := "config: " + old + " was renamed to " + repl
		if err == nil || err.Error() != want {
			t.Errorf("%s set: err = %v, want %q", old, err, want)
		}
	}
}

func TestConsoleDefaults(t *testing.T) {
	env := map[string]string{"DATABASE_DSN": "sqlite:///tmp/x.db", "INGEST_ADDR": "127.0.0.1:9"}
	c, err := FromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if c.IngestAddr != "127.0.0.1:9" || c.Console.Addr != "127.0.0.1:9" || c.Console.DBPath != "/tmp/x.db" ||
		c.Console.QueryTimeout != 10*time.Second || c.Console.QueryMaxRows != 1000 {
		t.Errorf("defaults = %+v / %+v", c.IngestAddr, c.Console)
	}
	if err := c.ValidateConsole(); err == nil || !strings.Contains(err.Error(), "CONSOLE_AUTH_DSN") {
		t.Errorf("ValidateConsole without DSN = %v", err)
	}
}

func TestResourceIsTheAPIOrigin(t *testing.T) {
	load := func(env map[string]string) (*Config, error) {
		env["DATABASE_DSN"] = "sqlite:///tmp/x.db"
		c, err := FromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		if err != nil {
			return nil, err
		}
		return c, c.ValidateConsole()
	}
	c, err := load(map[string]string{"PUBLIC_URL": "https://t.example.com/", "CONSOLE_AUTH_DSN": "token://ar_x?password=pw"})
	if err != nil || c.Console.ResourceURL != "https://t.example.com" {
		t.Errorf("token login default resource = %q, %v", c.Console.ResourceURL, err)
	}
	c, err = load(map[string]string{"PUBLIC_URL": "https://t.example.com", "CONSOLE_AUTH_DSN": "oauth://idp.example.com"})
	if err != nil || c.Console.ResourceURL != "https://t.example.com" || c.Console.Audience != "https://t.example.com" {
		t.Errorf("oauth default resource/audience = %q/%q, %v", c.Console.ResourceURL, c.Console.Audience, err)
	}
	for _, dsn := range []string{
		"token://ar_x?password=pw&resource=https://api.example.com/mcp",
		"oauth://idp.example.com?resource=https://api.example.com/api",
		"token://ar_x?password=pw&resource=https://api.example.com?x=1",
		"token://ar_x?password=pw&resource=https://user@api.example.com",
		"oauth://idp.example.com?resource=api.example.com",
	} {
		if _, err := load(map[string]string{"CONSOLE_AUTH_DSN": dsn}); err == nil || !strings.Contains(err.Error(), "origin") {
			t.Errorf("%s: err = %v, want an origin-only refusal", dsn, err)
		}
	}
	for _, dsn := range []string{"token://ar_x?password=pw", "oauth://idp.example.com"} {
		if _, err := load(map[string]string{"PUBLIC_URL": "https://t.example.com/analytics", "CONSOLE_AUTH_DSN": dsn}); err == nil || !strings.Contains(err.Error(), "origin") {
			t.Errorf("%s with a PUBLIC_URL path: err = %v, want an origin-only refusal", dsn, err)
		}
	}
	if c, err := load(map[string]string{"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&resource=https://api.example.com/"}); err != nil || c.Console.ResourceURL != "https://api.example.com" {
		t.Errorf("trailing slash origin = %q, %v", c.Console.ResourceURL, err)
	}
	// The resource must name the same origin a client actually connects
	// to: uppercase host and an explicit default port are just spellings
	// of the same origin, not a different one.
	if c, err := load(map[string]string{"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&resource=https://API.example.com:443"}); err != nil || c.Console.ResourceURL != "https://api.example.com" {
		t.Errorf("uppercase host + default port origin = %q, %v, want https://api.example.com", c.Console.ResourceURL, err)
	}
	if c, err := load(map[string]string{"CONSOLE_AUTH_DSN": "oauth://idp.example.com?resource=http://api.example.com:80"}); err != nil || c.Console.ResourceURL != "http://api.example.com" {
		t.Errorf("http default port origin = %q, %v, want http://api.example.com", c.Console.ResourceURL, err)
	}
	// A non-default port must survive: it is part of the origin.
	if c, err := load(map[string]string{"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&resource=https://api.example.com:8443"}); err != nil || c.Console.ResourceURL != "https://api.example.com:8443" {
		t.Errorf("non-default port origin = %q, %v, want https://api.example.com:8443", c.Console.ResourceURL, err)
	}
}

func TestAudienceGivenOnlyWhenExplicit(t *testing.T) {
	for dsn, want := range map[string]bool{
		"oauth://idp.example.com?resource=https://t.example.com":                                false,
		"oauth://idp.example.com?resource=https://t.example.com&audience=https://t.example.com": true,
		"token://ar_x?password=pw&resource=https://t.example.com":                               false,
	} {
		cfg, err := FromEnv(mcpEnv(map[string]string{"CONSOLE_AUTH_DSN": dsn}))
		if err != nil || cfg.ValidateConsole() != nil {
			t.Fatalf("%s: %v %v", dsn, err, cfg.ValidateConsole())
		}
		if got := cfg.Console.AudienceGiven(); got != want {
			t.Errorf("%s: AudienceGiven = %v, want %v", dsn, got, want)
		}
	}
}

// TestConsoleURLIsTheResourceDefault: CONSOLE_URL names the console's own public
// address; the login's resource (so its issuer and the dashboards'
// callback) follows it, falling back to PUBLIC_URL, and resource= still
// wins when given.
func TestConsoleURLIsTheResourceDefault(t *testing.T) {
	load := func(env map[string]string) (*Config, error) {
		env["DATABASE_DSN"] = "sqlite:///tmp/x.db"
		c, err := FromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		if err != nil {
			return nil, err
		}
		return c, c.ValidateConsole()
	}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"CONSOLE_URL beside an ingest PUBLIC_URL", map[string]string{
			"PUBLIC_URL": "https://t.example.com", "CONSOLE_URL": "https://tapi.example.com/",
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw"}, "https://tapi.example.com"},
		{"CONSOLE_URL defaults to PUBLIC_URL", map[string]string{
			"PUBLIC_URL": "https://t.example.com", "CONSOLE_AUTH_DSN": "token://ar_x?password=pw"}, "https://t.example.com"},
		{"CONSOLE_URL alone", map[string]string{
			"CONSOLE_URL": "https://tapi.example.com", "CONSOLE_AUTH_DSN": "oauth://idp.example.com"}, "https://tapi.example.com"},
		{"resource= wins over CONSOLE_URL", map[string]string{
			"CONSOLE_URL": "https://tapi.example.com", "CONSOLE_AUTH_DSN": "token://ar_x?password=pw&resource=https://mcp.example.com"}, "https://mcp.example.com"},
	}
	for _, tc := range cases {
		c, err := load(tc.env)
		if err != nil || c.Console.ResourceURL != tc.want {
			t.Errorf("%s: resource = %q, %v; want %q", tc.name, c.Console.ResourceURL, err, tc.want)
		}
	}
	if c, _ := load(map[string]string{"PUBLIC_URL": "https://t.example.com"}); c.Console.URL != "https://t.example.com" {
		t.Errorf("CONSOLE_URL default = %q, want PUBLIC_URL", c.Console.URL)
	}

	// A bad value names the variable it came from.
	if _, err := load(map[string]string{"CONSOLE_URL": "https://tapi.example.com/api",
		"CONSOLE_AUTH_DSN": "token://ar_x?password=pw"}); err == nil || !strings.Contains(err.Error(), "CONSOLE_URL=") {
		t.Errorf("CONSOLE_URL with a path: %v, want a refusal naming CONSOLE_URL", err)
	}
	if _, err := load(map[string]string{"PUBLIC_URL": "https://t.example.com/site",
		"CONSOLE_AUTH_DSN": "token://ar_x?password=pw"}); err == nil || !strings.Contains(err.Error(), "PUBLIC_URL (CONSOLE_URL's default)") {
		t.Errorf("PUBLIC_URL with a path: %v, want a refusal naming PUBLIC_URL as CONSOLE_URL's default", err)
	}
}
