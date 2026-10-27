package config

import (
	"os"
	"path/filepath"
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
	if c.Ignored != nil || c.IgnoredWarning() != "" {
		t.Errorf("Ignored = %v, want none", c.Ignored)
	}
	if c.IngestLateDays != 1 {
		t.Errorf("IngestLateDays = %d, want 1", c.IngestLateDays)
	}
	if c.Retention.ArchivedDays != 30 {
		t.Errorf("Retention.ArchivedDays = %d, want 30", c.Retention.ArchivedDays)
	}
	if c.Retention.Days != 400 {
		t.Errorf("Retention.Days = %d, want 400", c.Retention.Days)
	}
	if c.Forms.DraftDays != DefaultFormDraftDays || DefaultFormDraftDays != 7 {
		t.Errorf("Forms.DraftDays = %d (default %d), want 7", c.Forms.DraftDays, DefaultFormDraftDays)
	}
	if c.Reporting.CacheAge != 900*time.Second || c.Reporting.RefreshAge != 60*time.Second {
		t.Errorf("Reporting = %+v", c.Reporting)
	}
	if c.IngestDisabled {
		t.Error("IngestDisabled is on by default")
	}
}

// INGEST_DISABLED takes Go's boolean spellings (1, t, true, TRUE, True and
// their 0/f/false counterparts); anything else refuses the boot, naming
// the variable, rather than guessing which way a typo was meant.
func TestIngestDisabled(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, "TRUE": true, "t": true,
		"0": false, "false": false, "False": false, "": false} {
		c, err := load(t, map[string]string{"INGEST_DISABLED": v})
		if err != nil {
			t.Errorf("INGEST_DISABLED=%q: %v", v, err)
			continue
		}
		if c.IngestDisabled != want {
			t.Errorf("INGEST_DISABLED=%q: IngestDisabled = %v, want %v", v, c.IngestDisabled, want)
		}
	}
	for _, v := range []string{"yes", "on", "2"} {
		if _, err := load(t, map[string]string{"INGEST_DISABLED": v}); err == nil || !strings.Contains(err.Error(), "INGEST_DISABLED") {
			t.Errorf("INGEST_DISABLED=%q: err = %v, want a refusal naming the variable", v, err)
		}
	}
}

