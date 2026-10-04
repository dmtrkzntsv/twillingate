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

type RetentionClass struct {
	RawDays       int `json:"raw_days"`
	AggregateDays int `json:"aggregate_days"`
}

type Retention struct {
	// Events is every family's window: raw rows are rolled up and deleted
	// after RawDays; aggregates, actors, cohorts and identities are kept
	// AggregateDays.
	Events RetentionClass `json:"events"`
	// ArchivedDays is how long an archived project, dashboard or widget
	// survives before the daily pass deletes it. 0 keeps archived items
	// forever.
	ArchivedDays int `json:"archived_days"`
}

// ReportingConfig sizes the two-age cache a sql widget's loaded value is
// served from (internal/reporting): an ordinary request reuses a value up
// to CacheAge old; a fresh=true request reuses one younger than this
// (RefreshAge instead), which must not exceed CacheAge when that is
// non-zero. REPORTING_CACHE_SECONDS 0 turns the cache off outright: every
// request, fresh or not, loads again.
type ReportingConfig struct {
	CacheAge, RefreshAge time.Duration
}

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
// modes resource defaults to CONSOLE_URL (itself defaulting to PUBLIC_URL)
// and must be an origin (scheme://host[:port], no path): one identifier covers /mcp and /api/.
// The oauth issuer is https://<host>[/path] and audience defaults to the
// resource. oauth+insecure produces an http issuer for local IdPs and tests.
type ConsoleConfig struct {
	Addr          string // CONSOLE_ADDR, defaults to IngestAddr
	URL           string // CONSOLE_URL, the console's public origin; defaults to PUBLIC_URL
	DBPath        string // CONSOLE_DB_PATH, defaults to DATABASE_DSN path
	AuthDSN       string // CONSOLE_AUTH_DSN, verbatim
	AuthMode      string // "oauth" | "token", from the DSN scheme
	ResourceURL   string
	Issuer        string
	Audience      string
	Token         string
	RedirectHosts []string      // token:// redirect=, callback hosts beyond loopback, claude.ai and chatgpt.com
	Password      string        // token:// password=; set, it turns on the login server
	QueryTimeout  time.Duration // CONSOLE_QUERY_TIMEOUT, default 10s
	QueryMaxRows  int           // CONSOLE_QUERY_MAX_ROWS, default 1000

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

// The caps' defaults (ATTRIBUTE_VALUES_TOP_N, IDENTITIES_TOP_N), sized for
// an indie site or app. ATTRIBUTE_VALUES_TOP_N is one cap for every
// client-supplied value, views breakdowns and attributes alike: past the
// 100th path of a day, real traffic is single crawler hits, and attributes
// are mostly a handful of values. Identities are dropped past their cap,
// so theirs sits well above a small app's daily users. The console's
// limits tool reports them beside the values in force, so they live here
// rather than as literals in parse.
//
// ATTRIBUTE_BREAKDOWNS_MAX: the attributes all active projects may declare
// together; each is a breakdown with its own aggregate rows.
const (
	DefaultAttributeValuesTopN    = 100
	DefaultAttributeBreakdownsMax = 10
	DefaultIdentitiesTopN         = 500
)

// The retention defaults (RETENTION_EVENTS_RAW_DAYS,
// RETENTION_EVENTS_AGGREGATE_DAYS, RETENTION_ARCHIVED_DAYS), reported by the
// limits tool beside the values in force like the caps'.
const (
	DefaultRawDays       = 30
	DefaultAggregateDays = 365
	DefaultArchivedDays  = 30
)

type Config struct {
	IngestAddr          string
	Database            string
	Geo                 string
	PublicURL           string
	Log                 LogConfig
	Buffer              BufferConfig
	Retention           Retention
	AttributeValuesTopN int
	// AttributeBreakdownsMax bounds the attributes all active projects
	// declare together; 0 is no limit.
	AttributeBreakdownsMax int
	IdentitiesTopN         int
	Reporting              ReportingConfig
	Console                ConsoleConfig
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

// FromEnv parses the environment via lookup (os.LookupEnv in production,
// a map lookup in tests).
func FromEnv(lookup func(string) (string, bool)) (*Config, error) {
	if err := refuseRenamed(lookup); err != nil {
		return nil, err
	}
	e := &env{lookup: lookup}
	c := &Config{
		IngestAddr: e.str("INGEST_ADDR", "127.0.0.1:8080"),
		Database:   e.str("DATABASE_DSN", ""),
		Geo:        e.str("GEO_DSN", "cloudflare://"),
		// The collector's public base URL (https://twillingate.example.com).
		// Embed snippets and MCP integration guidance are built from it;
		// unset, they carry a placeholder and tell the model to ask.
		PublicURL: strings.TrimSuffix(e.str("PUBLIC_URL", ""), "/"),
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
			Events: RetentionClass{
				RawDays:       e.num("RETENTION_EVENTS_RAW_DAYS", DefaultRawDays),
				AggregateDays: e.num("RETENTION_EVENTS_AGGREGATE_DAYS", DefaultAggregateDays),
			},
			ArchivedDays: e.num("RETENTION_ARCHIVED_DAYS", DefaultArchivedDays),
		},
		// Distinct client-supplied *values* per declared attribute key are
		// capped globally rather than per project (spec: the operator picks
		// the key, clients pick the values). The same cap holds the values
		// of each views breakdown, and IDENTITIES_TOP_N the users and groups
		// a day. Each cap keeps the aggregates, which outlive raw rows, from
		// growing with a dimension that carries ids; 0 keeps every value.
		AttributeValuesTopN:    e.num("ATTRIBUTE_VALUES_TOP_N", DefaultAttributeValuesTopN),
		AttributeBreakdownsMax: e.num("ATTRIBUTE_BREAKDOWNS_MAX", DefaultAttributeBreakdownsMax),
		IdentitiesTopN:         e.num("IDENTITIES_TOP_N", DefaultIdentitiesTopN),
		Reporting: ReportingConfig{
			CacheAge:   time.Duration(e.num("REPORTING_CACHE_SECONDS", 900)) * time.Second,
			RefreshAge: time.Duration(e.num("REPORTING_REFRESH_SECONDS", 60)) * time.Second,
		},
	}
	c.Console = ConsoleConfig{
		Addr: e.str("CONSOLE_ADDR", c.IngestAddr),
		// The console's public address, when it has a hostname of its own
		// (https://console.example.com beside an ingest-only PUBLIC_URL). The
		// login's resource, issuer and dashboards callback follow it, so the
		// console's own host needs no redirect= entry.
		URL:          strings.TrimSuffix(e.str("CONSOLE_URL", c.PublicURL), "/"),
		DBPath:       e.str("CONSOLE_DB_PATH", strings.TrimPrefix(c.Database, "sqlite://")),
		AuthDSN:      e.str("CONSOLE_AUTH_DSN", ""),
		QueryTimeout: e.dur("CONSOLE_QUERY_TIMEOUT", 10*time.Second),
		QueryMaxRows: e.num("CONSOLE_QUERY_MAX_ROWS", 1000),
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

// renamed maps each retired variable to its replacement. A set old name
// refuses the boot, so a leftover one cannot silently stop taking effect.
var renamed = []struct{ old, repl string }{
	{"LISTEN_ADDR", "INGEST_ADDR"},
	{"API_ADDR", "CONSOLE_ADDR"},
	{"API_URL", "CONSOLE_URL"},
	{"API_AUTH_DSN", "CONSOLE_AUTH_DSN"},
	{"API_DB_PATH", "CONSOLE_DB_PATH"},
	{"API_QUERY_TIMEOUT", "CONSOLE_QUERY_TIMEOUT"},
	{"API_QUERY_MAX_ROWS", "CONSOLE_QUERY_MAX_ROWS"},
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
	// Validate global retention (negative values only)
	if rc := c.Retention.Events; rc.RawDays < 0 || rc.AggregateDays < 0 {
		return fmt.Errorf("config: retention days must not be negative: %+v", rc)
	}
	for _, v := range []struct {
		name string
		n    int
	}{
		{"ATTRIBUTE_VALUES_TOP_N", c.AttributeValuesTopN},
		{"IDENTITIES_TOP_N", c.IdentitiesTopN},
	} {
		if v.n < 0 {
			return fmt.Errorf("config: %s must not be negative (0 keeps every value): %d", v.name, v.n)
		}
	}
	if c.AttributeBreakdownsMax < 0 {
		return fmt.Errorf("config: ATTRIBUTE_BREAKDOWNS_MAX must not be negative (0 is no limit): %d", c.AttributeBreakdownsMax)
	}
	if c.Retention.ArchivedDays < 0 {
		return fmt.Errorf("config: RETENTION_ARCHIVED_DAYS must not be negative: %d", c.Retention.ArchivedDays)
	}
	if c.Reporting.CacheAge < 0 {
		return fmt.Errorf("config: REPORTING_CACHE_SECONDS must not be negative")
	}
	if c.Reporting.RefreshAge < 0 {
		return fmt.Errorf("config: REPORTING_REFRESH_SECONDS must not be negative")
	}
	if c.Reporting.CacheAge > 0 && c.Reporting.RefreshAge > c.Reporting.CacheAge {
		return fmt.Errorf("config: REPORTING_REFRESH_SECONDS (%d) exceeds REPORTING_CACHE_SECONDS (%d)",
			int(c.Reporting.RefreshAge/time.Second), int(c.Reporting.CacheAge/time.Second))
	}
	return nil
}

// MaxEventAge is the raw window: a clamped timestamp can never land in a
// day already rolled up.
func (c *Config) MaxEventAge() time.Duration {
	return time.Duration(c.Retention.Events.RawDays) * 24 * time.Hour
}

// parseConsoleAuthDSN fans CONSOLE_AUTH_DSN out into the mode-specific ConsoleConfig
// fields. Called from parse once the rest of the config (PUBLIC_URL for
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
			return fmt.Errorf("config: CONSOLE_AUTH_DSN oauth:// requires CONSOLE_URL, PUBLIC_URL or ?resource=<origin> to derive the resource from")
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
		return fmt.Errorf("config: CONSOLE_AUTH_DSN token:// password= requires CONSOLE_URL, PUBLIC_URL or resource=<origin> to derive the resource from")
	}
	if err := checkLoginURL(m.ResourceURL, m.Insecure); err != nil {
		return fmt.Errorf("config: CONSOLE_AUTH_DSN token:// resource=%q %v", m.ResourceURL, err)
	}
	return nil
}

// resource resolves the console resource identifier: resource= when given,
// else CONSOLE_URL (which defaults to PUBLIC_URL), either way read as an
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
			if c.Console.URL == c.PublicURL {
				name = "PUBLIC_URL (CONSOLE_URL's default)"
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
