// Package config loads infra settings from the process environment
// (12-factor; systemd/compose/make load the env *file*, the process reads
// real env vars). It reads no file: the project list lives in the
// registry (internal/manage).
// stdlib only: net/url for DSN scheme checks.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type LogConfig struct {
	Level  string
	Format string
	File   string
}

type BufferConfig struct {
	FlushMaxEvents int
	FlushInterval  time.Duration
	Capacity       int
}

type Retention struct {
	// Days (RETENTION_DAYS) is how long the event files under raw/ are kept:
	// a month file is deleted once its last day is older (raw-first D5).
	Days int `json:"days"`
	// ArchivedDays is how long an archived project, dashboard or widget
	// survives before the daily pass deletes it. 0 keeps archived items
	// forever.
	ArchivedDays int `json:"archived_days"`
}

// FormsConfig carries the forms settings. DraftDays
// (RETENTION_FORMS_DRAFT_DAYS) is how long a form created by its first
// submission accepts submissions as a draft before the daily pass archives
// it unapproved; it is read when a draft is created or restored, so a
// change moves no existing window. AttributionDays (ATTRIBUTION_DAYS) is
// how far before a submission its source is looked for: its submitter's
// last non-direct visit that started within that many days.
type FormsConfig struct {
	DraftDays       int
	AttributionDays int
}

// ReportingConfig sizes the two-age cache a sql widget's loaded value is
// served from (internal/reporting): an ordinary request reuses a value up
// to CacheAge old; a fresh=true request reuses one younger than this
// (RefreshAge instead), which must not exceed CacheAge when that is
// non-zero. REPORTING_CACHE_MAX_AGE 0 turns the cache off outright: every
// request, fresh or not, loads again.
type ReportingConfig struct {
	CacheAge, RefreshAge time.Duration
}

// EngineConfig sizes the DuckDB children (raw-first spec D8, D23).
type EngineConfig struct {
	// MemoryLimit (ENGINE_MEMORY_LIMIT) is each child's DuckDB
	// memory_limit, in DuckDB's units (MB is 10^6 bytes, MiB 2^20).
	MemoryLimit string
	// MaxChildren (ENGINE_MAX_CHILDREN) bounds the children running at
	// once across every process on the data directory.
	MaxChildren int
}

// The engine defaults: 512 MB is the least a COUNT(DISTINCT) over three
// years at the cap runs in, and two children are for a split install's two processes.
const (
	DefaultEngineMemoryLimit = "512MB"
	DefaultEngineMaxChildren = 2
)

// engineMemory is the memory_limit forms DuckDB takes, a unit required.
var engineMemory = regexp.MustCompile(`(?i)^[0-9]+(\.[0-9]+)?\s*(KB|MB|GB|TB|KiB|MiB|GiB|TiB)$`)

// ConsoleConfig carries the console surface settings (serve -console). Authentication comes from
// the single CONSOLE_AUTH_DSN; parsing fans it out into the mode-specific
// fields the verifiers consume:
//
//	token://<token>[?password=<pw>[&redirect=<host>…][&resource=<url>]]
//	oauth://<issuer-host>[/path][?resource=<url>][&audience=<aud>]
//
// password= on token:// turns on the browser login server, which needs a
// resource URL. Callbacks are allowed by host: loopback, claude.ai and
// chatgpt.com always, and each redirect= adds one more. In token and oauth
// modes resource defaults to CONSOLE_URL (itself defaulting to INGEST_URL)
// and must be an origin (scheme://host[:port], no path): one identifier covers /mcp and /api/.
// The oauth issuer is https://<host>[/path] and audience defaults to the
// resource. oauth+insecure produces an http issuer for local IdPs and tests.
type ConsoleConfig struct {
	Addr          string // CONSOLE_ADDR, defaults to IngestAddr
	URL           string // CONSOLE_URL, the console's public origin; defaults to INGEST_URL
	Database      string // CONSOLE_DATABASE_DSN, sqlite:// only; defaults to DATABASE_DSN
	DBPath        string // Database's file path, what the console's read handles open
	AuthDSN       string // CONSOLE_AUTH_DSN, verbatim
	AuthMode      string // "oauth" | "token", from the DSN scheme
	ResourceURL   string
	Issuer        string
	Audience      string
	Token         string
	RedirectHosts []string      // token:// redirect=, callback hosts beyond loopback, claude.ai and chatgpt.com
	Password      string        // token:// password=; set, it turns on the login server
	QueryTimeout  time.Duration // CONSOLE_QUERY_TIMEOUT, default 30s
	QueryMaxRows  int           // CONSOLE_QUERY_ROWS_MAX, default 1000

	// Insecure is token:// insecure=1: the console origin (resource) may
	// be plain http on any host, for a network that encrypts the link
	// itself (a tailnet, a VPN). The password and tokens cross it as sent.
	Insecure bool

	// audienceGiven records an explicit oauth audience=, which the verifier
	// accepts alone; the default admits the resource and resource/mcp.
	audienceGiven bool

	// authErr holds the DSN parse failure until ValidateConsole reports it:
	// bare `serve` must stay lenient (warn and skip the console), so FromEnv
	// cannot fail on a broken auth DSN.
	authErr error
}