func TestEnvOverrides(t *testing.T) {
	c, err := load(t, map[string]string{
		"DATABASE_DSN":               "sqlite:///tmp/a.db",
		"INGEST_ADDR":                "0.0.0.0:9999",
		"GEO_DSN":                    "none://",
		"LOG_LEVEL":                  "debug",
		"LOG_FORMAT":                 "text",
		"LOG_FILE":                   "/tmp/a.log",
		"BUFFER_FLUSH_MAX_EVENTS":    "5",
		"BUFFER_FLUSH_INTERVAL":      "250ms",
		"BUFFER_CAPACITY":            "42",
		"RETENTION_ARCHIVED_DAYS":    "7",
		"REPORTING_CACHE_MAX_AGE":    "2m",
		"REPORTING_REFRESH_MAX_AGE":  "30s",
		"RETENTION_FORMS_DRAFT_DAYS": "14",
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
	if c.Retention.ArchivedDays != 7 {
		t.Errorf("Retention.ArchivedDays = %d, want 7", c.Retention.ArchivedDays)
	}
	if c.Forms.DraftDays != 14 {
		t.Errorf("Forms.DraftDays = %d, want 14", c.Forms.DraftDays)
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
		"no database":                     {"DATABASE_DSN": ""},
		"bad geo scheme":                  base(map[string]string{"GEO_DSN": "???"}),
		"zero retention days":             base(map[string]string{"RETENTION_DAYS": "0"}),
		"late days exceed retention":      base(map[string]string{"RETENTION_DAYS": "30", "INGEST_LATE_DAYS": "45"}),
		"zero late days":                  base(map[string]string{"INGEST_LATE_DAYS": "0"}),
		"negative late days":              base(map[string]string{"INGEST_LATE_DAYS": "-1"}),
		"negative archived_days":          base(map[string]string{"RETENTION_ARCHIVED_DAYS": "-1"}),
		"zero form draft days":            base(map[string]string{"RETENTION_FORMS_DRAFT_DAYS": "0"}),
		"negative form draft days":        base(map[string]string{"RETENTION_FORMS_DRAFT_DAYS": "-3"}),
		"zero attribution days":           base(map[string]string{"ATTRIBUTION_DAYS": "0"}),
		"attribution days past retention": base(map[string]string{"RETENTION_DAYS": "30", "ATTRIBUTION_DAYS": "31"}),
		"bad integer":                     base(map[string]string{"BUFFER_CAPACITY": "many"}),
		"invalid duration":                base(map[string]string{"BUFFER_FLUSH_INTERVAL": "fast"}),
		"negative cache age":              base(map[string]string{"REPORTING_CACHE_MAX_AGE": "-1s"}),
		"negative refresh age":            base(map[string]string{"REPORTING_REFRESH_MAX_AGE": "-1s"}),
		"cache age without a unit":        base(map[string]string{"REPORTING_CACHE_MAX_AGE": "900"}),
		"refresh exceeds cache": base(map[string]string{
			"REPORTING_CACHE_MAX_AGE": "1m", "REPORTING_REFRESH_MAX_AGE": "2m"}),
		"console database not sqlite": base(map[string]string{"CONSOLE_DATABASE_DSN": "/tmp/x.db"}),
	}
	for name, vars := range cases {
		if _, err := FromEnv(func(k string) (string, bool) { v, ok := vars[k]; return v, ok }); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// TestEventsDir: the event files sit in data/ beside the database unless
// EVENTS_DIR moves them; a relative path is made absolute, so it names
// the same directory whatever the working directory.
func TestEventsDir(t *testing.T) {
	db := t.TempDir()
	c, err := load(t, map[string]string{"DATABASE_DSN": "sqlite://" + db + "/a.db"})
	if err != nil || c.EventsDir != filepath.Join(db, "data") || c.EventsDirMoveTo != "" {
		t.Fatalf("default EventsDir = %v, %v; want data/ beside a.db, nothing to move", c, err)
	}
	c, err = load(t, map[string]string{"EVENTS_DIR": "/srv/events"})
	if err != nil || c.EventsDir != "/srv/events" {
		t.Fatalf("EventsDir = %v, %v; want /srv/events", c, err)
	}
	wd, _ := os.Getwd()
	c, err = load(t, map[string]string{"EVENTS_DIR": "events"})
	if err != nil || c.EventsDir != filepath.Join(wd, "events") {
		t.Fatalf("relative EventsDir = %v, %v; want %s", c, err, filepath.Join(wd, "events"))
	}
}

// TestEventsDirOfAnOlderRelease: where only the raw/ an older release
// wrote beside the database exists, the event files are read there and
// moved to data/ by the ingest process; once data/ exists, or EVENTS_DIR
// is set, raw/ is left alone.
func TestEventsDirOfAnOlderRelease(t *testing.T) {
	db := t.TempDir()
	if err := os.Mkdir(filepath.Join(db, "raw"), 0o750); err != nil {
		t.Fatal(err)
	}
	dsn := map[string]string{"DATABASE_DSN": "sqlite://" + db + "/a.db"}
	c, err := load(t, dsn)
	if err != nil || c.EventsDir != filepath.Join(db, "raw") || c.EventsDirMoveTo != filepath.Join(db, "data") {
		t.Fatalf("EventsDir, EventsDirMoveTo = %v, %v; want raw/, to move to data/", c, err)
	}
	c, err = load(t, map[string]string{"DATABASE_DSN": dsn["DATABASE_DSN"], "EVENTS_DIR": filepath.Join(db, "raw")})
	if err != nil || c.EventsDirMoveTo != "" {
		t.Fatalf("with EVENTS_DIR set: %v, %v; want nothing to move", c, err)
	}
	if err := os.Mkdir(filepath.Join(db, "data"), 0o750); err != nil {
		t.Fatal(err)
	}
	c, err = load(t, dsn)
	if err != nil || c.EventsDir != filepath.Join(db, "data") || c.EventsDirMoveTo != "" {
		t.Fatalf("with data/ there: %v, %v; want data/, nothing to move", c, err)
	}
}

// TestEventsDirMustNotHoldTheDatabase: the SQL engine reads all of
// EVENTS_DIR, so a database inside it would be within reach of widget and
// query SQL (raw-first spec D8).
func TestEventsDirMustNotHoldTheDatabase(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"the database's directory": {"EVENTS_DIR": "/tmp"},
		"a parent of it":           {"EVENTS_DIR": "/"},
		"the console's database's": {"EVENTS_DIR": "/srv/x", "CONSOLE_DATABASE_DSN": "sqlite:///srv/x/replica.db"},
	} {
		if _, err := load(t, vars); err == nil || !strings.Contains(err.Error(), "EVENTS_DIR") {
			t.Errorf("%s: err = %v, want a refusal naming EVENTS_DIR", name, err)
		}
	}
	if _, err := load(t, map[string]string{"EVENTS_DIR": "/tmp/rawer"}); err != nil {
		t.Errorf("a sibling directory: %v", err)
	}
}

func TestRetentionDaysFromEnv(t *testing.T) {
	c, err := load(t, map[string]string{"RETENTION_DAYS": "1095"})
	if err != nil || c.Retention.Days != 1095 {
		t.Fatalf("Retention.Days = %v, %v; want 1095", c, err)
	}
}

func TestFormsAttributionDaysDefault(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Forms.AttributionDays != 7 {
		t.Errorf("AttributionDays = %d, want 7", c.Forms.AttributionDays)
	}
}

// Unset, ATTRIBUTION_DAYS follows a RETENTION_DAYS below its default, so
// such an install still boots; set explicitly past RETENTION_DAYS, it is
// refused.
func TestAttributionDaysWithinShortRetention(t *testing.T) {
	c, err := load(t, map[string]string{"RETENTION_DAYS": "3"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Forms.AttributionDays != 3 {
		t.Errorf("AttributionDays = %d, want 3", c.Forms.AttributionDays)
	}
	if _, err := load(t, map[string]string{"RETENTION_DAYS": "3", "ATTRIBUTION_DAYS": "5"}); err == nil {
		t.Error("ATTRIBUTION_DAYS=5 past RETENTION_DAYS=3 was accepted")
	}
}

// A draft needs a window of at least a day: 0 would archive every new
// form on the next pass, so it is refused, naming the variable.
func TestFormDraftDaysMessage(t *testing.T) {
	_, err := load(t, map[string]string{"RETENTION_FORMS_DRAFT_DAYS": "0"})
	if err == nil || !strings.Contains(err.Error(), "RETENTION_FORMS_DRAFT_DAYS") {
		t.Errorf("err = %v, want a refusal naming RETENTION_FORMS_DRAFT_DAYS", err)
	}
}

// TestRefreshExceedsCacheMessage pins the refusal's exact text, which
// names both variables and their values so a person can fix the .env
// without hunting for which one to change.
func TestRefreshExceedsCacheMessage(t *testing.T) {
	_, err := load(t, map[string]string{"REPORTING_CACHE_MAX_AGE": "1m", "REPORTING_REFRESH_MAX_AGE": "2m"})
	if err == nil || err.Error() != "config: REPORTING_REFRESH_MAX_AGE (2m0s) exceeds REPORTING_CACHE_MAX_AGE (1m0s)" {
		t.Errorf("err = %v", err)
	}
}

// TestReportingCacheZeroAllowsAnyRefresh: a cache age of 0 disables the
// cache outright (every request recomputes), so a refresh age longer
// than it is meaningless to refuse.
func TestReportingCacheZeroAllowsAnyRefresh(t *testing.T) {
	c, err := load(t, map[string]string{"REPORTING_CACHE_MAX_AGE": "0", "REPORTING_REFRESH_MAX_AGE": "2m"})
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
func TestMaxEventAgeDefault(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.MaxEventAge(); got != 24*time.Hour {
		t.Fatalf("MaxEventAge = %v, want the 1-day late window", got)
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
	if c.Retention.Days != 400 {
		t.Fatalf("old names changed retention: %+v", c.Retention)
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
		{"oauth resource from INGEST_URL", map[string]string{
			"CONSOLE_AUTH_DSN": "oauth://idp.example.com",
			"INGEST_URL":       "https://twillingate.example.com"}, true},
		{"oauth no resource and no INGEST_URL", map[string]string{
			"CONSOLE_AUTH_DSN": "oauth://idp.example.com"}, false},
		{"oauth empty issuer", map[string]string{
			"CONSOLE_AUTH_DSN": "oauth://?resource=https://twillingate.example.com"}, false},
		{"cloudflare removed", map[string]string{
			"CONSOLE_AUTH_DSN": "cloudflare://team.cloudflareaccess.com?aud=aud123"}, false},
		{"token login ok", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/api/mcp/auth_callback&resource=https://mcp.example.com"}, true},
		{"token login resource from INGEST_URL", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=https://claude.ai/api/mcp/auth_callback",
			"INGEST_URL":       "https://mcp.example.com"}, true},
		{"token login one-character password", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=a&redirect=http://localhost/callback&resource=https://mcp.example.com"}, true},
		{"token login http loopback redirect", map[string]string{
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw&redirect=http://127.0.0.1/callback&resource=https://mcp.example.com"}, true},
		{"token login no resource and no INGEST_URL", map[string]string{
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
	t.Run("oauth defaults resource to INGEST_URL and audience to resource", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(map[string]string{
			"CONSOLE_AUTH_DSN": "oauth://idp.example.com",
			"INGEST_URL":       "https://twillingate.example.com"}))
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
	if cfg.Console.QueryTimeout != 30*time.Second || cfg.Console.QueryMaxRows != 1000 {
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
		"INGEST_URL":       "https://mcp.example.com"}))
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
		t.Errorf("resource = %q, want INGEST_URL", m.ResourceURL)
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
	load := func(dsn, ingestURL string) (ConsoleConfig, error) {
		cfg, err := FromEnv(mcpEnv(map[string]string{"CONSOLE_AUTH_DSN": dsn, "INGEST_URL": ingestURL}))
		if err != nil {
			return ConsoleConfig{}, err
		}
		return cfg.Console, cfg.ValidateConsole()
	}
	for _, tc := range []struct {
		name, dsn, ingestURL string
		wantErr              string // substring; "" accepts
		wantResource         string
	}{
		{"http without insecure", "token://ar_x?password=p&resource=http://homelab:8290", "", "insecure=1", ""},
		{"http INGEST_URL without insecure", "token://ar_x?password=p", "http://homelab:8290", "insecure=1", ""},
		{"http with insecure", "token://ar_x?password=p&resource=http://homelab:8290&insecure=1", "", "", "http://homelab:8290"},
		{"http INGEST_URL with insecure", "token://ar_x?password=p&insecure=1", "http://Homelab:8290", "", "http://homelab:8290"},
		{"https with insecure", "token://ar_x?password=p&insecure=1", "https://console.example.com", "", "https://console.example.com"},
		{"insecure=0", "token://ar_x?password=p&resource=http://homelab:8290&insecure=0", "", `insecure="0" must be 1`, ""},
		{"insecure=true", "token://ar_x?password=p&resource=http://homelab:8290&insecure=true", "", `insecure="true" must be 1`, ""},
		{"insecure twice", "token://ar_x?password=p&insecure=1&insecure=1", "http://homelab:8290", "must be 1", ""},
		{"http redirect stays refused", "token://ar_x?password=p&insecure=1&redirect=http://app.example.com/cb", "http://homelab:8290", "may only use http", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := load(tc.dsn, tc.ingestURL)
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
		"LISTEN_ADDR":               "INGEST_ADDR",
		"PUBLIC_URL":                "INGEST_URL",
		"API_ADDR":                  "CONSOLE_ADDR",
		"API_URL":                   "CONSOLE_URL",
		"API_AUTH_DSN":              "CONSOLE_AUTH_DSN",
		"API_DB_PATH":               "CONSOLE_DATABASE_DSN (sqlite://<path>)",
		"CONSOLE_DB_PATH":           "CONSOLE_DATABASE_DSN (sqlite://<path>)",
		"API_QUERY_TIMEOUT":         "CONSOLE_QUERY_TIMEOUT",
		"API_QUERY_MAX_ROWS":        "CONSOLE_QUERY_ROWS_MAX",
		"CONSOLE_QUERY_MAX_ROWS":    "CONSOLE_QUERY_ROWS_MAX",
		"FORMS_DRAFT_DAYS":          "RETENTION_FORMS_DRAFT_DAYS",
		"REPORTING_CACHE_SECONDS":   "REPORTING_CACHE_MAX_AGE (a duration: 900 is 15m)",
		"REPORTING_REFRESH_SECONDS": "REPORTING_REFRESH_MAX_AGE (a duration: 60 is 1m)",
	} {
		env := map[string]string{"DATABASE_DSN": "sqlite:///tmp/x.db", old: "x"}
		_, err := FromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		want := "config: " + old + " was renamed to " + repl
		if err == nil || err.Error() != want {
			t.Errorf("%s set: err = %v, want %q", old, err, want)
		}
	}
}

// CONSOLE_DATABASE_DSN names the database the console reads in the
// DATABASE_DSN form; the read handles open its path.
func TestConsoleDatabaseDSN(t *testing.T) {
	c, err := load(t, map[string]string{"CONSOLE_DATABASE_DSN": "sqlite:///var/lib/replica.db"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Console.Database != "sqlite:///var/lib/replica.db" || c.Console.DBPath != "/var/lib/replica.db" {
		t.Errorf("Database/DBPath = %q/%q", c.Console.Database, c.Console.DBPath)
	}
	if _, err := load(t, map[string]string{"CONSOLE_DATABASE_DSN": "/var/lib/replica.db"}); err == nil ||
		!strings.Contains(err.Error(), "CONSOLE_DATABASE_DSN must be sqlite://<path>") {
		t.Errorf("bare path: err = %v", err)
	}
}

func TestConsoleDefaults(t *testing.T) {
	env := map[string]string{"DATABASE_DSN": "sqlite:///tmp/x.db", "INGEST_ADDR": "127.0.0.1:9"}
	c, err := FromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if c.IngestAddr != "127.0.0.1:9" || c.Console.Addr != "127.0.0.1:9" || c.Console.DBPath != "/tmp/x.db" ||
		c.Console.QueryTimeout != 30*time.Second || c.Console.QueryMaxRows != 1000 {
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
	c, err := load(map[string]string{"INGEST_URL": "https://t.example.com/", "CONSOLE_AUTH_DSN": "token://ar_x?password=pw"})
	if err != nil || c.Console.ResourceURL != "https://t.example.com" {
		t.Errorf("token login default resource = %q, %v", c.Console.ResourceURL, err)
	}
	c, err = load(map[string]string{"INGEST_URL": "https://t.example.com", "CONSOLE_AUTH_DSN": "oauth://idp.example.com"})
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
		if _, err := load(map[string]string{"INGEST_URL": "https://t.example.com/analytics", "CONSOLE_AUTH_DSN": dsn}); err == nil || !strings.Contains(err.Error(), "origin") {
			t.Errorf("%s with a INGEST_URL path: err = %v, want an origin-only refusal", dsn, err)
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
// callback) follows it, falling back to INGEST_URL, and resource= still
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
		{"CONSOLE_URL beside an ingest INGEST_URL", map[string]string{
			"INGEST_URL": "https://t.example.com", "CONSOLE_URL": "https://tapi.example.com/",
			"CONSOLE_AUTH_DSN": "token://ar_x?password=pw"}, "https://tapi.example.com"},
		{"CONSOLE_URL defaults to INGEST_URL", map[string]string{
			"INGEST_URL": "https://t.example.com", "CONSOLE_AUTH_DSN": "token://ar_x?password=pw"}, "https://t.example.com"},
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
	if c, _ := load(map[string]string{"INGEST_URL": "https://t.example.com"}); c.Console.URL != "https://t.example.com" {
		t.Errorf("CONSOLE_URL default = %q, want INGEST_URL", c.Console.URL)
	}

	// A bad value names the variable it came from.
	if _, err := load(map[string]string{"CONSOLE_URL": "https://tapi.example.com/api",
		"CONSOLE_AUTH_DSN": "token://ar_x?password=pw"}); err == nil || !strings.Contains(err.Error(), "CONSOLE_URL=") {
		t.Errorf("CONSOLE_URL with a path: %v, want a refusal naming CONSOLE_URL", err)
	}
	if _, err := load(map[string]string{"INGEST_URL": "https://t.example.com/site",
		"CONSOLE_AUTH_DSN": "token://ar_x?password=pw"}); err == nil || !strings.Contains(err.Error(), "INGEST_URL (CONSOLE_URL's default)") {
		t.Errorf("INGEST_URL with a path: %v, want a refusal naming INGEST_URL as CONSOLE_URL's default", err)
	}
}

// TestLateDaysWithinDefaultRetention: the late window alone is set, the
// retention keeps its default 400, so any value from 1 to 400 loads.
func TestLateDaysWithinDefaultRetention(t *testing.T) {
	for _, v := range []string{"1", "7", "400"} {
		if _, err := load(t, map[string]string{"INGEST_LATE_DAYS": v}); err != nil {
			t.Fatalf("INGEST_LATE_DAYS=%s: %v", v, err)
		}
	}
}

// TestLateDaysWithinRetentionDays: a window larger than the files'
// retention is refused by name, so the operator knows which to change.
func TestLateDaysWithinRetentionDays(t *testing.T) {
	_, err := load(t, map[string]string{"RETENTION_DAYS": "30", "INGEST_LATE_DAYS": "45"})
	if err == nil {
		t.Fatal("INGEST_LATE_DAYS=45 with RETENTION_DAYS=30 accepted")
	}
	for _, want := range []string{"INGEST_LATE_DAYS (45)", "RETENTION_DAYS (30)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
	if _, err := load(t, map[string]string{"RETENTION_DAYS": "30", "INGEST_LATE_DAYS": "30"}); err != nil {
		t.Errorf("INGEST_LATE_DAYS equal to RETENTION_DAYS refused: %v", err)
	}
}

// TestMaxEventAgeIsTheLateWindow: the clamp follows the late window, not
// the retention.
func TestMaxEventAgeIsTheLateWindow(t *testing.T) {
	c, err := load(t, map[string]string{"INGEST_LATE_DAYS": "2", "RETENTION_DAYS": "400"})
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxEventAge() != 48*time.Hour {
		t.Errorf("MaxEventAge = %v, want 48h", c.MaxEventAge())
	}
}

// The variables raw-first PR 6 retired are ignored like any unknown
// one, whatever their value: an old env file still boots.
func TestRetiredVariablesAreIgnored(t *testing.T) {
	c, err := load(t, map[string]string{
		"IDENTITIES_TOP_N": "-1", "RETENTION_EVENTS_AGGREGATE_DAYS": "90", "RETENTION_RAW_DAYS": "0",
		"RETENTION_AGGREGATE_DAYS": "60", "RETENTION_EVENTS_RAW_DAYS": "14",
		"ATTRIBUTE_VALUES_TOP_N": "200", "ATTRIBUTE_BREAKDOWNS_MAX": "7"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Retention.Days != DefaultRetentionDays {
		t.Errorf("Retention.Days = %d, want %d", c.Retention.Days, DefaultRetentionDays)
	}
}

// TestRetiredEngineSettingsAreIgnored: an install that still sets
// ENGINE_MEMORY_LIMIT or ENGINE_MAX_CHILDREN, whatever the value, loads,
// and the warning names only the ones it sets.
func TestRetiredEngineSettingsAreIgnored(t *testing.T) {
	for _, c := range []struct {
		vars map[string]string
		want string
	}{
		{map[string]string{"ENGINE_MEMORY_LIMIT": "lots", "ENGINE_MAX_CHILDREN": "0"},
			"ENGINE_MEMORY_LIMIT and ENGINE_MAX_CHILDREN are ignored: SQL runs in an external SQL engine"},
		{map[string]string{"ENGINE_MEMORY_LIMIT": "512MB"}, "ENGINE_MEMORY_LIMIT is ignored: SQL runs in an external SQL engine"},
		{map[string]string{"ENGINE_MAX_CHILDREN": "2"}, "ENGINE_MAX_CHILDREN is ignored: SQL runs in an external SQL engine"},
		{map[string]string{"ENGINE_MAX_CHILDREN": ""}, ""},
	} {
		cfg, err := load(t, c.vars)
		if err != nil {
			t.Fatalf("%v: %v", c.vars, err)
		}
		if got := cfg.IgnoredWarning(); got != c.want {
			t.Errorf("%v: warning = %q, want %q", c.vars, got, c.want)
		}
	}
}