// DefaultRetentionDays is RETENTION_DAYS's default (RETENTION_ARCHIVED_DAYS
// is the archive's own). A little over a year, so a year-on-year comparison
// still has last year's day.
const (
	DefaultArchivedDays  = 30
	DefaultRetentionDays = 400
)

// DefaultIngestLateDays is INGEST_LATE_DAYS's default: a day's grace for
// a late event, the most a phone offline overnight needs.
const DefaultIngestLateDays = 1

// DefaultFormDraftDays is RETENTION_FORMS_DRAFT_DAYS's default: a week to
// see what a new form sends and approve it.
const DefaultFormDraftDays = 7

// DefaultFormAttributionDays is ATTRIBUTION_DAYS's default: a week back
// from a submission to the visit that brought its submitter. Unset, the
// default is RETENTION_DAYS when that is smaller, so an install that keeps
// fewer days still boots; a value set explicitly past RETENTION_DAYS is
// refused.
const DefaultFormAttributionDays = 7

type Config struct {
	IngestAddr string
	// IngestDisabled (INGEST_DISABLED) refuses every ingest route.
	IngestDisabled bool
	// IngestLateDays (INGEST_LATE_DAYS) is how far back a client timestamp
	// may lie: older ones are clamped to received − IngestLateDays. At most
	// Retention.Days, so a late event never reaches a day whose file has
	// been deleted.
	IngestLateDays int
	Database       string
	// EventsDir (EVENTS_DIR) holds the event files (internal/archive):
	// absolute, beside the database's file as raw/ unless set. It never
	// holds the database: the external SQL engine reads it whole.
	EventsDir string
	// SQLEngineSocket (SQL_ENGINE_SOCKET) is the Unix socket of the
	// external SQL engine that answers widget SQL and the query tool's
	// sql (package sqlengine); "" means none, and SQL is refused.
	SQLEngineSocket string
	Geo             string
	IngestURL       string
	Log             LogConfig
	Buffer          BufferConfig
	Retention       Retention
	Forms           FormsConfig
	Reporting       ReportingConfig
	Engine          EngineConfig
	Console         ConsoleConfig
}

// Load builds the configuration from the process environment.
func Load() (*Config, error) {
	return FromEnv(os.LookupEnv)
}

// env reads typed values from a lookup function, remembering the first error.
type env struct {
	lookup func(string) (string, bool)
	err    error
}

func (e *env) str(key, def string) string {
	if v, ok := e.lookup(key); ok && v != "" {
		return v
	}
	return def
}

func (e *env) num(key string, def int) int {
	v, ok := e.lookup(key)
	if !ok || v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		if e.err == nil {
			e.err = fmt.Errorf("config: %s: invalid integer %q", key, v)
		}
		return def
	}
	return n
}

func (e *env) dur(key string, def time.Duration) time.Duration {
	v, ok := e.lookup(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		if e.err == nil {
			e.err = fmt.Errorf("config: %s: invalid duration %q", key, v)
		}
		return def
	}
	return d
}

// bool reads Go's boolean spellings (strconv.ParseBool: 1, t, true, TRUE,
// True and the 0/f/false counterparts); anything else is an error rather
// than a guess at which way a typo was meant.
func (e *env) bool(key string, def bool) bool {
	v, ok := e.lookup(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		if e.err == nil {
			e.err = fmt.Errorf("config: %s: invalid boolean %q (true or false, 1 or 0)", key, v)
		}
		return def
	}
	return b
}

// FromEnv parses the environment via lookup (os.LookupEnv in production,
// a map lookup in tests).
func FromEnv(lookup func(string) (string, bool)) (*Config, error) {
	if err := refuseRenamed(lookup); err != nil {
		return nil, err
	}
	e := &env{lookup: lookup}
	retentionDays := e.num("RETENTION_DAYS", DefaultRetentionDays)
	c := &Config{
		IngestAddr: e.str("INGEST_ADDR", "127.0.0.1:8080"),
		// A complete stop of ingest: every route that takes events or
		// form submissions refuses with 429; the console is unaffected.
		IngestDisabled: e.bool("INGEST_DISABLED", false),
		IngestLateDays: e.num("INGEST_LATE_DAYS", DefaultIngestLateDays),
		Database:       e.str("DATABASE_DSN", ""),
		Geo:            e.str("GEO_DSN", "cloudflare://"),
		// The collector's public base URL (https://twillingate.example.com).
		// Embed snippets and MCP integration guidance are built from it;
		// unset, they carry a placeholder and tell the model to ask.
		IngestURL: strings.TrimSuffix(e.str("INGEST_URL", ""), "/"),
		Log: LogConfig{
			Level:  e.str("LOG_LEVEL", "info"),
			Format: e.str("LOG_FORMAT", "json"),
			File:   e.str("LOG_FILE", ""),
		},
		Buffer: BufferConfig{
			FlushMaxEvents: e.num("BUFFER_FLUSH_MAX_EVENTS", 1000),
			FlushInterval:  e.dur("BUFFER_FLUSH_INTERVAL", 5*time.Second),
			Capacity:       e.num("BUFFER_CAPACITY", 10000),
		},
		Retention: Retention{
			Days:         retentionDays,
			ArchivedDays: e.num("RETENTION_ARCHIVED_DAYS", DefaultArchivedDays),
		},
		Forms: FormsConfig{DraftDays: e.num("RETENTION_FORMS_DRAFT_DAYS", DefaultFormDraftDays),
			AttributionDays: e.num("ATTRIBUTION_DAYS", min(DefaultFormAttributionDays, retentionDays))},
		Reporting: ReportingConfig{
			CacheAge:   e.dur("REPORTING_CACHE_MAX_AGE", 15*time.Minute),
			RefreshAge: e.dur("REPORTING_REFRESH_MAX_AGE", time.Minute),
		},
		Engine: EngineConfig{
			MemoryLimit: e.str("ENGINE_MEMORY_LIMIT", DefaultEngineMemoryLimit),
			MaxChildren: e.num("ENGINE_MAX_CHILDREN", DefaultEngineMaxChildren),
		},
	}
	c.Console = ConsoleConfig{
		Addr: e.str("CONSOLE_ADDR", c.IngestAddr),
		// The console's public address, when it has a hostname of its own
		// (https://console.example.com beside an ingest-only INGEST_URL). The
		// login's resource, issuer and dashboards callback follow it, so the
		// console's own host needs no redirect= entry.
		URL:          strings.TrimSuffix(e.str("CONSOLE_URL", c.IngestURL), "/"),
		Database:     e.str("CONSOLE_DATABASE_DSN", c.Database),
		AuthDSN:      e.str("CONSOLE_AUTH_DSN", ""),
		QueryTimeout: e.dur("CONSOLE_QUERY_TIMEOUT", 30*time.Second),
		QueryMaxRows: e.num("CONSOLE_QUERY_ROWS_MAX", 1000),
	}
	c.Console.DBPath = strings.TrimPrefix(c.Console.Database, "sqlite://")
	c.SQLEngineSocket = e.str("SQL_ENGINE_SOCKET", "")
	c.EventsDir = e.str("EVENTS_DIR", filepath.Join(filepath.Dir(strings.TrimPrefix(c.Database, "sqlite://")), "raw"))
	if abs, err := filepath.Abs(c.EventsDir); err == nil {
		c.EventsDir = abs
	}
	if c.Console.AuthDSN != "" {
		c.Console.authErr = c.parseConsoleAuthDSN()
	}
	if e.err != nil {
		return nil, e.err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// renamed maps each retired variable to its current name. A set old name
// refuses the boot, so a leftover one cannot silently stop taking effect.
// A name retired twice points at the latest, and a replacement that takes
// its value in another form says so.
var renamed = []struct{ old, repl string }{
	{"LISTEN_ADDR", "INGEST_ADDR"},
	{"PUBLIC_URL", "INGEST_URL"},
	{"API_ADDR", "CONSOLE_ADDR"},
	{"API_URL", "CONSOLE_URL"},
	{"API_AUTH_DSN", "CONSOLE_AUTH_DSN"},
	{"API_DB_PATH", "CONSOLE_DATABASE_DSN (sqlite://<path>)"},
	{"CONSOLE_DB_PATH", "CONSOLE_DATABASE_DSN (sqlite://<path>)"},
	{"API_QUERY_TIMEOUT", "CONSOLE_QUERY_TIMEOUT"},
	{"API_QUERY_MAX_ROWS", "CONSOLE_QUERY_ROWS_MAX"},
	{"CONSOLE_QUERY_MAX_ROWS", "CONSOLE_QUERY_ROWS_MAX"},
	{"FORMS_DRAFT_DAYS", "RETENTION_FORMS_DRAFT_DAYS"},
	{"REPORTING_CACHE_SECONDS", "REPORTING_CACHE_MAX_AGE (a duration: 900 is 15m)"},
	{"REPORTING_REFRESH_SECONDS", "REPORTING_REFRESH_MAX_AGE (a duration: 60 is 1m)"},
}

// refuseRenamed treats an empty value as unset, as env.str does, so a
// leftover `LISTEN_ADDR=` line does not block the boot.
func refuseRenamed(lookup func(string) (string, bool)) error {
	for _, r := range renamed {
		if v, ok := lookup(r.old); ok && v != "" {
			return fmt.Errorf("config: %s was renamed to %s", r.old, r.repl)
		}
	}
	return nil
}

func (c *Config) validate() error {
	if c.Database == "" {
		return fmt.Errorf("config: DATABASE_DSN is required")
	}
	for _, dsn := range []string{c.Database, c.Geo} {
		u, err := url.Parse(dsn)
		if err != nil || u.Scheme == "" {
			return fmt.Errorf("config: invalid DSN %q", dsn)
		}
	}
	// The console reads with its own SQLite handles, whatever the store.
	if !strings.HasPrefix(c.Console.Database, "sqlite://") {
		return fmt.Errorf("config: CONSOLE_DATABASE_DSN must be sqlite://<path>: %q", c.Console.Database)
	}
	// The engine child may write where it may read: a database file inside
	// EVENTS_DIR would be within reach of widget and query SQL.
	for _, db := range []string{c.Database, c.Console.Database} {
		path, err := filepath.Abs(strings.TrimPrefix(db, "sqlite://"))
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(c.EventsDir, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("config: EVENTS_DIR (%s) must not hold the database (%s): the SQL engine reads everything in it", c.EventsDir, path)
		}
	}
	if c.IngestLateDays < 1 {
		return fmt.Errorf("config: INGEST_LATE_DAYS must be at least 1: %d", c.IngestLateDays)
	}
	if c.IngestLateDays > c.Retention.Days {
		return fmt.Errorf("config: INGEST_LATE_DAYS (%d) exceeds RETENTION_DAYS (%d): a late event could land in a day whose file is already deleted",
			c.IngestLateDays, c.Retention.Days)
	}
	if c.Retention.ArchivedDays < 0 {
		return fmt.Errorf("config: RETENTION_ARCHIVED_DAYS must not be negative: %d", c.Retention.ArchivedDays)
	}
	if c.Retention.Days < 1 {
		return fmt.Errorf("config: RETENTION_DAYS must be at least 1: %d", c.Retention.Days)
	}
	if c.Forms.DraftDays < 1 {
		return fmt.Errorf("config: RETENTION_FORMS_DRAFT_DAYS must be at least 1: %d", c.Forms.DraftDays)
	}
	if c.Forms.AttributionDays < 1 || c.Forms.AttributionDays > c.Retention.Days {
		return fmt.Errorf("config: ATTRIBUTION_DAYS must be from 1 to RETENTION_DAYS (%d): %d",
			c.Retention.Days, c.Forms.AttributionDays)
	}
	if c.Reporting.CacheAge < 0 {
		return fmt.Errorf("config: REPORTING_CACHE_MAX_AGE must not be negative")
	}
	if c.Reporting.RefreshAge < 0 {
		return fmt.Errorf("config: REPORTING_REFRESH_MAX_AGE must not be negative")
	}
	if c.Reporting.CacheAge > 0 && c.Reporting.RefreshAge > c.Reporting.CacheAge {
		return fmt.Errorf("config: REPORTING_REFRESH_MAX_AGE (%s) exceeds REPORTING_CACHE_MAX_AGE (%s)",
			c.Reporting.RefreshAge, c.Reporting.CacheAge)
	}
	if !engineMemory.MatchString(c.Engine.MemoryLimit) {
		return fmt.Errorf("config: ENGINE_MEMORY_LIMIT must be a size with a unit, like 512MB or 1GiB: %q", c.Engine.MemoryLimit)
	}
	if c.Engine.MaxChildren < 1 {
		return fmt.Errorf("config: ENGINE_MAX_CHILDREN must be at least 1: %d", c.Engine.MaxChildren)
	}
	return nil
}

// MaxEventAge is the late window (INGEST_LATE_DAYS): how far back a client
// timestamp may lie before it is clamped. Validation keeps it within
// RETENTION_DAYS, so a clamped timestamp never lands in a day whose file is
// already deleted.
func (c *Config) MaxEventAge() time.Duration {
	return time.Duration(c.IngestLateDays) * 24 * time.Hour
}

// parseConsoleAuthDSN fans CONSOLE_AUTH_DSN out into the mode-specific ConsoleConfig
// fields. Called from parse once the rest of the config (INGEST_URL for
// the oauth resource default) is known.
func (c *Config) parseConsoleAuthDSN() error {
	m := &c.Console
	scheme, rest, ok := strings.Cut(m.AuthDSN, "://")
	if !ok {
		return fmt.Errorf("config: invalid CONSOLE_AUTH_DSN %q (token://<token> or oauth://<issuer-host>)", m.AuthDSN)
	}
	switch scheme {
	case "token":
		// Not URL-parsed: the token is opaque and must survive verbatim.
		// Everything after the first '?' configures the login server.
		token, query, hasQuery := strings.Cut(rest, "?")
		m.AuthMode = "token"
		m.Token = token
		if m.Token == "" {
			return fmt.Errorf("config: CONSOLE_AUTH_DSN token:// requires a token (mint with `twillingate keygen -console`)")
		}
		if hasQuery {
			return c.parseTokenLogin(query)
		}
	case "oauth", "oauth+insecure":
		u, err := url.Parse(m.AuthDSN)
		if err != nil {
			return fmt.Errorf("config: invalid CONSOLE_AUTH_DSN: %v", err)
		}
		if u.Host == "" {
			return fmt.Errorf("config: CONSOLE_AUTH_DSN oauth:// requires an issuer host (oauth://idp.example.com)")
		}
		m.AuthMode = "oauth"
		issuerScheme := "https"
		if scheme == "oauth+insecure" {
			issuerScheme = "http"
		}
		m.Issuer = issuerScheme + "://" + u.Host + strings.TrimSuffix(u.Path, "/")
		q := u.Query()
		if m.ResourceURL, err = c.resource("oauth://", q.Get("resource")); err != nil {
			return err
		}
		if m.ResourceURL == "" {
			return fmt.Errorf("config: CONSOLE_AUTH_DSN oauth:// requires CONSOLE_URL, INGEST_URL or ?resource=<origin> to derive the resource from")
		}
		m.Audience = q.Get("audience")
		m.audienceGiven = m.Audience != ""
		if m.Audience == "" {
			m.Audience = m.ResourceURL
		}
	default:
		return fmt.Errorf("config: unknown CONSOLE_AUTH_DSN scheme %q (token or oauth)", scheme)
	}
	return nil
}

// parseTokenLogin reads the token:// query that turns on the browser login
// server: password=, repeated redirect=, resource= and insecure=1.
func (c *Config) parseTokenLogin(query string) error {
	m := &c.Console
	q, err := url.ParseQuery(query)
	if err != nil {
		return fmt.Errorf("config: invalid CONSOLE_AUTH_DSN token:// query: %v", err)
	}
	for k := range q {
		if k != "redirect" && k != "password" && k != "resource" && k != "insecure" {
			return fmt.Errorf("config: CONSOLE_AUTH_DSN token:// has unknown parameter %q (redirect, password, resource or insecure)", k)
		}
	}
	if v, ok := q["insecure"]; ok {
		if len(v) != 1 || v[0] != "1" {
			return fmt.Errorf("config: CONSOLE_AUTH_DSN token:// insecure=%q must be 1", strings.Join(v, ","))
		}
		m.Insecure = true
	}
	m.Password = q.Get("password")
	if m.Password == "" {
		return fmt.Errorf("config: CONSOLE_AUTH_DSN token:// redirect= and resource= need a password=, which turns the login on")
	}
	for _, r := range q["redirect"] {
		host, err := redirectHost(r)
		if err != nil {
			return fmt.Errorf("config: CONSOLE_AUTH_DSN token:// redirect=%q %v", r, err)
		}
		m.RedirectHosts = append(m.RedirectHosts, host)
	}
	if m.ResourceURL, err = c.resource("token://", q.Get("resource")); err != nil {
		return err
	}
	if m.ResourceURL == "" {
		return fmt.Errorf("config: CONSOLE_AUTH_DSN token:// password= requires CONSOLE_URL, INGEST_URL or resource=<origin> to derive the resource from")
	}
	if err := checkLoginURL(m.ResourceURL, m.Insecure); err != nil {
		return fmt.Errorf("config: CONSOLE_AUTH_DSN token:// resource=%q %v", m.ResourceURL, err)
	}
	return nil
}

// resource resolves the console resource identifier: resource= when given,
// else CONSOLE_URL (which defaults to INGEST_URL), either way read as an
// origin. Empty when none is set.
func (c *Config) resource(scheme, given string) (string, error) {
	switch {
	case given != "":
		r, err := resourceOrigin(given)
		if err != nil {
			return "", fmt.Errorf("config: CONSOLE_AUTH_DSN %s resource=%q %v", scheme, given, err)
		}
		return r, nil
	case c.Console.URL != "":
		r, err := resourceOrigin(c.Console.URL)
		if err != nil {
			name := "CONSOLE_URL"
			if c.Console.URL == c.IngestURL {
				name = "INGEST_URL (CONSOLE_URL's default)"
			}
			return "", fmt.Errorf("config: %s=%q %v, or set CONSOLE_URL", name, c.Console.URL, err)
		}
		return r, nil
	}
	return "", nil
}

// resourceOrigin reads a resource= value as the console origin: an absolute
// http(s) URL with no path beyond "/", returned without the slash. One
// identifier covers /mcp and /api/, and matches the host-rooted RFC 9728
// metadata the console serves. The host is lowercased and a default port (443
// for https, 80 for http) is dropped, so resource=https://Console.example.com:443
// names the same origin a client actually connects to (case-insensitive,
// port-implicit) rather than comparing as a distinct string.
func resourceOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("must be an absolute http(s) origin such as https://console.example.com")
	}
	if (u.Path != "" && u.Path != "/") || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return "", fmt.Errorf("must be an origin with no path, userinfo, query or fragment, such as https://console.example.com (the console serves /mcp and /api/ under it)")
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") { // IPv6; Hostname() strips the brackets Host carried
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && !((u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80")) {
		host += ":" + port
	}
	return u.Scheme + "://" + host, nil
}

// redirectHost reads a redirect= value as the host it allows: a bare host
// (app.example.com, 127.0.0.1, [::1]) or, for DSNs written against exact
// callbacks, a full URL whose host is taken.
func redirectHost(raw string) (string, error) {
	if strings.Contains(raw, "://") {
		if err := checkLoginURL(raw, false); err != nil {
			return "", err
		}
		u, _ := url.Parse(raw)
		return strings.ToLower(u.Hostname()), nil
	}
	u, err := url.Parse("https://" + raw)
	if raw == "" || err != nil || u.Host != raw || u.Port() != "" {
		return "", fmt.Errorf("must be a host such as app.example.com, without scheme, port or path")
	}
	return strings.ToLower(u.Hostname()), nil
}

// checkLoginURL admits an absolute http(s) URL with no fragment, and plain
// http only on a loopback host, or on any host with anyHTTP (insecure=1).
func checkLoginURL(raw string, anyHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("does not parse: %v", err)
	}
	if u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("must be an absolute http(s) URL")
	}
	if strings.Contains(raw, "#") {
		return fmt.Errorf("must not carry a fragment")
	}
	if h := u.Hostname(); u.Scheme == "http" && !anyHTTP && h != "localhost" && h != "127.0.0.1" && h != "::1" {
		return fmt.Errorf("may only use http on localhost, 127.0.0.1 or [::1], or anywhere with insecure=1 on a network that encrypts the link itself")
	}
	return nil
}

// AudienceGiven reports whether oauth:// named its audience= explicitly
// rather than defaulting it to the resource.
func (m ConsoleConfig) AudienceGiven() bool { return m.audienceGiven }

// LoginEnabled reports whether token:// runs the browser login server.
func (m ConsoleConfig) LoginEnabled() bool { return m.AuthMode == "token" && m.Password != "" }

// ValidateConsole fail-fasts the console surface (endpoint spec §4): there is no
// unauthenticated mode and no way to reach one by omission.
func (c *Config) ValidateConsole() error {
	m := c.Console
	if m.AuthDSN == "" {
		return fmt.Errorf("config: -console requires CONSOLE_AUTH_DSN (token://<token> or oauth://<issuer-host>)")
	}
	return m.authErr
}
